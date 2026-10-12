# shellcheck shell=bash
# The LXD servers on the lab's VMs: the snap from its channel at a recorded
# revision with refreshes held, the preseed, and the checks that the server
# answers on the VM and over the lab network.

readonly LXD_PORT=8443

# lab_servers: the LXD servers among the VMs of the selected scenarios.
lab_servers() { lab_vms | grep -v '^spire$'; }

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
	curl -fsS -H 'Snap-Device-Series: 16' 'https://api.snapcraft.io/v2/snaps/info/lxd?fields=revision,version' |
		jq -er --arg track "$track" --arg risk "$risk" '
			first(."channel-map"[]
				| select(.channel.architecture == "amd64" and .channel.track == $track and .channel.risk == $risk)
				| "\(.revision) \(.version)")' ||
		die "the Snap Store has no amd64 revision of lxd in channel $channel"
}

# lxd_resolve SERVER: records the server's channel and revision in env.json,
# unless a previous (possibly partial) "up" did: the pinned revision, or the
# channel's current one.
lxd_resolve() {
	local server="$1" channel revision found
	[[ "$(env_get ".servers[\"$server\"].revision // empty")" == "" ]] || return 0
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

# lxd_install SERVER: installs the snap at the recorded channel and revision,
# and holds its refreshes.
lxd_install() {
	local server="$1" channel revision current tracking
	channel="$(env_get ".servers[\"$server\"].channel")"
	revision="$(env_get ".servers[\"$server\"].revision")"
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
	vm_ssh "$server" "sudo snap refresh --hold lxd" >/dev/null
}

# lxd_allow_idmap SERVER: lets LXD map host uid and gid 1000 into c-raw
# (raw.idmap "both 1000 1000"). The snap reads /etc/subuid and /etc/subgid:
# root's default range (1000000, 1000000000) keeps every other map as it is
# without these files. Done before any instance exists.
lxd_allow_idmap() {
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

# lxd_preseed SERVER: the server's lxd init preseed.
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
}

# lxd_init SERVER: initializes the server with its preseed, unless it has a
# storage pool already.
lxd_init() {
	local server="$1"
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
	remote_json="$(curl -fsSk --max-time 10 "$endpoint/1.0")" ||
		die "$server: GET $endpoint/1.0 fails from this host"
	[[ "$(jq -r .metadata.auth <<<"$remote_json")" == untrusted ]] ||
		die "$server: GET $endpoint/1.0 is not an untrusted answer: $(jq -c .metadata.auth <<<"$remote_json")"
	fp_local="$(jq -r .environment.certificate <<<"$local_json" | lxd_fingerprint_pem)"
	fp_wire="$(openssl s_client -connect "$(vm_ip "$server"):$LXD_PORT" </dev/null 2>/dev/null | lxd_fingerprint_pem)"
	[[ -n "$fp_local" && "$fp_local" == "$fp_wire" ]] ||
		die "$server: the certificate on $endpoint ($fp_wire) is not the server's ($fp_local)"
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

# lxd_verify SERVER: waits until the server is ready, then checks that it
# answers over the lab network with the pin and version env.json records.
lxd_verify() {
	local server="$1" fp version
	vm_ssh "$server" "sudo lxd waitready --timeout=300" || die "$server: LXD does not start (lab.sh logs $server snap.lxd.daemon)"
	fp="$(openssl s_client -connect "$(vm_ip "$server"):$LXD_PORT" </dev/null 2>/dev/null | lxd_fingerprint_pem)"
	[[ "$fp" == "$(env_get ".servers[\"$server\"].fingerprint")" ]] ||
		die "$server: the certificate on $(lxd_endpoint "$server") ($fp) is not the recorded pin"
	version="$(vm_lxc "$server" query /1.0 | jq -r .environment.server_version)"
	[[ "$version" == "$(env_get ".servers[\"$server\"].version")" ]] ||
		die "$server: LXD is $version, env.json records $(env_get ".servers[\"$server\"].version")"
}
