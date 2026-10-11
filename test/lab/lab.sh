#!/usr/bin/env bash
# The lxd-spiffe lab: real LXD servers on libvirt VMs, for the lxd-probe gate
# runbooks and the plugins' end-to-end tests. See
# .specs/lxd-test-environment.md and "lab.sh help".
set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$LAB_DIR/../.." && pwd)"

# shellcheck source=lib/common.sh
source "$LAB_DIR/lib/common.sh"
# shellcheck source=lib/preflight.sh
source "$LAB_DIR/lib/preflight.sh"
# shellcheck source=lib/vm.sh
source "$LAB_DIR/lib/vm.sh"
# shellcheck source=lib/lxd.sh
source "$LAB_DIR/lib/lxd.sh"

usage() {
	cat <<USAGE
usage: lab.sh <command> [options]

commands:
  preflight [--no-install]  check that this machine can run the lab, and
                            install the missing software after confirmation
                            (--no-install: only report)
  up                        build the lab (or complete a partial one)
  down                      destroy the lab: VMs, network, storage, state
  status                    show the VMs, the LXD servers and the instances
  ssh <vm> [command]        open a shell, or run a command, on a VM
  logs <vm> [unit]          show a VM's cloud-init log, or a unit's journal
  snapshot                  save the running lab as its baseline
  reset                     return the lab to its baseline, in about a minute
  deploy                    build and install what the scenarios need
  probe <server> [--as <identity>] -- <args>
                            run lxd-probe on this host against a server, as
                            an identity (default full)
  guest <server> <instance> [--user <user>] -- <args>
                            run lxd-probe inside an instance
  refresh                   move the LXD servers to their channels' newest
                            revision
  test [-run REGEX]         run the acceptance tests
  help                      show this help

preflight, up, deploy, reset and test take --scenario probe|plugins|all
(default: LAB_SCENARIOS, now "$LAB_SCENARIOS").

Settings are in lab.env, overridden by lab.local.env and by the environment.
USAGE
}

require_state() {
	[[ -f "$(env_file)" ]] || die "no lab here: run lab.sh up first"
}

# require_vm VM: dies unless VM is one of the lab's and exists.
require_vm() {
	[[ " ${LAB_ALL_VMS[*]} " == *" $1 "* ]] || die "unknown VM \"$1\"; VMs: ${LAB_ALL_VMS[*]}"
	[[ "$(vm_state "$1")" != absent ]] || die "$1 does not exist: lab.sh up (with its scenario)"
}

# resolve_state: creates env.json with the VMs' image, unless a previous
# (possibly partial) "up" did; "down" forgets it. The scenarios built so far
# accumulate.
resolve_state() {
	if [[ ! -f "$(env_file)" ]]; then
		section "Versions"
		local url sha
		url="$(ubuntu_image_url)"
		sha="${LAB_UBUNTU_IMAGE_SHA256:-$(published_sha256 "$url")}" ||
			die "no published checksum for $url: set LAB_UBUNTU_IMAGE_SHA256"
		jq -n \
			--arg release "$LAB_UBUNTU_RELEASE" --arg url "$url" --arg sha "$sha" \
			--arg network "$LAB_NETWORK" --arg subnet "$LAB_SUBNET" \
			--arg user "$LAB_USER" --arg key "$(ssh_key)" \
			'{
				scenarios: [],
				image: {distro: "ubuntu", release: $release, url: $url, sha256: $sha},
				network: {name: $network, subnet: $subnet},
				ssh: {user: $user, key: $key},
				vms: {},
				servers: {}
			}' >"$(env_file)"
		info "VMs: Ubuntu $LAB_UBUNTU_RELEASE (${sha:0:12})"
	else
		info "reusing the versions resolved at the first up (lab.sh status)"
	fi
	local tmp vm server
	tmp="$(mktemp "$LAB_STATE_DIR/env.XXXXXX")"
	jq --args '.scenarios = (.scenarios + $ARGS.positional | unique)' "${LAB_SELECTED[@]}" <"$(env_file)" >"$tmp" &&
		mv "$tmp" "$(env_file)"
	for vm in $(lab_vms); do
		env_set ".vms[\"$vm\"].ip" "$(vm_ip "$vm")"
		env_set ".vms[\"$vm\"].domain" "$(vm_domain "$vm")"
	done
	for server in $(lab_servers); do
		lxd_resolve "$server"
	done
}

