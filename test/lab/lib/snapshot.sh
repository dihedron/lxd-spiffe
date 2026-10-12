# shellcheck shell=bash
# Snapshots of the whole lab, so that a session starts from a known state
# without a new "up". Safety comes before speed: the VMs run virtual machines
# of their own, whose nested state a memory snapshot may not restore
# faithfully, so snapshots are taken with the VMs shut off (disk only, internal
# to each qcow2) and a reset boots them cold. LXD stops its instances at
# shutdown, but may not start again a VM that powers off quickly (seen with
# LXD 5.21): the lab stops and starts its instances itself.

readonly LAB_SNAPSHOT=lab-base

snapshot_exists() {
	vsh snapshot-info "$(vm_domain "$1")" "$LAB_SNAPSHOT" >/dev/null 2>&1
}

all_snapshots_exist() {
	local vm
	for vm in $(lab_vms); do
		snapshot_exists "$vm" || return 1
	done
}

# vm_shutdown VM: shuts VM down cleanly, never by force.
vm_shutdown() {
	local vm="$1" deadline=$((SECONDS + 600))
	[[ "$(vm_state "$vm")" == "shut off" ]] && return
	info "shutting $vm down"
	vsh shutdown "$(vm_domain "$vm")" >/dev/null
	until [[ "$(vm_state "$vm")" == "shut off" ]]; do
		((SECONDS < deadline)) || die "$vm does not shut down (virsh console $(vm_domain "$vm")); no snapshot taken"
		sleep 3
	done
}

# lab_ready: waits until every VM answers over SSH and every server is ready
# with its recorded pin and version, then starts the baseline instances and
# waits until they run.
lab_ready() {
	local vm server
	for vm in $(lab_vms); do
		wait_for_vm "$vm"
	done
	for server in $(lab_servers); do
		lxd_verify "$server"
		start_instances "$server"
		wait_instances "$server"
	done
}

# lab_snapshot: stops the baseline instances and shuts the lab down,
# snapshots every VM's disks as the lab's baseline, and starts it again.
lab_snapshot() {
	local vm server
	for vm in $(lab_vms); do
		[[ "$(vm_state "$vm")" == running ]] || die "$vm is not running: run lab.sh up first"
	done
	if all_snapshots_exist && ! confirm "Replace the lab's snapshot, taken $(env_get '.snapshot.taken // "at an unknown time"')?"; then
		info "snapshot kept"
		return 1
	fi
	for server in $(lab_servers); do
		stop_instances "$server"
	done
	for vm in $(lab_vms); do
		vm_shutdown "$vm"
	done
	for vm in $(lab_vms); do
		if snapshot_exists "$vm"; then
			vsh snapshot-delete "$(vm_domain "$vm")" "$LAB_SNAPSHOT" >/dev/null
		fi
		info "snapshotting $vm"
		vsh snapshot-create-as "$(vm_domain "$vm")" "$LAB_SNAPSHOT" \
			--description "lxd-spiffe lab baseline" --atomic >/dev/null
	done
	env_set .snapshot.taken "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	for vm in $(lab_vms); do
		vsh start "$(vm_domain "$vm")" >/dev/null
	done
	lab_ready
	info "snapshot taken: lab.sh reset returns the lab to this state"
}

# lab_reset: powers every VM off, reverts its disks to the snapshot and boots
# it, then checks the servers and the instances.
lab_reset() {
	all_snapshots_exist || die "no snapshot of the whole lab: run lab.sh up (or lab.sh snapshot)"
	local vm
	for vm in $(lab_vms); do
		info "reverting $vm"
		# what runs now is discarded: no clean shutdown needed
		[[ "$(vm_state "$vm")" == "shut off" ]] || vsh destroy "$(vm_domain "$vm")" >/dev/null
		vsh snapshot-revert "$(vm_domain "$vm")" "$LAB_SNAPSHOT" >/dev/null
		[[ "$(vm_state "$vm")" == running ]] || vsh start "$(vm_domain "$vm")" >/dev/null
	done
	lab_ready
	info "the lab is back to the snapshot taken $(env_get '.snapshot.taken')"
}
