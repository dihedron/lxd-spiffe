#!/usr/bin/env bash
# The lxd-spiffe lab: real LXD servers on libvirt VMs, for the lxd-probe gate
# runbooks and the plugins' end-to-end tests. See
# .specs/lxd-test-environment.md and "lab.sh help".
set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# set when up changes the baseline (instances added, VMs resized): up then takes a new snapshot
LAB_BASELINE_CHANGED=0
REPO_DIR="$(cd "$LAB_DIR/../.." && pwd)"

# shellcheck source=lib/common.sh
source "$LAB_DIR/lib/common.sh"
# shellcheck source=lib/preflight.sh
source "$LAB_DIR/lib/preflight.sh"
# shellcheck source=lib/vm.sh
source "$LAB_DIR/lib/vm.sh"
# shellcheck source=lib/lxd.sh
source "$LAB_DIR/lib/lxd.sh"
# shellcheck source=lib/pki.sh
source "$LAB_DIR/lib/pki.sh"
# shellcheck source=lib/auth.sh
source "$LAB_DIR/lib/auth.sh"
# shellcheck source=lib/instances.sh
source "$LAB_DIR/lib/instances.sh"
# shellcheck source=lib/snapshot.sh
source "$LAB_DIR/lib/snapshot.sh"
# shellcheck source=lib/deploy.sh
source "$LAB_DIR/lib/deploy.sh"
# shellcheck source=lib/run.sh
source "$LAB_DIR/lib/run.sh"

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
  snapshot                  save the lab's disks as its baseline (the VMs are
                            shut down for it)
  reset                     return the lab to its baseline (cold boot)
  deploy                    build and install what the scenarios need; after
                            reset, the inner loop after a code change
  probe <server> [--member <vm>] [--as <identity>] -- <args>
                            run lxd-probe on this host against a server (a
                            cluster through its first member, or --member),
                            as an identity: read, files, edit, full
                            (default), outsider, or bearer where the server
                            has one
  guest <server> <instance> [--project <project>] [--user <uid>] -- <args>
                            run lxd-probe inside an instance (as root
                            unless --user is given)
  token <server> [--expiry <durations>]
                            issue a short-lived token (default 2M) to the
                            bearer identity lab-bearer-short, for
                            probe --as bearer-short (L13)
  pull <server> <instance> [--project <project>] [--user <uid>] <run_id>
                            copy a guest run into the evidence directory
  refresh                   move the LXD servers to their channels' newest
                            revision, then offer a new snapshot
  test [-run REGEX] [--collect] [--record-host]
                            run the acceptance tests: the lab checks, then
                            the gate sessions (--collect: copy the sessions'
                            evidence and fixtures into the repository;
                            --record-host: record this machine's host name,
                            redacted otherwise)
  help                      show this help

preflight, up, deploy, reset and test take --scenario probe|plugins|all
(default: LAB_SCENARIOS, now "$LAB_SCENARIOS"). LAB_CLUSTER (now
"$LAB_CLUSTER": no, 5.21, 6.9 or both) chooses the running clusters for every
command.

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
	ensure_pki

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
	shutdown_unchosen_clusters

	section "LXD"
	for server in $(lab_servers); do
		lxd_install "$server"
		lxd_allow_idmap "$server"
		lxd_init "$server"
		lxd_check "$server"
		auth_setup "$server"
	done

	section "Instances"
	for server in $(lab_servers); do
		is_cluster "$server" || ensure_restricted_project "$server"
		ensure_instances "$server"
	done
	for server in $(lab_servers); do
		start_instances "$server"
		wait_instances "$server"
		ensure_selinux "$server"
		baseline_grants "$server"
	done

	# a snapshot on the first success, and a new one when this up added
	# baseline instances or resized VMs: reset would otherwise undo them
	if ! all_snapshots_exist || ((LAB_BASELINE_CHANGED)); then
		section "Snapshot"
		LAB_ASSUME_YES=1 lab_snapshot
	fi

	cmd_deploy

	section "Done"
	info "the lab is up: lab.sh status, lab.sh ssh <vm>, lab.sh reset"
}