cmd_up() {
	run_preflight || die "the preflight failed: fix the failures above and run lab.sh up again"
	mkdir -p "$LAB_STATE_DIR"
	chmod 0700 "$LAB_STATE_DIR"
	ensure_ssh_key
	resolve_state

	section "VMs"
	ensure_pool
	ensure_base_image
	ensure_network
	local vm server
	for vm in $(lab_vms); do
		ensure_vm "$vm"
	done
	for vm in $(lab_vms); do
		wait_for_vm "$vm"
	done

	section "LXD"
	for server in $(lab_servers); do
		lxd_install "$server"
		lxd_init "$server"
		lxd_check "$server"
	done

	section "Done"
	info "the LXD servers are up: lab.sh status, lab.sh ssh <vm>"
}

cmd_down() {
	if ! confirm "Destroy the lab (its VMs, network $LAB_NETWORK, storage pool $LAB_POOL and $LAB_STATE_DIR)?"; then
		info "nothing destroyed"
		return 1
	fi
	destroy_all
	rm -rf "$LAB_STATE_DIR"
	info "the lab is gone (the download cache in $LAB_CACHE_DIR stays)"
}

cmd_status() {
	require_state
	section "Lab"
	info "scenarios: $(env_get '.scenarios | join(" ")')"
	info "VMs:       $(env_get '.image | "\(.distro) \(.release) (\(.sha256[0:12]))"') on $(env_get '.network | "\(.name) \(.subnet)"')"
	section "VMs"
	local vm
	for vm in "${LAB_ALL_VMS[@]}"; do
		[[ "$(vm_state "$vm")" != absent || -n "$(env_get ".vms[\"$vm\"] // empty")" ]] || continue
		printf '        %-6s %-13s %s\n' "$vm" "$(vm_ip "$vm")" "$(vm_state "$vm")"
	done
	section "LXD servers"
	# shellcheck disable=SC2016 # a jq program
	env_get '.servers | to_entries[] | .key as $s | .value |
		"\($s)\tLXD \(.version // "?") (\(.channel) rev \(.revision), held)\t\(.endpoint)",
		"\tstorage \(.storage // "?"), lxdbr0 \(.bridge // "?")",
		"\tpin \(.fingerprint // "?")",
		"\textensions \(.api_extensions_hash // "?")"' |
		while IFS= read -r line; do info "$line"; done
}

cmd_ssh() {
	require_state
	local vm="${1:-}"
	[[ -n "$vm" ]] || die "usage: lab.sh ssh <vm> [command]"
	require_vm "$vm"
	shift
	exec ssh -t "${SSH_OPTS[@]}" "$LAB_USER@$(vm_ip "$vm")" "$@"
}

cmd_logs() {
	require_state
	local vm="${1:-}" unit="${2:-}"
	[[ -n "$vm" ]] || die "usage: lab.sh logs <vm> [unit]"
	require_vm "$vm"
	if [[ -n "$unit" ]]; then
		vm_ssh "$vm" "sudo journalctl --no-pager -n 300 -u $(printf '%q' "$unit")"
	else
		vm_ssh "$vm" "sudo tail -n 300 /var/log/cloud-init-output.log"
	fi
}

# not_yet COMMAND STEP: the commands of later steps of chunk 4.5.
not_yet() {
	die "lab.sh $1 is not implemented yet (chunk 4.5, step $2)"
}

main() {
	local command="${1:-help}"
	shift || true
	load_settings
	load_ssh

	# --scenario applies to the commands that build or use a scenario
	local scenario="" args=()
	while (($#)); do
		case "$1" in
		--scenario)
			[[ $# -ge 2 ]] || die "--scenario needs a value: probe, plugins or all"
			scenario="$2"
			shift 2
			;;
		--scenario=*)
			scenario="${1#--scenario=}"
			shift
			;;
		--)
			args+=("$@")
			break
			;;
		*)
			args+=("$1")
			shift
			;;
		esac
	done
	case "$command" in
	preflight | up | deploy | reset | test) select_scenarios "$scenario" ;;
	*) [[ -z "$scenario" ]] || die "lab.sh $command does not take --scenario" ;;
	esac

	case "$command" in
	preflight) run_preflight "${args[@]}" ;;
	up) cmd_up "${args[@]}" ;;
	down) cmd_down ;;
	status) cmd_status ;;
	ssh) cmd_ssh "${args[@]}" ;;
	logs) cmd_logs "${args[@]}" ;;
	snapshot | reset) not_yet "$command" 3 ;;
	deploy | probe | guest | refresh) not_yet "$command" 4 ;;
	test) not_yet "$command" 5 ;;
	help | -h | --help) usage ;;
	*)
		usage >&2
		exit 2
		;;
	esac
}

main "$@"
