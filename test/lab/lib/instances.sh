# shellcheck shell=bash
# The baseline instances of the probe's lab matrix: on each standalone LXD
# server, four containers and five VMs in the default project, and one
# container in the restricted project; on each cluster, c-unpriv on the first
# member and v-ubuntu on the second (gate L11). Each has a user "probe" with
# uid 1000.

readonly LAB_RESTRICTED_PROJECT=restricted
readonly LAB_RESTRICTED_BRIDGE=lxdbr1

# baseline_instances SERVER: "PROJECT NAME KIND IMAGE-SETTING CONFIG..." per
# line.
# In CONFIG, "_" stands for a space and @nosecureboot for the server's way to
# boot a VM without secure boot (Alpine's images are not signed), and
# @selinux-enforcing a VM that ensure_selinux makes run SELinux enforcing,
# and @member=N the cluster member the instance is created on. c-priv needs
# nesting: under LXD's AppArmor profile, a privileged Ubuntu 24.04 container
# cannot set up the mount namespaces of systemd's services (no network).
# Loops read it on fd 3: the ssh inside them reads stdin.
baseline_instances() {
	if is_cluster "${1:?baseline_instances needs a server}"; then
		cat <<INSTANCES
default c-unpriv container LAB_IMAGE_UBUNTU @member=1
default v-ubuntu vm LAB_IMAGE_UBUNTU @member=2
INSTANCES
		return
	fi
	cat <<INSTANCES
default c-unpriv container LAB_IMAGE_UBUNTU
default c-priv container LAB_IMAGE_UBUNTU security.privileged=true security.nesting=true
default c-iso container LAB_IMAGE_UBUNTU security.idmap.isolated=true
default c-raw container LAB_IMAGE_UBUNTU raw.idmap=both_1000_1000
default v-ubuntu vm LAB_IMAGE_UBUNTU
default v-debian vm LAB_IMAGE_DEBIAN
default v-alpine vm LAB_IMAGE_ALPINE @nosecureboot
default v-rocky vm LAB_IMAGE_ROCKY @selinux-enforcing
default v-alma vm LAB_IMAGE_ALMA
$LAB_RESTRICTED_PROJECT c-unpriv container LAB_IMAGE_UBUNTU
INSTANCES
}

# The guests' cloud-init: the probe user with uid 1000, and no default user
# (Ubuntu's would take uid 1000). Alpine's image has a user of its own with
# uid 1000, which bootcmd (before the users module) removes.
guest_user_data() {
	cat <<'USER'
#cloud-config
bootcmd:
  - - sh
    - -c
    - |
      u="$(getent passwd 1000 | cut -d: -f1)"
      if [ -n "$u" ] && [ "$u" != probe ]; then userdel -r "$u" || exit 1; fi
      g="$(getent group 1000 | cut -d: -f1)"
      if [ -n "$g" ] && [ "$g" != probe ]; then groupdel "$g" || exit 1; fi
      exit 0
users:
  - name: probe
    uid: 1000
    shell: /bin/sh
    lock_passwd: true
USER
}

# ensure_restricted_project SERVER: the restricted project, sharing the
# default project's images. LXD restricts only projects with profiles of their
# own, so its default profile gets a root disk and a NIC, both managed (allowed
# in a restricted project). The NIC is on a bridge of the project's own: LXD
# keeps instance DNS names unique per network, and the project's c-unpriv
# shares its name with the default project's.
ensure_restricted_project() {
	local server="$1" project="$LAB_RESTRICTED_PROJECT"
	if ! vm_lxc "$server" network show "$LAB_RESTRICTED_BRIDGE" >/dev/null 2>&1; then
		info "$server: creating network $LAB_RESTRICTED_BRIDGE"
		vm_lxc "$server" network create "$LAB_RESTRICTED_BRIDGE" >/dev/null
	fi
	if ! vm_lxc "$server" project list -f csv | grep -q "^$project[ ,]"; then
		info "$server: creating project $project"
		vm_lxc "$server" project create "$project" \
			-c restricted=true -c features.images=false -c features.profiles=true >/dev/null
	fi
	if ! vm_lxc "$server" profile device get default root pool --project "$project" >/dev/null 2>&1; then
		vm_lxc "$server" profile device add default root disk path=/ pool=default --project "$project" >/dev/null
	fi
	case "$(vm_lxc "$server" profile device get default eth0 network --project "$project" 2>/dev/null)" in
	"$LAB_RESTRICTED_BRIDGE") ;;
	"") vm_lxc "$server" profile device add default eth0 nic network="$LAB_RESTRICTED_BRIDGE" name=eth0 --project "$project" >/dev/null ;;
	*) vm_lxc "$server" profile device set default eth0 network="$LAB_RESTRICTED_BRIDGE" --project "$project" >/dev/null ;;
	esac
}

