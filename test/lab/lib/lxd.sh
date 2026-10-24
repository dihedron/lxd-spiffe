# shellcheck shell=bash
# The LXD servers on the lab's VMs: the snap from its channel at a recorded
# revision with refreshes held, the preseed, and the checks that the server
# answers on the VM and over the lab network.

readonly LXD_PORT=8443

# lab_servers: the LXD servers of the selected topology: the standalone ones,
# then the chosen clusters (each after its sibling, whose revision it runs);
# with --only, that server alone (none for spire).
lab_servers() {
	if [[ -n "$LAB_ONLY" ]]; then
		[[ "$LAB_ONLY" == spire ]] || echo "$LAB_ONLY"
		return
	fi
	echo lxd5
	echo lxd6
	lab_clusters
}

# lxd_channel SERVER: the snap channel the server follows.
lxd_channel() {
	case "$1" in
	lxd5) echo "$LAB_LXD5_CHANNEL" ;;
	*) echo "$LAB_LXD6_CHANNEL" ;;
	esac
}

# lxd_pinned_revision SERVER: the revision the settings pin, or nothing.
lxd_pinned_revision() {
	case "$1" in
	lxd5) echo "$LAB_LXD5_REVISION" ;;
	*) echo "$LAB_LXD6_REVISION" ;;
	esac
}

lxd_endpoint() { echo "https://$(vm_ip "$1"):$LXD_PORT"; }

