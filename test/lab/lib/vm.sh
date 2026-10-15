# shellcheck shell=bash
# The lab's libvirt resources: storage pool, base image, network and VMs.
# Adapted from the openstack-spiffe lab; every resource is named after
# LAB_NETWORK (lxd-lab), so the two labs never touch each other's.

readonly LAB_USER=lab

vsh() { virsh -q -c qemu:///system "$@"; }

# vm_mac VM: the VM's fixed MAC, from which libvirt's DHCP gives it its address
# (52:54:00:fb: the openstack-spiffe lab uses 52:54:00:fa:).
vm_mac() { printf '52:54:00:fb:00:%02x' "$(vm_host "$1")"; }

# vm_state VM: the domain's state (running, shut off...), or "absent".
vm_state() { vsh domstate "$(vm_domain "$1")" 2>/dev/null || echo absent; }

# --- SSH ----------------------------------------------------------------------

ssh_key() { echo "$LAB_STATE_DIR/id_ed25519"; }

# SSH_OPTS: the options of every SSH connection to the VMs (set by load_ssh).
SSH_OPTS=()
load_ssh() {
	SSH_OPTS=(-i "$(ssh_key)" -o "UserKnownHostsFile=$LAB_STATE_DIR/known_hosts"
		-o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 -o BatchMode=yes
		-o LogLevel=ERROR -o ServerAliveInterval=15)
}

# vm_ssh VM [COMMAND...]: runs COMMAND (a shell string) on VM as the lab user.
vm_ssh() {
	local vm="$1"
	shift
	ssh "${SSH_OPTS[@]}" "$LAB_USER@$(vm_ip "$vm")" "$@"
}

ensure_ssh_key() {
	[[ -f "$(ssh_key)" ]] || ssh-keygen -q -t ed25519 -N '' -C "lxd-spiffe-lab" -f "$(ssh_key)"
}

# --- Storage ------------------------------------------------------------------

ensure_pool() {
	if ! vsh pool-info "$LAB_POOL" >/dev/null 2>&1; then
		info "creating storage pool $LAB_POOL in $LAB_POOL_DIR"
		vsh pool-define-as "$LAB_POOL" dir --target "$LAB_POOL_DIR" >/dev/null
		vsh pool-build "$LAB_POOL" >/dev/null
		vsh pool-autostart "$LAB_POOL" >/dev/null
	fi
	[[ "$(vsh pool-info "$LAB_POOL" | awk '/^State:/ { print $2 }')" == running ]] || vsh pool-start "$LAB_POOL" >/dev/null
}

# upload_volume NAME FILE: uploads FILE as volume NAME, unless it exists.
upload_volume() {
	vsh vol-info --pool "$LAB_POOL" "$1" >/dev/null 2>&1 && return 0
	info "uploading $1"
	vsh vol-create-as "$LAB_POOL" "$1" "$(stat -c %s "$2")" --format raw >/dev/null
	vsh vol-upload --pool "$LAB_POOL" "$1" "$2"
	vsh pool-refresh "$LAB_POOL" >/dev/null
}

# base_volume: the name of the base image volume, which includes the start of
# the image's checksum: a newer cloud image becomes a new base, never altering
# the VMs built on the previous one.
base_volume() {
	local sha
	sha="$(env_get .image.sha256)"
	echo "base-ubuntu-${sha:0:12}.qcow2"
}

ensure_base_image() {
	local path
	path="$(fetch "$(env_get .image.url)" "$(env_get .image.sha256)")"
	upload_volume "$(base_volume)" "$path"
}

# --- Network ------------------------------------------------------------------

ensure_network() {
	if ! vsh net-info "$LAB_NETWORK" >/dev/null 2>&1; then
		info "creating network $LAB_NETWORK ($LAB_SUBNET)"
		local xml="$LAB_STATE_DIR/network.xml" vm
		{
			echo "<network>"
			echo "  <name>$LAB_NETWORK</name>"
			echo "  <forward mode='nat'/>"
			echo "  <bridge stp='on' delay='0'/>"
			echo "  <domain name='lab' localOnly='yes'/>"
			echo "  <dns>"
			for vm in "${LAB_ALL_VMS[@]}"; do
				echo "    <host ip='$(vm_ip "$vm")'><hostname>$vm.lab</hostname></host>"
			done
			echo "  </dns>"
			echo "  <ip address='$(lab_ip 1)' netmask='255.255.255.0'>"
			echo "    <dhcp>"
			echo "      <range start='$(lab_ip 200)' end='$(lab_ip 250)'/>"
			for vm in "${LAB_ALL_VMS[@]}"; do
				echo "      <host mac='$(vm_mac "$vm")' name='$vm' ip='$(vm_ip "$vm")'/>"
			done
			echo "    </dhcp>"
			echo "  </ip>"
			echo "</network>"
		} >"$xml"
		vsh net-define "$xml" >/dev/null
		vsh net-autostart "$LAB_NETWORK" >/dev/null
	fi
	[[ "$(vsh net-info "$LAB_NETWORK" | awk '/^Active:/ { print $2 }')" == yes ]] || vsh net-start "$LAB_NETWORK" >/dev/null
	ensure_network_hosts
}

# ensure_network_hosts: brings the network's DHCP and DNS host entries to the
# lab's VMs (LAB_ALL_VMS), on the running network and in its definition: a
# network defined by an older lab knows other VMs (node1-node3 before the
# clusters). Entries are replaced by address.
ensure_network_hosts() {
	local xml vm ip name want=() entry
	xml="$(vsh net-dumpxml "$LAB_NETWORK")"
	for vm in "${LAB_ALL_VMS[@]}"; do
		want+=("$(vm_ip "$vm") $vm")
	done
	# entries whose address or name the lab no longer has
	while read -r ip name; do
		[[ " ${want[*]} " == *" $ip $name "* ]] && continue
		info "network $LAB_NETWORK: removing the entries of $name ($ip)"
		vsh net-update "$LAB_NETWORK" delete ip-dhcp-host "<host ip='$ip'/>" --live --config >/dev/null 2>&1 || true
		vsh net-update "$LAB_NETWORK" delete dns-host "<host ip='$ip'/>" --live --config >/dev/null 2>&1 || true
	done < <(sed -n "s/.*<host mac='[^']*' name='\([^']*\)' ip='\([^']*\)'.*/\2 \1/p" <<<"$xml")
	xml="$(vsh net-dumpxml "$LAB_NETWORK")"
	for entry in "${want[@]}"; do
		read -r ip vm <<<"$entry"
		if ! grep -q "<host mac='$(vm_mac "$vm")' name='$vm' ip='$ip'/>" <<<"$xml"; then
			info "network $LAB_NETWORK: adding $vm ($ip)"
			vsh net-update "$LAB_NETWORK" add ip-dhcp-host "<host mac='$(vm_mac "$vm")' name='$vm' ip='$ip'/>" --live --config >/dev/null
		fi
		if ! grep -q "<hostname>$vm.lab</hostname>" <<<"$xml"; then
			vsh net-update "$LAB_NETWORK" add dns-host "<host ip='$ip'><hostname>$vm.lab</hostname></host>" --live --config >/dev/null
		fi
	done
}

# --- VMs ----------------------------------------------------------------------

# seed_image VM: builds the cloud-init seed of VM and prints its path.
seed_image() {
	local vm="$1" dir="$LAB_STATE_DIR/seed/$1"
	mkdir -p "$dir"
	cat >"$dir/meta-data" <<META
instance-id: $vm-$(date +%s)
local-hostname: $vm
META
	cat >"$dir/user-data" <<USER
#cloud-config
hostname: $vm
fqdn: $vm.lab
manage_etc_hosts: true
ssh_pwauth: false
users:
  - name: $LAB_USER
    gecos: lxd-spiffe lab
    shell: /bin/bash
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat "$(ssh_key).pub")
USER
	local iso="$dir/seed.iso"
	if have cloud-localds; then
		cloud-localds "$iso" "$dir/user-data" "$dir/meta-data"
	elif have xorriso; then
		xorriso -as genisoimage -quiet -output "$iso" -volid cidata -joliet -rock "$dir/user-data" "$dir/meta-data" 2>/dev/null
	else
		genisoimage -quiet -output "$iso" -volid cidata -joliet -rock "$dir/user-data" "$dir/meta-data"
	fi
	echo "$iso"
}

