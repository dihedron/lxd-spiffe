# shellcheck shell=bash
# The baseline instances of the probe's lab matrix, on every LXD server: four
# containers and three VMs in the default project, and one container in the
# restricted project. Each has a user "probe" with uid 1000.

readonly LAB_RESTRICTED_PROJECT=restricted
readonly LAB_RESTRICTED_BRIDGE=lxdbr1

# baseline_instances: "PROJECT NAME KIND IMAGE-SETTING CONFIG..." per line.
# In CONFIG, "_" stands for a space and @nosecureboot for the server's way to
# boot a VM without secure boot (Alpine's images are not signed). c-priv needs
# nesting: under LXD's AppArmor profile, a privileged Ubuntu 24.04 container
# cannot set up the mount namespaces of systemd's services (no network).
# Loops read it on fd 3: the ssh inside them reads stdin.
baseline_instances() {
	cat <<INSTANCES
default c-unpriv container LAB_IMAGE_UBUNTU
default c-priv container LAB_IMAGE_UBUNTU security.privileged=true security.nesting=true
default c-iso container LAB_IMAGE_UBUNTU security.idmap.isolated=true
default c-raw container LAB_IMAGE_UBUNTU raw.idmap=both_1000_1000
default v-ubuntu vm LAB_IMAGE_UBUNTU
default v-debian vm LAB_IMAGE_DEBIAN
default v-alpine vm LAB_IMAGE_ALPINE @nosecureboot
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
			[[ "$config" == @nosecureboot ]] && config="$(nosecureboot_config "$server")"
			args+=(-c "${config//_/ }")
		done
		info "$server: launching $project/$name ($kind, $image)"
		if ! vm_lxc "$server" "${args[@]}" >/dev/null; then
			# a launch that fails to start leaves the instance created: the
			# next up would take it as done
			vm_lxc "$server" delete --force "$name" --project "$project" >/dev/null 2>&1 || true
			die "$server: cannot launch $project/$name"
		fi
	done 3< <(baseline_instances)
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
	done 3< <(baseline_instances)
	info "$server: the baseline instances are ready"
}

# baseline_grants SERVER: the identities' grants on the baseline instances.
baseline_grants() {
	local server="$1" project
	for project in default "$LAB_RESTRICTED_PROJECT"; do
		# shellcheck disable=SC2046 # one instance name per word
		lab_grant "$server" "$project" $(baseline_instances | awk -v p="$project" '$1 == p { print $2 }')
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
	done 3< <(baseline_instances)
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
	done 3< <(baseline_instances)
}

# instance_status SERVER PROJECT NAME: Running, Stopped, ...
instance_status() {
	vm_lxc "$1" query "/1.0/instances/$3?project=$2" | jq -r .status
}