# lxd_store_revision CHANNEL: the amd64 revision and version CHANNEL has in
# the Snap Store now, as "REVISION VERSION". The channel map names the latest
# track's channels without it ("stable", not "latest/stable"), so the match is
# on track and risk.
lxd_store_revision() {
	local channel="$1" track risk
	if [[ "$channel" == */* ]]; then
		track="${channel%%/*}" risk="${channel#*/}"
	else
		track=latest risk="$channel"
	fi
	[[ "$risk" != */* ]] || die "snap channel $channel: branches are not supported"
	curl -fsS --proto '=https' -H 'Snap-Device-Series: 16' 'https://api.snapcraft.io/v2/snaps/info/lxd?fields=revision,version' |
		jq -er --arg track "$track" --arg risk "$risk" '
			first(."channel-map"[]
				| select(.channel.architecture == "amd64" and .channel.track == $track and .channel.risk == $risk)
				| "\(.revision) \(.version)")' ||
		die "the Snap Store has no amd64 revision of lxd in channel $channel"
}

# lxd_resolve SERVER: records the server's channel and revision in env.json,
# unless a previous (possibly partial) "up" did: the pinned revision, or the
# channel's current one. A cluster runs its standalone sibling's, resolved
# before it (lab_servers lists the siblings first).
lxd_resolve() {
	local server="$1" channel revision found sibling member
	[[ "$(env_get ".servers[\"$server\"].revision // empty")" == "" ]] || return 0
	if is_cluster "$server"; then
		sibling="$(cluster_sibling "$server")"
		channel="$(env_get ".servers[\"$sibling\"].channel // empty")"
		revision="$(env_get ".servers[\"$sibling\"].revision // empty")"
		[[ -n "$revision" ]] || die "$server: $sibling's revision is not resolved"
		info "$server: LXD $channel revision $revision, as $sibling"
		env_set ".servers[\"$server\"].channel" "$channel"
		env_set ".servers[\"$server\"].revision" "$revision"
		env_set ".servers[\"$server\"].endpoint" "$(lxd_endpoint "$server")"
		env_set ".servers[\"$server\"].cluster" yes
		for member in $(cluster_members "$server"); do
			env_set ".servers[\"$server\"].members[\"$member\"].endpoint" "$(lxd_endpoint "$member")"
		done
		return 0
	fi
	channel="$(lxd_channel "$server")"
	revision="$(lxd_pinned_revision "$server")"
	if [[ -z "$revision" ]]; then
		found="$(lxd_store_revision "$channel")"
		revision="${found%% *}"
		info "$server: LXD $channel is revision $revision (${found#* })"
	else
		info "$server: LXD $channel pinned at revision $revision"
	fi
	env_set ".servers[\"$server\"].channel" "$channel"
	env_set ".servers[\"$server\"].revision" "$revision"
	env_set ".servers[\"$server\"].endpoint" "$(lxd_endpoint "$server")"
}

# snap_hold SERVER: holds LXD's refreshes and the automatic refreshes of every
# other snap, then waits for any change already under way: an automatic
# refresh of LXD's base snap (core24, core26) unmounts it for a moment, and
# lxc then fails with "cannot locate base snap". A VM reverted to an older
# snapshot is the likeliest to start one at boot.
snap_hold() {
	local vm="$1"
	vm_ssh "$vm" 'set -e
		sudo snap wait system seed.loaded
		sudo snap refresh --hold lxd >/dev/null
		sudo snap refresh --hold >/dev/null
		for _ in $(seq 120); do
			snap changes | awk "NR > 1 && \$2 ~ /^(Do|Doing|Undo|Undoing|Wait)$/ { busy = 1 } END { exit !busy }" || exit 0
			sleep 5
		done
		exit 1' || die "$vm: snapd is still busy after 10 minutes (lab.sh ssh $vm snap changes)"
}

# lxd_install SERVER: installs the snap at the recorded channel and revision
# on each of the server's VMs, and holds refreshes (snap_hold).
lxd_install() {
	local server="$1" vm
	for vm in $(server_vms "$server"); do
		lxd_install_vm "$vm" "$(env_get ".servers[\"$server\"].channel")" "$(env_get ".servers[\"$server\"].revision")"
	done
}

# lxd_install_vm VM CHANNEL REVISION
lxd_install_vm() {
	local server="$1" channel="$2" revision="$3" current tracking
	vm_ssh "$server" "sudo snap wait system seed.loaded" >/dev/null
	read -r current tracking < <(vm_ssh "$server" "snap list lxd 2>/dev/null" | awk 'NR == 2 { print $3, $4 }') || true
	if [[ -z "$current" ]]; then
		info "$server: installing LXD from $channel"
		vm_ssh "$server" "sudo snap install lxd --channel=$channel" >/dev/null
	elif [[ "$tracking" != "$channel" ]]; then
		info "$server: moving LXD from $tracking to $channel"
		vm_ssh "$server" "sudo snap refresh lxd --channel=$channel" >/dev/null
	fi
	current="$(vm_ssh "$server" "snap list lxd" | awk 'NR == 2 { print $3 }')"
	if [[ "$current" != "$revision" ]]; then
		info "$server: moving LXD from revision $current to $revision"
		vm_ssh "$server" "sudo snap refresh lxd --channel=$channel --revision=$revision" >/dev/null ||
			die "$server: the Snap Store refuses LXD revision $revision (no longer released?): pin another revision, or none, and run lab.sh down && lab.sh up"
	fi
	snap_hold "$server"
}

# lxd_allow_idmap SERVER: lets LXD map host uid and gid 1000 into c-raw
# (raw.idmap "both 1000 1000"). The snap reads /etc/subuid and /etc/subgid:
# root's default range (1000000, 1000000000) keeps every other map as it is
# without these files. Done before any instance exists.
lxd_allow_idmap() {
	local vm
	for vm in $(server_vms "$1"); do
		lxd_allow_idmap_vm "$vm"
	done
}

lxd_allow_idmap_vm() {
	local server="$1"
	vm_ssh "$server" "grep -qx root:1000:1 /etc/subuid && grep -qx root:1000:1 /etc/subgid" && return
	info "$server: allowing LXD to map uid and gid 1000 (raw.idmap)"
	vm_ssh "$server" 'set -e
		for f in /etc/subuid /etc/subgid; do
			grep -qx root:1000000:1000000000 "$f" || echo root:1000000:1000000000 | sudo tee -a "$f" >/dev/null
			grep -qx root:1000:1 "$f" || echo root:1000:1 | sudo tee -a "$f" >/dev/null
		done
		sudo snap restart lxd.daemon >/dev/null
		sudo lxd waitready --timeout=300' || die "$server: cannot allow LXD to map uid 1000"
}

# lxd_preseed SERVER [MEMBER]: the server's lxd init preseed; with MEMBER,
# the bootstrap of a cluster by that member.
lxd_preseed() {
	local storage
	case "$LAB_LXD_STORAGE" in
	zfs) storage="  driver: zfs
  config:
    size: ${LAB_LXD_ZFS_GIB}GiB" ;;
	dir) storage="  driver: dir" ;;
	*) die "LAB_LXD_STORAGE must be zfs or dir, not \"$LAB_LXD_STORAGE\"" ;;
	esac
	cat <<PRESEED
config:
  core.https_address: "$(vm_ip "$1"):$LXD_PORT"
networks:
- name: lxdbr0
  type: bridge
  config:
    ipv4.address: auto
    ipv4.nat: "true"
    ipv6.address: none
storage_pools:
- name: default
$storage
profiles:
- name: default
  devices:
    root:
      type: disk
      path: /
      pool: default
    eth0:
      type: nic
      name: eth0
      network: lxdbr0
PRESEED
	if [[ -n "${2:-}" ]]; then
		cat <<CLUSTER
cluster:
  server_name: $2
  enabled: true
CLUSTER
	fi
}

# lxd_join_preseed MEMBER TOKEN: the preseed by which MEMBER joins a
# cluster: the pool and the bridge are the cluster's; only the pool's size is
# the member's own.
lxd_join_preseed() {
	cat <<PRESEED
cluster:
  enabled: true
  server_name: $1
  server_address: "$(vm_ip "$1"):$LXD_PORT"
  cluster_token: "$2"
PRESEED
	if [[ "$LAB_LXD_STORAGE" == zfs ]]; then
		cat <<MEMBER
  member_config:
  - entity: storage-pool
    name: default
    key: size
    value: ${LAB_LXD_ZFS_GIB}GiB
MEMBER
	fi
}

# lxd_init_cluster CLUSTER: the first member bootstraps the cluster with the
# lab's preseed, the others join it with a join token each; members already
# in it are left alone.
lxd_init_cluster() {
	local cluster="$1" first member members token
	for member in $(cluster_members "$cluster"); do
		vm_ssh "$member" "sudo lxd waitready --timeout=300" || die "$member: LXD does not start (lab.sh logs $member snap.lxd.daemon)"
	done
	first="$(cluster_members "$cluster" | head -n1)"
	if ! members="$(vm_ssh "$first" "sudo lxc query /1.0/cluster/members" 2>/dev/null)" || [[ "$(jq -r 'length' <<<"$members")" == 0 ]]; then
		[[ -z "$(vm_ssh "$first" "sudo lxc storage list -f csv 2>/dev/null")" ]] ||
			die "$first: LXD is initialized but not clustered: lab.sh down, or reinstall LXD on it"
		info "$cluster: $first bootstraps the cluster ($LAB_LXD_STORAGE storage, lxdbr0, $(lxd_endpoint "$first"))"
		lxd_preseed "$first" "$first" | vm_ssh "$first" "sudo lxd init --preseed" ||
			die "$first: cannot bootstrap cluster $cluster"
		members="$(vm_ssh "$first" "sudo lxc query /1.0/cluster/members")"
	fi
	for member in $(cluster_members "$cluster" | tail -n +2); do
		jq -e --arg m "$member" '.[] | select(endswith("/" + $m))' <<<"$members" >/dev/null && continue
		info "$cluster: $member joins the cluster"
		# a token left by an interrupted up would make "cluster add" fail
		vm_ssh "$first" "sudo lxc cluster revoke-token $member" >/dev/null 2>&1 || true
		token="$(vm_ssh "$first" "sudo lxc cluster add $member --quiet")" || die "$cluster: no join token for $member"
		lxd_join_preseed "$member" "$token" | vm_ssh "$member" "sudo lxd init --preseed" ||
			die "$member: cannot join cluster $cluster"
	done
}

# lxd_init SERVER: initializes the server with its preseed, unless it has a
# storage pool already.
lxd_init() {
	local server="$1"
	if is_cluster "$server"; then
		lxd_init_cluster "$server"
		return
	fi
	vm_ssh "$server" "sudo lxd waitready --timeout=300" || die "$server: LXD does not start (lab.sh logs $server snap.lxd.daemon)"
	if [[ -n "$(vm_ssh "$server" "sudo lxc storage list -f csv 2>/dev/null")" ]]; then
		return
	fi
	info "$server: initializing LXD ($LAB_LXD_STORAGE storage, lxdbr0, $(lxd_endpoint "$server"))"
	lxd_preseed "$server" | vm_ssh "$server" "sudo lxd init --preseed" ||
		die "$server: lxd init failed"
}

# lxd_fingerprint_pem: the SHA-256 fingerprint of the PEM certificate on
# stdin, as LXD writes it (lowercase hex, no colons).
lxd_fingerprint_pem() {
	openssl x509 -noout -fingerprint -sha256 | sed 's/.*=//; s/://g' | tr 'A-F' 'a-f'
}

# lxd_check SERVER: checks that the server answers "lxc info" on its VM and
# GET /1.0 over the lab network with the certificate it has on disk, then
# records its version, extensions hash, certificate fingerprint and storage.
lxd_check() {
	local server="$1" endpoint local_json remote_json fp_local fp_wire version hash
	endpoint="$(lxd_endpoint "$server")"
	vm_ssh "$server" "sudo lxc info" >/dev/null || die "$server: lxc info fails"
	local_json="$(vm_ssh "$server" "sudo lxc query /1.0")" || die "$server: lxc query /1.0 fails"
	remote_json="$(curl -fsSk --proto '=https' --max-time 10 "$endpoint/1.0")" ||
		die "$server: GET $endpoint/1.0 fails from this host"
	[[ "$(jq -r .metadata.auth <<<"$remote_json")" == untrusted ]] ||
		die "$server: GET $endpoint/1.0 is not an untrusted answer: $(jq -c .metadata.auth <<<"$remote_json")"
	fp_local="$(jq -r .environment.certificate <<<"$local_json" | lxd_fingerprint_pem)"
	fp_wire="$(openssl s_client -connect "$(vm_ip "$server"):$LXD_PORT" </dev/null 2>/dev/null | lxd_fingerprint_pem)"
	[[ -n "$fp_local" && "$fp_local" == "$fp_wire" ]] ||
		die "$server: the certificate on $endpoint ($fp_wire) is not the server's ($fp_local)"
	is_cluster "$server" && lxd_check_members "$server" "$fp_local"
	version="$(jq -r .environment.server_version <<<"$local_json")"
	# as evidence.HashExtensions: sorted, joined by newlines, SHA-256
	hash="sha256:$(jq -j '.api_extensions | sort | join("\n")' <<<"$local_json" | sha256sum | cut -d' ' -f1)"
	env_set ".servers[\"$server\"].version" "$version"
	env_set ".servers[\"$server\"].api_extensions_hash" "$hash"
	env_set ".servers[\"$server\"].fingerprint" "$fp_local"
	env_set ".servers[\"$server\"].storage" "$(vm_ssh "$server" "sudo lxc query /1.0/storage-pools/default" | jq -r .driver)"
	env_set ".servers[\"$server\"].bridge" "$(vm_ssh "$server" "sudo lxc network get lxdbr0 ipv4.address")"
	info "$server: LXD $version at $endpoint, pin ${fp_local:0:16}..."
}

# lxd_check_members CLUSTER PIN: every member is online, and presents the
# cluster's certificate (PIN) on its own address.
lxd_check_members() {
	local cluster="$1" pin="$2" members member fp
	members="$(vm_lxc "$cluster" query "/1.0/cluster/members?recursion=1")" || die "$cluster: cannot list the members"
	for member in $(cluster_members "$cluster"); do
		[[ "$(jq -r --arg m "$member" '.[] | select(.server_name == $m) | .status' <<<"$members")" == Online ]] ||
			die "$cluster: $member is not an online member: $(jq -c --arg m "$member" '.[] | select(.server_name == $m) | {status, message}' <<<"$members")"
		fp="$(openssl s_client -connect "$(vm_ip "$member"):$LXD_PORT" </dev/null 2>/dev/null | lxd_fingerprint_pem)"
		[[ "$fp" == "$pin" ]] || die "$cluster: $member presents $fp, not the cluster's certificate $pin"
	done
}

# lxd_verify SERVER: waits until the server is ready, then checks that it
# answers over the lab network with the pin and version env.json records.
lxd_verify() {
	local server="$1" fp version vm
	for vm in $(server_vms "$server"); do
		snap_hold "$vm"
		vm_ssh "$vm" "sudo lxd waitready --timeout=300" || die "$vm: LXD does not start (lab.sh logs $vm snap.lxd.daemon)"
	done
	fp="$(openssl s_client -connect "$(vm_ip "$server"):$LXD_PORT" </dev/null 2>/dev/null | lxd_fingerprint_pem)"
	[[ "$fp" == "$(env_get ".servers[\"$server\"].fingerprint")" ]] ||
		die "$server: the certificate on $(lxd_endpoint "$server") ($fp) is not the recorded pin"
	version="$(vm_lxc "$server" query /1.0 | jq -r .environment.server_version)"
	[[ "$version" == "$(env_get ".servers[\"$server\"].version")" ]] ||
		die "$server: LXD is $version, env.json records $(env_get ".servers[\"$server\"].version")"
	if is_cluster "$server"; then
		lxd_check_members "$server" "$fp"
	fi
}

# lxd_refresh_plan SERVER [REVISION VERSION]: prints "SERVER CURRENT NEW
# VERSION" when the server's channel has a newer revision than the one
# installed; tells why not otherwise. A cluster follows its sibling: REVISION
# and VERSION are the sibling's after this refresh.
lxd_refresh_plan() {
	local server="$1" channel current pinned found
	channel="$(env_get ".servers[\"$server\"].channel")"
	current="$(env_get ".servers[\"$server\"].revision")"
	if is_cluster "$server"; then
		if [[ "$current" == "$2" ]]; then
			info "$server: at $(cluster_sibling "$server")'s revision $current" >&2
			return
		fi
		echo "$server $current $2 $3"
		return
	fi
	pinned="$(lxd_pinned_revision "$server")"
	if [[ -n "$pinned" ]]; then
		info "$server: pinned at revision $pinned by the settings: left alone" >&2
		return
	fi
	found="$(lxd_store_revision "$channel")"
	if [[ "${found%% *}" == "$current" ]]; then
		info "$server: LXD $channel is up to date (revision $current)" >&2
		return
	fi
	echo "$server $current ${found%% *} ${found#* }"
}

# lxd_refresh SERVER REVISION: moves the server to REVISION of its channel,
# holds refreshes again and records the server anew. A cluster's members are
# refreshed all at once: a refreshed member waits for the others to reach
# its version before it serves, so one at a time would gain nothing here.
lxd_refresh() {
	local server="$1" revision="$2" channel vm pids=() failed=()
	channel="$(env_get ".servers[\"$server\"].channel")"
	info "$server: refreshing LXD to revision $revision of $channel"
	for vm in $(server_vms "$server"); do
		vm_ssh "$vm" "set -e
			sudo snap refresh --unhold lxd >/dev/null
			sudo timeout 900 snap refresh lxd --channel=$channel --revision=$revision >/dev/null
			sudo snap refresh --hold lxd >/dev/null" &
		pids+=("$vm:$!")
	done
	for vm in "${pids[@]}"; do
		wait "${vm#*:}" || failed+=("${vm%%:*}")
	done
	((${#failed[@]} == 0)) ||
		die "$server: the refresh to revision $revision failed on ${failed[*]} (lab.sh logs <vm> snap.lxd.daemon); refreshes may be unheld: lab.sh ssh <vm> sudo snap refresh --hold lxd"
	for vm in $(server_vms "$server"); do
		vm_ssh "$vm" "sudo lxd waitready --timeout=600" ||
			die "$vm: LXD is not ready after the refresh to revision $revision (lab.sh logs $vm snap.lxd.daemon)"
	done
	env_set ".servers[\"$server\"].revision" "$revision"
	lxd_check "$server"
}