# vm_osinfo: the most precise OS name this host's libvirt knows for the VMs'
# image (e.g. ubuntu24.04), or a recent generic Linux.
vm_osinfo() {
	local known name
	known=" $(virt-install --osinfo list 2>/dev/null | tr ',' '\n' | tr -d ' ' | tr '\n' ' ') "
	for name in "ubuntu$LAB_UBUNTU_RELEASE" linux2024 linux2022; do
		if [[ "$known" == *" $name "* ]]; then
			echo "$name"
			return
		fi
	done
	echo generic
}

# ensure_vm VM: creates and starts VM, or starts it if it is shut off.
ensure_vm() {
	local vm="$1" domain
	domain="$(vm_domain "$vm")"
	if vsh dominfo "$domain" >/dev/null 2>&1; then
		resize_vm "$vm"
		[[ "$(vm_state "$vm")" == running ]] || vsh start "$domain" >/dev/null
		return
	fi
	info "creating VM $vm ($(vm_ip "$vm"))"
	local disk="$vm.qcow2" seed="$vm-seed.iso"
	vsh vol-delete --pool "$LAB_POOL" "$disk" >/dev/null 2>&1 || true
	vsh vol-delete --pool "$LAB_POOL" "$seed" >/dev/null 2>&1 || true
	vsh vol-create-as "$LAB_POOL" "$disk" "$(vm_size "$vm" disk)G" --format qcow2 \
		--backing-vol "$(base_volume)" --backing-vol-format qcow2 >/dev/null
	upload_volume "$seed" "$(seed_image "$vm")"
	# host-passthrough: LXD runs virtual machines inside, with nested KVM
	virt-install --connect qemu:///system --name "$domain" \
		--memory $(($(vm_size "$vm" ram) * 1024)) --vcpus "$(vm_size "$vm" vcpus)" \
		--cpu host-passthrough --osinfo "$(vm_osinfo)" \
		--disk "vol=$LAB_POOL/$disk,bus=virtio" \
		--disk "vol=$LAB_POOL/$seed,device=cdrom" \
		--network "network=$LAB_NETWORK,mac=$(vm_mac "$vm"),model=virtio" \
		--import --graphics none --console pty,target_type=serial --noautoconsole >/dev/null
}

