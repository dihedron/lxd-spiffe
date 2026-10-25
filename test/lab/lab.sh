#!/usr/bin/env bash
# The lxd-spiffe lab: real LXD servers on libvirt VMs, for the lxd-probe gate
# runbooks and the plugins' end-to-end tests. See
# .specs/lxd-test-environment.md and "lab.sh help".
set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# the servers (clusters, spire) whose baseline up changed (instances added, VMs resized), space separated: up then snapshots them anew
LAB_BASELINE_CHANGED=""
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
# shellcheck source=lib/spire.sh
source "$LAB_DIR/lib/spire.sh"

usage() {
	cat <<USAGE
usage: lab.sh <command> [options]

commands:
  preflight [--no-install]  check that this machine can run the lab, and
                            install the missing software after confirmation
                            (--no-install: only report)
  up                        build the lab (or complete a partial one)
  down [--only <server>]    destroy the lab: VMs, network, storage, state
                            (--only lxd5|lxd6|c5|c6: that server's VMs and
                            snapshots alone; up builds it again)
  status                    show the VMs, the LXD servers and the instances
  ssh <vm> [command]        open a shell, or run a command, on a VM
  logs <vm> [unit]          show a VM's cloud-init log, or a unit's journal
  snapshot [--only <name>]  save the lab's disks as its baseline (the VMs are
                            shut down for it)
  reset [--only <name>]     return the lab to its baseline (cold boot);
                            --only lxd5|lxd6|c5|c6|spire: that server's (a
                            cluster's three members) or spire's VMs alone
  stop                      stop the instances and shut every VM down
                            cleanly; disks, snapshot and state stay
  start                     boot the VMs as they were left (no revert, no
                            deploy) and start the instances
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
  refresh [--only <server>] move the LXD servers to their channels' newest
                            revision, and the chosen clusters to their
                            siblings', then offer a new snapshot (--only
                            lxd5|lxd6|c5|c6: that server alone; a cluster
                            to its sibling's recorded revision)
  guest-up <server> <name> [--vm] [--privileged] [--no-files] [--target <member>]
           [--project <project>] [--claim-name <name>] [--agent-data-tmpfs]
                            (plugins) launch an Ubuntu guest with SPIRE Agent
                            and lxd-agent-plugin, lab-spire granted on it in
                            project default (--no-files: without
                            can_access_files; --claim-name: the agent claims
                            another instance's name; --agent-data-tmpfs: the
                            agent's data in /run, tmpfs mode)
  guest-down <server> <name> [--project <project>]
                            (plugins) delete a guest and lab-spire's grants
  guest-copy <server> <src> <dst> [--keep-agent] [--via-snapshot]
                            (plugins) copy a running guest container; without
                            --keep-agent its SPIRE Agent data stays out of the
                            copy; --via-snapshot copies from a stateless
                            snapshot (needed for a VM)
  spire-mode file_pull|config_push|auto|default
                            (plugins) set the server plugin's proof_mode and
                            restart SPIRE Server (default: from
                            LAB_SPIRE_MODES)
  test [-run REGEX] [--collect] [--record-host]
                            run the acceptance tests: the lab checks, then
                            the gate sessions (--collect: copy the sessions'
                            evidence and fixtures into the repository;
                            --record-host: record this machine's host name,
                            redacted otherwise)
  help                      show this help

preflight, up, deploy, reset and test take --scenario probe|plugins|all
(default: LAB_SCENARIOS, now "$LAB_SCENARIOS"). LAB_CLUSTER (now
"$LAB_CLUSTER": no, 5.21, 6 or both) chooses the running clusters for every
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
	if scenario_selected plugins; then
		spire_resolve
	fi
}

