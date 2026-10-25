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
	# relative locations are relative to the lab directory, the evidence's to
	# the repository
	[[ "$LAB_STATE_DIR" = /* ]] || LAB_STATE_DIR="$LAB_DIR/$LAB_STATE_DIR"
	[[ "$LAB_EVIDENCE_DIR" = /* ]] || LAB_EVIDENCE_DIR="$REPO_DIR/$LAB_EVIDENCE_DIR"
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
# plugins or all) or, when VALUE is empty, from LAB_SCENARIOS.
select_scenarios() {
	local value="${1:-$LAB_SCENARIOS}" scenario
	[[ "$value" == all ]] && value="probe plugins"
	LAB_SELECTED=()
	for scenario in $value; do
		case "$scenario" in
		probe | plugins) ;;
		*) die "unknown scenario \"$scenario\": probe, plugins or all" ;;
		esac
		[[ " ${LAB_SELECTED[*]} " == *" $scenario "* ]] || LAB_SELECTED+=("$scenario")
	done
	((${#LAB_SELECTED[@]} > 0)) || die "no scenario selected: set LAB_SCENARIOS or --scenario"
	case "$LAB_CLUSTER" in
	no | "" | 5.21 | 6 | both) ;;
	6.9) die "LAB_CLUSTER=6.9 is now LAB_CLUSTER=6 (c6 follows lxd6's LXD 6.x revision)" ;;
	*) die "LAB_CLUSTER must be no, 5.21, 6 or both, not \"$LAB_CLUSTER\"" ;;
	esac
}

# scenario_selected NAME: whether NAME is in LAB_SELECTED.
scenario_selected() { [[ " ${LAB_SELECTED[*]} " == *" $1 "* ]]; }

# Every cluster the lab can have: c5 (LXD 5.21 LTS) and c6 (6.x).
readonly LAB_ALL_CLUSTERS=(c5 c6)

# lab_clusters: the clusters LAB_CLUSTER chooses, one per line.
lab_clusters() {
	case "$LAB_CLUSTER" in
	5.21) echo c5 ;;
	6) echo c6 ;;
	both) printf '%s\n' c5 c6 ;;
	esac
}

# is_cluster NAME: whether NAME is one of the lab's clusters.
is_cluster() { [[ " ${LAB_ALL_CLUSTERS[*]} " == *" $1 "* ]]; }

# cluster_members CLUSTER: its member VMs, the first one bootstraps it.
cluster_members() { printf '%s\n' "${1}n1" "${1}n2" "${1}n3"; }

# cluster_sibling CLUSTER: the standalone server whose channel and revision
# the cluster runs.
cluster_sibling() {
	case "$1" in
	c5) echo lxd5 ;;
	c6) echo lxd6 ;;
	esac
}

# server_vms SERVER: the VMs of a server: its own, or a cluster's members.
server_vms() {
	if is_cluster "$1"; then
		cluster_members "$1"
	else
		echo "$1"
	fi
}

# vm_server VM: the server VM belongs to (a cluster for its members), or spire.
vm_server() {
	case "$1" in
	c[56]n[1-3]) echo "${1%n?}" ;;
	*) echo "$1" ;;
	esac
}

# The one server (a cluster: its members) or spire the command acts on, set
# by --only (select_only); empty: the whole lab.
LAB_ONLY=""

# select_only NAME: sets LAB_ONLY, once the scenarios are selected; NAME must
# be one of the VMs or servers the command would otherwise act on.
select_only() {
	[[ -n "$1" ]] || return 0
	case "$1" in
	lxd5 | lxd6 | c5 | c6 | spire) ;;
	*) die "--only takes lxd5, lxd6, c5, c6 or spire, not \"$1\"" ;;
	esac
	lab_vms | grep -qx "$(server_vms "$1" | head -n1)" ||
		die "--only $1: not in the lab this command acts on (LAB_CLUSTER=$LAB_CLUSTER, scenarios ${LAB_SELECTED[*]})"
	LAB_ONLY="$1"
}

# lab_vms: the VMs of the selected scenarios and topology, one per line;
# with --only, its VMs alone.
lab_vms() {
	local cluster
	if [[ -n "$LAB_ONLY" ]]; then
		server_vms "$LAB_ONLY"
		return
	fi
	echo lxd5
	echo lxd6
	for cluster in $(lab_clusters); do
		cluster_members "$cluster"
	done
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
	c5n[1-3] | c6n[1-3]) kind=NODE ;;
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

# Every VM the lab can have, in any scenario and topology: the network knows
# them all, and "down" removes them all.
readonly LAB_ALL_VMS=(lxd5 lxd6 c5n1 c5n2 c5n3 c6n1 c6n2 c6n3 spire)

# vm_host VM: the host part of the VM's address; a cluster's is its first
# member's, so that what reaches a server by name reaches a cluster through
# that member.
vm_host() {
	case "$1" in
	lxd5) echo "$LAB_LXD5_HOST" ;;
	lxd6) echo "$LAB_LXD6_HOST" ;;
	c5n1) echo "$LAB_C5N1_HOST" ;;
	c5n2) echo "$LAB_C5N2_HOST" ;;
	c5n3) echo "$LAB_C5N3_HOST" ;;
	c6n1) echo "$LAB_C6N1_HOST" ;;
	c6n2) echo "$LAB_C6N2_HOST" ;;
	c6n3) echo "$LAB_C6N3_HOST" ;;
	c5 | c6) vm_host "${1}n1" ;;
	spire) echo "$LAB_SPIRE_HOST" ;;
	*) die "unknown VM $1" ;;
	esac
}

vm_ip() { lab_ip "$(vm_host "$1")"; }

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

# published_sha256 URL: the SHA-256 of URL from the SHA256SUMS published next
# to it.
published_sha256() {
	local dir="${1%/*}" file="${1##*/}"
	curl -fsSL --proto '=https' "$dir/SHA256SUMS" 2>/dev/null | awk -v f="$file" '
		$2 == f || $2 == "*" f { print $1; found = 1; exit }
		END { exit !found }'
}

# fetch URL SHA256: downloads URL into the cache unless already there with
# that checksum, and prints the cached path. Fails on a checksum mismatch.
fetch() {
	local url="$1" sha="$2" path
	path="$LAB_CACHE_DIR/downloads/${url##*/}"
	mkdir -p "${path%/*}"
	if [[ -f "$path" ]] && echo "$sha  $path" | sha256sum -c --quiet >/dev/null 2>&1; then
		echo "$path"
		return
	fi
	info "downloading $url" >&2
	curl -fL --proto '=https' --progress-bar -o "$path.part" "$url" >&2 || die "cannot download $url"
	echo "$sha  $path.part" | sha256sum -c --quiet >/dev/null 2>&1 || {
		rm -f "$path.part"
		die "checksum mismatch for $url (expected $sha)"
	}
	mv "$path.part" "$path"
	echo "$path"
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

# env_del PATH: removes a value from env.json, if there.
env_del() {
	local tmp
	tmp="$(mktemp "$LAB_STATE_DIR/env.XXXXXX")"
	jq "del($1)" "$(env_file)" >"$tmp" && mv "$tmp" "$(env_file)"
}