# nosecureboot_config SERVER: the setting that turns secure boot off, which
# LXD 6 moved from security.secureboot into boot.mode (instance_boot_mode).
nosecureboot_config() {
	if lxd_has_extension "$1" instance_boot_mode; then
		echo boot.mode=uefi-nosecureboot
	else
		echo security.secureboot=false
	fi
}

# ensure_instances SERVER: launches the baseline instances that are missing.
ensure_instances() {
	local server="$1" project name kind setting image args config
	while read -r -u 3 project name kind setting config; do
		vm_lxc "$server" query "/1.0/instances/$name?project=$project" >/dev/null 2>&1 && continue
		image="${!setting}"
		args=(launch "$image" "$name" --project "$project" -c "cloud-init.user-data=$(guest_user_data)")
		if [[ "$kind" == vm ]]; then
			args+=(--vm -c "limits.cpu=$LAB_GUEST_VM_VCPUS" -c "limits.memory=${LAB_GUEST_VM_RAM_GIB}GiB")
		fi
		for config in $config; do
			[[ "$config" == @selinux-enforcing ]] && continue # ensure_selinux
			if [[ "$config" == @member=* ]]; then
				args+=(--target "$(cluster_members "$server" | sed -n "${config#@member=}p")")
				continue
			fi
			[[ "$config" == @nosecureboot ]] && config="$(nosecureboot_config "$server")"
			args+=(-c "${config//_/ }")
		done
		info "$server: launching $project/$name ($kind, $image)"
		LAB_BASELINE_CHANGED=1
		if ! vm_lxc "$server" "${args[@]}" >/dev/null; then
			# a launch that fails to start leaves the instance created: the
			# next up would take it as done
			vm_lxc "$server" delete --force "$name" --project "$project" >/dev/null 2>&1 || true
			die "$server: cannot launch $project/$name"
		fi
	done 3< <(baseline_instances "$server")
}

# wait_instances SERVER: waits until every baseline instance runs commands
# (VMs: lxd-agent answers) and has finished cloud-init with the probe user,
# then records the instances in env.json.
wait_instances() {
	local server="$1" project name kind deadline uid json
	while read -r -u 3 project name kind _; do
		deadline=$((SECONDS + 600))
		until vm_lxc "$server" exec "$name" --project "$project" -- true >/dev/null 2>&1; do
			((SECONDS < deadline)) || die "$server: $project/$name does not answer lxc exec (lab.sh ssh $server sudo lxc console $name --project $project --show-log)"
			sleep 5
		done
		vm_lxc "$server" exec "$name" --project "$project" -- cloud-init status --wait >/dev/null 2>&1 ||
			info "warning: $server: cloud-init of $project/$name reports errors"
		uid="$(vm_lxc "$server" exec "$name" --project "$project" -- id -u probe 2>/dev/null)" || true
		[[ "$uid" == 1000 ]] || die "$server: $project/$name has no user probe with uid 1000 (got \"$uid\")"
		json="$(vm_lxc "$server" query "/1.0/instances/$name?project=$project")"
		env_set ".servers[\"$server\"].instances[\"$project/$name\"].type" "$(jq -r .type <<<"$json")"
		env_set ".servers[\"$server\"].instances[\"$project/$name\"].image" "$(jq -r '.config["image.description"] // ""' <<<"$json")"
		env_set ".servers[\"$server\"].instances[\"$project/$name\"].fingerprint" "$(jq -r '.config["volatile.base_image"]' <<<"$json")"
		env_set ".servers[\"$server\"].instances[\"$project/$name\"].location" "$(jq -r '.location' <<<"$json")"
	done 3< <(baseline_instances "$server")
	info "$server: the baseline instances are ready"
}

# selinux_setup_script: the guest side of @selinux-enforcing (probe spec
# gate L7): the targeted policy and audit (dnf retried: mirrors fail now and
# then), the drop-in that lets lxd-agent start under enforcing (it runs from
# /run/lxd_agent as var_run_t, in init_t, which may not create vsock
# sockets; as bin_t it runs in unconfined_service_t), and a relabel.
selinux_setup_script() {
	cat <<'SETUP'
set -e
for t in 1 2 3 4 5; do dnf -y -q install selinux-policy-targeted audit >/dev/null 2>&1 && break; sleep 15; done
rpm -q selinux-policy-targeted audit >/dev/null
grep -q '^SELINUX=enforcing' /etc/selinux/config
mkdir -p /etc/systemd/system/lxd-agent.service.d
cat >/etc/systemd/system/lxd-agent.service.d/selinux.conf <<'DROPIN'
[Service]
ExecStartPre=/usr/bin/chcon -t bin_t /run/lxd_agent/lxd-agent
DROPIN
systemctl enable auditd >/dev/null 2>&1
touch /.autorelabel
sync
SETUP
}

# guest_exec SERVER PROJECT NAME COMMAND: runs COMMAND (a shell string) in
# an instance, bounded: lxc exec into a VM that is still booting can hang.
guest_exec() {
	vm_ssh "$1" "sudo timeout 60 lxc exec $(printf '%q' "$3") --project $(printf '%q' "$2") -- sh -c $(printf '%q' "$4")"
}

# ensure_selinux SERVER: makes the baseline VMs marked @selinux-enforcing
# run SELinux enforcing with lxd-agent in unconfined_service_t; one that
# already does is left alone. The relabel boot ends with the VM powered
# off, so it is started again.
ensure_selinux() {
	local server="$1" project name config deadline status label
	while read -r -u 3 project name _ _ config; do
		[[ " $config " == *" @selinux-enforcing "* ]] || continue
		[[ "$(guest_exec "$server" "$project" "$name" getenforce 2>/dev/null)" == Enforcing ]] && continue
		info "$server: making $project/$name run SELinux enforcing (policy, lxd-agent drop-in, relabel)"
		guest_exec "$server" "$project" "$name" "$(selinux_setup_script)" >/dev/null ||
			die "$server: cannot set up SELinux in $project/$name (the package mirrors may be unreachable: run lab.sh up again)"
		vm_lxc "$server" restart --force "$name" --project "$project" >/dev/null
		LAB_BASELINE_CHANGED=1
		deadline=$((SECONDS + 1200))
		until [[ "$(guest_exec "$server" "$project" "$name" getenforce 2>/dev/null)" == Enforcing ]]; do
			((SECONDS < deadline)) || die "$server: $project/$name does not come up enforcing (lab.sh ssh $server sudo lxc console $name --project $project)"
			status="$(vm_lxc "$server" query "/1.0/instances/$name?project=$project" 2>/dev/null | jq -r .status)"
			[[ "$status" == Stopped ]] && vm_lxc "$server" start "$name" --project "$project" >/dev/null 2>&1
			sleep 10
		done
		label="$(guest_exec "$server" "$project" "$name" "ps -eo label,comm | awk '\$2 == \"lxd-agent\" { print \$1 }'")"
		[[ "$label" == *:unconfined_service_t:* ]] || die "$server: lxd-agent in $project/$name runs as \"$label\", not unconfined_service_t"
		info "$server: $project/$name runs SELinux enforcing, lxd-agent as $label"
	done 3< <(baseline_instances "$server")
}

# baseline_grants SERVER: the identities' grants on the baseline instances.
baseline_grants() {
	local server="$1" project
	for project in $(baseline_instances "$server" | awk '{ print $1 }' | sort -u); do
		# shellcheck disable=SC2046 # one instance name per word
		lab_grant "$server" "$project" $(baseline_instances "$server" | awk -v p="$project" '$1 == p { print $2 }')
	done
}

# stop_instances SERVER: stops the running baseline instances cleanly.
stop_instances() {
	local server="$1" project name
	while read -r -u 3 project name _; do
		[[ "$(instance_status "$server" "$project" "$name")" == Running ]] || continue
		info "$server: stopping $project/$name"
		vm_lxc "$server" stop "$name" --project "$project" --timeout 120 >/dev/null ||
			die "$server: $project/$name does not stop cleanly; no snapshot taken"
	done 3< <(baseline_instances "$server")
}

# start_instances SERVER: starts the baseline instances that are not running.
# The lab does not rely on LXD to start again the instances it stopped at
# shutdown.
start_instances() {
	local server="$1" project name
	while read -r -u 3 project name _; do
		[[ "$(instance_status "$server" "$project" "$name")" == Running ]] && continue
		info "$server: starting $project/$name"
		vm_lxc "$server" start "$name" --project "$project" >/dev/null ||
			die "$server: cannot start $project/$name"
	done 3< <(baseline_instances "$server")
}

# instance_status SERVER PROJECT NAME: Running, Stopped, ...
instance_status() {
	vm_lxc "$1" query "/1.0/instances/$3?project=$2" | jq -r .status
}