# shutdown_unchosen_clusters: shuts down the members of every built cluster
# LAB_CLUSTER does not choose; they keep their disks and snapshot.
shutdown_unchosen_clusters() {
	local cluster member
	for cluster in "${LAB_ALL_CLUSTERS[@]}"; do
		lab_clusters | grep -qx "$cluster" && continue
		for member in $(cluster_members "$cluster"); do
			[[ "$(vm_state "$member")" == running ]] || continue
			info "$cluster is not chosen (LAB_CLUSTER=$LAB_CLUSTER): shutting $member down"
			vm_shutdown "$member"
		done
	done
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
		"\textensions \(.api_extensions_hash // "?")",
		"\tauthorization \(.auth.model // "?"), bearer identities: \(.auth.bearer // "?")",
		(select(.cluster == "yes") | "\tcluster members \(.members // {} | keys | join(" "))"),
		(.instances // {} | to_entries[] | "\t\(.key)\t\(.value.type)\t\(.value.image)\(if .value.location and .value.location != "none" then " on \(.value.location)" else "" end)")' |
		while IFS= read -r line; do info "$line"; done
	section "Snapshot"
	if all_snapshots_exist; then
		info "taken $(env_get '.snapshot.taken')"
	else
		info "none (lab.sh snapshot)"
	fi
	section "Deployed"
	info "lxd-probe $(env_get '.probe.version // "none (lab.sh deploy)"')"
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

# cmd_deploy: installs what the selected scenarios need.
cmd_deploy() {
	require_state
	section "Deploy"
	if scenario_selected probe; then
		deploy_probe
	fi
}

# cmd_refresh: moves the LXD servers whose revision the settings do not pin
# to the newest revision of their channels, after confirmation, then offers a
# new snapshot.
cmd_refresh() {
	require_state
	select_scenarios "$(env_get '.scenarios | join(" ")')"
	section "Refresh"
	local server plans=() plan current new version
	for server in $(lab_servers); do
		plan="$(lxd_refresh_plan "$server")"
		[[ -z "$plan" ]] || plans+=("$plan")
	done
	if ((${#plans[@]} == 0)); then
		info "nothing to refresh"
		return
	fi
	for plan in "${plans[@]}"; do
		read -r server current new version <<<"$plan"
		info "$server: revision $current -> $new ($version)"
	done
	confirm "Refresh these servers? LXD cannot go back to an older revision afterwards." || {
		info "nothing refreshed"
		return 1
	}
	for plan in "${plans[@]}"; do
		read -r server current new version <<<"$plan"
		lxd_refresh "$server" "$new"
	done
	if confirm "Replace the lab's snapshot with the refreshed lab?"; then
		section "Snapshot"
		LAB_ASSUME_YES=1 lab_snapshot
	else
		info "snapshot kept: lab.sh reset returns to the old revisions, which env.json no longer records"
	fi
}

# cmd_test [-run REGEX] [--collect] [--record-host]: runs the acceptance
# tests of the selected scenarios (test/lab/acceptance, build tag "lab").
cmd_test() {
	require_state
	local usage="usage: lab.sh test [-run REGEX] [--collect] [--record-host]"
	local run="" collect=no
	while (($#)); do
		case "$1" in
		-run | --run)
			[[ $# -ge 2 ]] || die "$usage"
			run="$2"
			shift 2
			;;
		--collect)
			collect=yes
			shift
			;;
		--record-host)
			export LXD_PROBE_RECORD_HOST=true
			shift
			;;
		*) die "$usage" ;;
		esac
	done
	scenario_selected probe || die "no tests for the selected scenarios yet"
	[[ -n "$(env_get '.probe.sha256 // empty')" ]] || die "lxd-probe is not deployed: run lab.sh deploy"
	export LAB_DIR REPO_DIR LAB_STATE_DIR LAB_EVIDENCE_DIR LAB_GUEST_VM_VCPUS LAB_GUEST_VM_RAM_GIB LAB_CLUSTER
	export LAB_COLLECT="$collect"
	local args=(-tags lab -count=1 -v -timeout 60m)
	[[ -z "$run" ]] || args+=(-run "$run")
	(cd "$REPO_DIR" && go test "${args[@]}" ./test/lab/acceptance)
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
	snapshot | reset)
		# the whole lab: every scenario built so far
		require_state
		select_scenarios "$(env_get '.scenarios | join(" ")')"
		"lab_$command"
		;;
	deploy) cmd_deploy ;;
	probe) cmd_probe "${args[@]}" ;;
	guest) cmd_guest "${args[@]}" ;;
	pull) cmd_pull "${args[@]}" ;;
	token) cmd_token "${args[@]}" ;;
	refresh) cmd_refresh ;;
	test) cmd_test "${args[@]}" ;;
	help | -h | --help) usage ;;
	*)
		usage >&2
		exit 2
		;;
	esac
}

main "$@"
