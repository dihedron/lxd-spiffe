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

# not_yet COMMAND STEP: the commands of later steps of chunk 4.5.
not_yet() {
	die "lab.sh $1 is not implemented yet (chunk 4.5, step $2)"
}

main() {
	local command="${1:-help}"
	shift || true
	load_settings

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
	up | down | status | ssh | logs) not_yet "$command" 2 ;;
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