cmd_up() {
	run_preflight || die "the preflight failed: fix the failures above and run lab.sh up again"
	mkdir -p "$LAB_STATE_DIR"
	chmod 0700 "$LAB_STATE_DIR"
	ensure_ssh_key
	resolve_state
	ensure_pki
	if scenario_selected plugins; then
		ensure_spire_pki
	fi

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
		if scenario_selected plugins; then
			auth_spire "$server"
		fi
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
		ensure_tar "$server"
		baseline_grants "$server"
	done

	# a snapshot on the first success, then a new one of each server whose
	# baseline this up changed (instances added, VMs resized: reset would
	# otherwise undo them) or that has none (rebuilt after down --only); the
	# others' snapshots stay
	if ! any_snapshot_exists; then
		section "Snapshot"
		LAB_ASSUME_YES=1 lab_snapshot
	else
		for server in $(lab_servers) $(scenario_selected plugins && echo spire); do
			[[ " $LAB_BASELINE_CHANGED " == *" $server "* ]] || ! LAB_ONLY="$server" all_snapshots_exist || continue
			section "Snapshot of $server"
			LAB_ONLY="$server" LAB_ASSUME_YES=1 lab_snapshot
		done
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
	if [[ -n "$LAB_ONLY" ]]; then
		down_only
		return
	fi
	if ! confirm "Destroy the lab (its VMs, network $LAB_NETWORK, storage pool $LAB_POOL and $LAB_STATE_DIR)?"; then
		info "nothing destroyed"
		return 1
	fi
	destroy_all
	rm -rf "$LAB_STATE_DIR"
	info "the lab is gone (the download cache in $LAB_CACHE_DIR stays)"
}

# down_only: destroys one LXD server's VMs (a cluster's members) with their
# snapshots and forgets the server in env.json; the rest of the lab stays,
# and up builds the server again.
down_only() {
	local vm
	[[ "$LAB_ONLY" != spire ]] || die "lab.sh down --only takes lxd5, lxd6, c5 or c6"
	if ! confirm "Destroy $LAB_ONLY (VMs $(server_vms "$LAB_ONLY" | paste -sd' '), with their disks and snapshots)?"; then
		info "nothing destroyed"
		return 1
	fi
	for vm in $(server_vms "$LAB_ONLY"); do
		destroy_vm "$vm"
		env_del ".vms[\"$vm\"]"
	done
	env_del ".servers[\"$LAB_ONLY\"]"
	env_del ".snapshot.only[\"$LAB_ONLY\"]"
	info "$LAB_ONLY is gone: lab.sh up builds it again"
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
		# shellcheck disable=SC2016 # a jq program
		env_get '.snapshot.only // {} | to_entries[] | "\(.key): taken \(.value)"' |
			while IFS= read -r line; do info "$line"; done
	else
		info "none (lab.sh snapshot)"
	fi
	section "Deployed"
	info "lxd-probe $(env_get '.probe.version // "none (lab.sh deploy)"')"
	if [[ -n "$(env_get '.spire.version // empty')" ]]; then
		info "SPIRE $(env_get '.spire.version') on spire, trust domain $LAB_TRUST_DOMAIN, proof_mode $(spire_proof_mode)"
		info "plugins $(env_get '.plugins.version // "none (lab.sh deploy --scenario plugins)"')"
	fi
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
	if scenario_selected plugins; then
		deploy_plugins
	fi
}

