# shellcheck shell=bash
# Common functions of the lab: settings, scenarios, output, and small
# helpers. Sourced by lab.sh, which sets LAB_DIR (test/lab) and REPO_DIR (the
# repository).

# load_settings reads lab.env, then lab.local.env if present, then gives
# precedence to the LAB_* variables set in the environment.
load_settings() {
	# only what the environment sets: exported variables
	local name pair saved=()
	for name in $(compgen -A export LAB_); do
		saved+=("$name=${!name}")
	done
	# shellcheck disable=SC1091 # lab.env: settings, checked on its own
	source "$LAB_DIR/lab.env"
	if [[ -f "$LAB_DIR/lab.local.env" ]]; then
		# shellcheck disable=SC1091
		source "$LAB_DIR/lab.local.env"
	fi
	for pair in "${saved[@]}"; do
		printf -v "${pair%%=*}" '%s' "${pair#*=}"
	done
	# relative locations are relative to the lab directory
	[[ "$LAB_STATE_DIR" = /* ]] || LAB_STATE_DIR="$LAB_DIR/$LAB_STATE_DIR"
	export PATH="$LAB_TOOLS_DIR/bin:$LAB_TOOLS_DIR/go/bin:$PATH"
}

# --- Output -------------------------------------------------------------------

if [[ -t 1 ]]; then
	_green=$'\e[32m' _yellow=$'\e[33m' _red=$'\e[31m' _bold=$'\e[1m' _reset=$'\e[0m'
else
	_green='' _yellow='' _red='' _bold='' _reset=''
fi

section() { printf '\n%s%s%s\n' "$_bold" "$*" "$_reset"; }
info() { printf '        %s\n' "$*"; }
die() {
	printf '%serror:%s %s\n' "$_red" "$_reset" "$*" >&2
	exit 1
}

# --- Scenarios and VMs --------------------------------------------------------

# The scenarios this command works on, set by select_scenarios.
LAB_SELECTED=()

# select_scenarios VALUE: sets LAB_SELECTED from a --scenario value (probe,
# plugins or all) or, when VALUE is empty, from LAB_SCENARIOS. Refuses what is
# not built yet.
select_scenarios() {
	local value="${1:-$LAB_SCENARIOS}" scenario
	[[ "$value" == all ]] && value="probe plugins"
	LAB_SELECTED=()
	for scenario in $value; do
		case "$scenario" in
		probe) ;;
		plugins) die "the plugins scenario is not built yet: it comes with the plugins' end-to-end step" ;;
		*) die "unknown scenario \"$scenario\": probe, plugins or all" ;;
		esac
		[[ " ${LAB_SELECTED[*]} " == *" $scenario "* ]] || LAB_SELECTED+=("$scenario")
	done
	((${#LAB_SELECTED[@]} > 0)) || die "no scenario selected: set LAB_SCENARIOS or --scenario"
	case "$LAB_CLUSTER" in
	no | "") ;;
	yes) die "LAB_CLUSTER=yes is not built yet: the cluster comes with gate L11" ;;
	*) die "LAB_CLUSTER must be yes or no, not \"$LAB_CLUSTER\"" ;;
	esac
}

# scenario_selected NAME: whether NAME is in LAB_SELECTED.
scenario_selected() { [[ " ${LAB_SELECTED[*]} " == *" $1 "* ]]; }

# lab_vms: the VMs of the selected scenarios and topology, one per line.
lab_vms() {
	echo lxd5
	echo lxd6
	if [[ "$LAB_CLUSTER" == yes ]]; then
		echo node1
		echo node2
		echo node3
	fi
	if scenario_selected plugins; then
		echo spire
	fi
}

# vm_size VM vcpus|ram|disk: a VM's size from the settings (RAM and disk in
# GiB).
vm_size() {
	local kind
	case "$1" in
	lxd5 | lxd6) kind=LXD ;;
	node*) kind=NODE ;;
	spire) kind=SPIRE ;;
	*) die "unknown VM $1" ;;
	esac
	case "$2" in
	vcpus) eval "echo \"\$LAB_${kind}_VCPUS\"" ;;
	ram) eval "echo \"\$LAB_${kind}_RAM_GIB\"" ;;
	disk) eval "echo \"\$LAB_${kind}_DISK_GIB\"" ;;
	esac
}

# vm_domain VM: the VM's libvirt domain.
vm_domain() { echo "$LAB_NETWORK-$1"; }

# --- Helpers ------------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

# version_ge A B: whether version A is at least B (dotted, optional "v"/"go").
version_ge() {
	local a="${1#v}" b="${2#v}"
	a="${a#go}" b="${b#go}"
	[[ "$(printf '%s\n%s\n' "$b" "$a" | sort -V | head -n1)" == "$b" ]]
}

# ip_to_int A.B.C.D
ip_to_int() {
	local IFS=. a b c d
	read -r a b c d <<<"$1"
	echo $(((a << 24) + (b << 16) + (c << 8) + d))
}

# cidr_range A.B.C.D/N: prints the first and last address as integers.
cidr_range() {
	local addr="${1%/*}" bits="${1#*/}"
	[[ "$1" == */* ]] || bits=32
	local start mask
	start=$(ip_to_int "$addr")
	mask=$(((0xFFFFFFFF << (32 - bits)) & 0xFFFFFFFF))
	start=$((start & mask))
	echo "$start $((start | (~mask & 0xFFFFFFFF)))"
}

# cidr_overlap X/N Y/M: whether the two ranges share an address.
cidr_overlap() {
	local a1 a2 b1 b2
	read -r a1 a2 <<<"$(cidr_range "$1")"
	read -r b1 b2 <<<"$(cidr_range "$2")"
	((a1 <= b2 && b1 <= a2))
}

# netmask_bits 255.255.255.0
netmask_bits() {
	local n bits=0
	n=$(ip_to_int "$1")
	while ((n & 0x80000000)); do
		bits=$((bits + 1))
		n=$(((n << 1) & 0xFFFFFFFF))
	done
	echo "$bits"
}

# lab_ip HOST_PART: the address of a host in LAB_SUBNET.
lab_ip() {
	local base="${LAB_SUBNET%/*}"
	echo "${base%.*}.$1"
}

# ubuntu_image_url: the VMs' cloud image, unless overridden.
ubuntu_image_url() {
	if [[ -n "$LAB_UBUNTU_IMAGE_URL" ]]; then
		echo "$LAB_UBUNTU_IMAGE_URL"
		return
	fi
	local codename
	case "$LAB_UBUNTU_RELEASE" in
	22.04) codename=jammy ;;
	24.04) codename=noble ;;
	26.04) codename=resolute ;;
	*) die "no known image for Ubuntu $LAB_UBUNTU_RELEASE: set LAB_UBUNTU_IMAGE_URL" ;;
	esac
	echo "https://cloud-images.ubuntu.com/$codename/current/$codename-server-cloudimg-amd64.img"
}

# go_mod_version: the Go version go.mod requires.
go_mod_version() {
	awk '$1 == "go" { print $2; exit }' "$REPO_DIR/go.mod"
}

# --- Lab state ----------------------------------------------------------------

env_file() { echo "$LAB_STATE_DIR/env.json"; }

# env_get FILTER: a value from env.json.
env_get() { jq -r "$1" "$(env_file)"; }

# env_set PATH VALUE: sets a string in env.json (PATH as in jq, e.g. .a.b).
env_set() {
	local tmp
	tmp="$(mktemp "$LAB_STATE_DIR/env.XXXXXX")"
	jq --arg v "$2" "$1 = \$v" "$(env_file)" >"$tmp" && mv "$tmp" "$(env_file)"
}