# resize_vm VM: brings an existing VM to the configured vCPUs and RAM. The
# sizes go into libvirt's configuration and apply from the next boot, so a
# running VM is shut down (destroyed if it does not stop in 5 minutes) and
# started again. A snapshot keeps the domain definition it was taken with,
# so a resize marks the baseline as changed and up takes a new snapshot.
resize_vm() {
	local vm="$1" domain kib vcpus want_kib want_vcpus deadline
	domain="$(vm_domain "$vm")"
	want_kib=$(($(vm_size "$vm" ram) * 1024 * 1024))
	want_vcpus="$(vm_size "$vm" vcpus)"
	kib="$(vsh dominfo "$domain" | awk '/^Max memory:/ { print $3 }')"
	vcpus="$(vsh vcpucount "$domain" --maximum --config)"
	((kib == want_kib && vcpus == want_vcpus)) && return
	info "resizing VM $vm to $want_vcpus vCPUs and $(vm_size "$vm" ram) GiB (it restarts)"
	vsh setmaxmem "$domain" "${want_kib}k" --config >/dev/null
	vsh setmem "$domain" "${want_kib}k" --config >/dev/null
	vsh setvcpus "$domain" "$want_vcpus" --maximum --config >/dev/null
	vsh setvcpus "$domain" "$want_vcpus" --config >/dev/null
	if [[ "$(vm_state "$vm")" == running ]]; then
		vsh shutdown "$domain" >/dev/null
		deadline=$((SECONDS + 300))
		until [[ "$(vm_state "$vm")" == "shut off" ]]; do
			if ((SECONDS >= deadline)); then
				vsh destroy "$domain" >/dev/null 2>&1 || true
				break
			fi
			sleep 3
		done
	fi
	vsh start "$domain" >/dev/null
	LAB_BASELINE_CHANGED=1
}

# wait_for_vm VM: waits until VM answers over SSH and cloud-init is done.
wait_for_vm() {
	local vm="$1" deadline=$((SECONDS + 900)) status
	until vm_ssh "$vm" true 2>/dev/null; do
		((SECONDS < deadline)) || die "$vm does not answer over SSH at $(vm_ip "$vm") (see: virsh console $(vm_domain "$vm"))"
		sleep 5
	done
	status=0
	vm_ssh "$vm" sudo cloud-init status --wait >/dev/null 2>&1 || status=$?
	case "$status" in
	0) info "$vm is up" ;;
	2) info "$vm is up (cloud-init reported recoverable errors: lab.sh logs $vm)" ;;
	*) die "cloud-init failed on $vm (lab.sh logs $vm)" ;;
	esac
}

# --- Teardown -----------------------------------------------------------------

# destroy_all: removes every VM the lab can have, whatever the scenario, then
# the pool and the network.
destroy_all() {
	local vm domain volume
	for vm in "${LAB_ALL_VMS[@]}"; do
		domain="$(vm_domain "$vm")"
		vsh dominfo "$domain" >/dev/null 2>&1 || continue
		info "destroying VM $vm"
		vsh destroy "$domain" >/dev/null 2>&1 || true
		vsh undefine "$domain" --remove-all-storage --snapshots-metadata >/dev/null 2>&1 ||
			vsh undefine "$domain" --snapshots-metadata >/dev/null
	done
	if vsh pool-info "$LAB_POOL" >/dev/null 2>&1; then
		info "destroying storage pool $LAB_POOL"
		vsh pool-start "$LAB_POOL" >/dev/null 2>&1 || true
		# plain rows (vsh is quiet), the name first: "vol-list --name" is
		# missing from older virsh
		while read -r volume _; do
			[[ -n "$volume" ]] && vsh vol-delete --pool "$LAB_POOL" "$volume" >/dev/null
		done < <(vsh vol-list "$LAB_POOL")
		vsh pool-destroy "$LAB_POOL" >/dev/null 2>&1 || true
		vsh pool-delete "$LAB_POOL" >/dev/null 2>&1 ||
			info "warning: $LAB_POOL_DIR could not be removed: remove it by hand (sudo rm -r $LAB_POOL_DIR)"
		vsh pool-undefine "$LAB_POOL" >/dev/null
	fi
	if vsh net-info "$LAB_NETWORK" >/dev/null 2>&1; then
		info "destroying network $LAB_NETWORK"
		vsh net-destroy "$LAB_NETWORK" >/dev/null 2>&1 || true
		vsh net-undefine "$LAB_NETWORK" >/dev/null
	fi
}