# cmd_refresh: moves the LXD servers whose revision the settings do not pin
# to the newest revision of their channels, after confirmation, then offers a
# new snapshot (LAB_REFRESH_SNAPSHOT=no: never, so that LAB_ASSUME_YES keeps the old one).
cmd_refresh() {
	require_state
	select_scenarios "$(env_get '.scenarios | join(" ")')"
	select_only "$1"
	[[ "$LAB_ONLY" != spire ]] || die "lab.sh refresh --only takes lxd5, lxd6, c5 or c6"
	section "Refresh"
	local server sibling plans=() plan current new version
	# the revision and version of each standalone server after this refresh,
	# which its cluster follows
	local -A revisions=() versions=()
	for server in $(lab_servers); do
		if is_cluster "$server"; then
			sibling="$(cluster_sibling "$server")"
			plan="$(lxd_refresh_plan "$server" \
				"${revisions[$sibling]:-$(env_get ".servers[\"$sibling\"].revision")}" \
				"${versions[$sibling]:-$(env_get ".servers[\"$sibling\"].version")}")"
		else
			plan="$(lxd_refresh_plan "$server")"
		fi
		[[ -z "$plan" ]] || plans+=("$plan")
		if [[ -n "$plan" ]]; then
			read -r _ _ new version <<<"$plan"
			revisions[$server]="$new" versions[$server]="$version"
		fi
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
	if [[ "${LAB_REFRESH_SNAPSHOT:-}" != no ]] && confirm "Replace the snapshot of ${LAB_ONLY:-the whole lab} with the refreshed state?"; then
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
	if scenario_selected probe; then
		[[ -n "$(env_get '.probe.sha256 // empty')" ]] || die "lxd-probe is not deployed: run lab.sh deploy"
	fi
	if scenario_selected plugins; then
		[[ -n "$(env_get '.plugins.version // empty')" ]] || die "the plugins are not deployed: run lab.sh deploy --scenario plugins"
	fi
	export LAB_SELECTED_SCENARIOS="${LAB_SELECTED[*]}"
	export LAB_DIR REPO_DIR LAB_STATE_DIR LAB_EVIDENCE_DIR LAB_GUEST_VM_VCPUS LAB_GUEST_VM_RAM_GIB LAB_CLUSTER
	export LAB_COLLECT="$collect"
	# LAB_TEST_TIMEOUT: go test's timeout; a full stress soak (ST-3) may need more than the default
	local args=(-tags lab -count=1 -v -timeout "${LAB_TEST_TIMEOUT:-60m}")
	[[ -z "$run" ]] || args+=(-run "$run")
	(cd "$REPO_DIR" && go test "${args[@]}" ./test/lab/acceptance)
}

main() {
	local command="${1:-help}"
	shift || true
	load_settings
	load_ssh

	# --scenario applies to the commands that build or use a scenario
	local scenario="" only="" args=()
	while (($#)); do
		case "$1" in
		--only)
			[[ $# -ge 2 ]] || die "--only needs a value: lxd5, lxd6, c5, c6 or spire"
			only="$2"
			shift 2
			;;
		--only=*)
			only="${1#--only=}"
			shift
			;;
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
	preflight | up | deploy | reset | start | test) select_scenarios "$scenario" ;;
	*) [[ -z "$scenario" ]] || die "lab.sh $command does not take --scenario" ;;
	esac
	# --only scopes the commands that act on the VMs' disks
	case "$command" in
	down | snapshot | reset | refresh) ;;
	*) [[ -z "$only" ]] || die "lab.sh $command does not take --only" ;;
	esac

	case "$command" in
	preflight) run_preflight "${args[@]}" ;;
	up) cmd_up "${args[@]}" ;;
	down)
		# any built server, chosen by LAB_CLUSTER or not
		if [[ -n "$only" ]]; then
			require_state
			case "$only" in
			lxd5 | lxd6 | c5 | c6) ;;
			*) die "lab.sh down --only takes lxd5, lxd6, c5 or c6, not \"$only\"" ;;
			esac
			LAB_ONLY="$only"
		fi
		cmd_down
		;;
	status) cmd_status ;;
	ssh) cmd_ssh "${args[@]}" ;;
	logs) cmd_logs "${args[@]}" ;;
	snapshot | reset | stop | start)
		# the whole lab: every scenario built so far
		require_state
		select_scenarios "$(env_get '.scenarios | join(" ")')"
		select_only "$only"
		"lab_$command"
		;;
	deploy) cmd_deploy ;;
	probe) cmd_probe "${args[@]}" ;;
	guest) cmd_guest "${args[@]}" ;;
	pull) cmd_pull "${args[@]}" ;;
	token) cmd_token "${args[@]}" ;;
	guest-up) cmd_guest_up "${args[@]}" ;;
	guest-down) cmd_guest_down "${args[@]}" ;;
	guest-copy) cmd_guest_copy "${args[@]}" ;;
	spire-mode) cmd_spire_mode "${args[@]}" ;;
	refresh) cmd_refresh "$only" ;;
	test) cmd_test "${args[@]}" ;;
	help | -h | --help) usage ;;
	*)
		usage >&2
		exit 2
		;;
	esac
}

main "$@"
