# shellcheck shell=bash
# Running lxd-probe by hand, for runbook steps: on this host against a server
# as one of the identities ("probe"), or inside an instance ("guest"). The
# session variables LXD_PROBE_RUN_ID, LXD_PROBE_GATE and LXD_PROBE_CELL pass
# through from the caller, so that one run can span both environments, and
# so do LXD_PROBE_LOG_LEVEL and LXD_PROBE_RECORD_HOST.

readonly LAB_PROBE_SESSION_VARS=(LXD_PROBE_RUN_ID LXD_PROBE_GATE LXD_PROBE_CELL LXD_PROBE_LOG_LEVEL LXD_PROBE_RECORD_HOST)

# require_server SERVER: dies unless SERVER is one of the lab's LXD servers.
require_server() {
	env_get '.servers | keys[]' | grep -qx "$1" ||
		die "unknown server \"$1\"; servers: $(env_get '.servers | keys | join(" ")')"
}

# require_deployed: dies unless deploy has built lxd-probe.
require_deployed() {
	[[ -x "$(probe_bin)" ]] || die "lxd-probe is not deployed: run lab.sh deploy"
}

# cmd_probe SERVER [--as IDENTITY] -- ARGS...: runs lxd-probe ARGS on this
# host with SERVER's and IDENTITY's environment file.
cmd_probe() {
	require_state
	require_deployed
	local usage="usage: lab.sh probe <server> [--as <identity>] -- <args>"
	local server="${1:-}" identity=full file
	[[ -n "$server" ]] || die "$usage"
	shift
	require_server "$server"
	while (($#)); do
		case "$1" in
		--as)
			[[ $# -ge 2 ]] || die "$usage"
			identity="$2"
			shift 2
			;;
		--)
			shift
			break
			;;
		*) die "$usage" ;;
		esac
	done
	file="$(probe_env_file "$server" "$identity")"
	[[ -f "$file" ]] || die "$server has no identity \"$identity\" (identities: ${LAB_IDENTITIES[*]}, and bearer where the server has bearer identities); or run lab.sh deploy"
	(
		set -a
		# shellcheck disable=SC1090 # written by probe_env
		source "$file"
		set +a
		exec "$(probe_bin)" "$@"
	)
}

# cmd_guest SERVER INSTANCE [--project P] [--user UID] -- ARGS...: runs
# lxd-probe ARGS inside INSTANCE, as root unless --user is given.
cmd_guest() {
	require_state
	local usage="usage: lab.sh guest <server> <instance> [--project <project>] [--user <uid>] -- <args>"
	local server="${1:-}" instance="${2:-}" project=default uid=0 var env=()
	[[ -n "$server" && -n "$instance" ]] || die "$usage"
	shift 2
	require_server "$server"
	while (($#)); do
		case "$1" in
		--project)
			[[ $# -ge 2 ]] || die "$usage"
			project="$2"
			shift 2
			;;
		--user)
			[[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || die "--user takes a numeric uid; $usage"
			uid="$2"
			shift 2
			;;
		--)
			shift
			break
			;;
		*) die "$usage" ;;
		esac
	done
	env=(--env "LXD_PROBE_EVIDENCE_DIR=$(guest_evidence_dir "$uid")")
	for var in "${LAB_PROBE_SESSION_VARS[@]}"; do
		[[ -z "${!var:-}" ]] || env+=(--env "$var=${!var}")
	done
	vm_lxc "$server" exec "$instance" --project "$project" --user "$uid" --group "$uid" --cwd / \
		"${env[@]}" -- "$LAB_GUEST_PROBE" "$@"
}

# cmd_pull SERVER INSTANCE [--project P] [--user UID] RUN_ID: copies the
# guest run RUN_ID, written as root or as UID, from the instance into
# LAB_EVIDENCE_DIR/RUN_ID/, streamed through tar (no files left on the VM).
# Files already pulled are replaced.
cmd_pull() {
	require_state
	local usage="usage: lab.sh pull <server> <instance> [--project <project>] [--user <uid>] <run_id>"
	local server="${1:-}" instance="${2:-}" project=default uid=0 run=""
	[[ -n "$server" && -n "$instance" ]] || die "$usage"
	shift 2
	require_server "$server"
	while (($#)); do
		case "$1" in
		--project)
			[[ $# -ge 2 ]] || die "$usage"
			project="$2"
			shift 2
			;;
		--user)
			[[ $# -ge 2 && "$2" =~ ^[0-9]+$ ]] || die "--user takes a numeric uid; $usage"
			uid="$2"
			shift 2
			;;
		-*) die "$usage" ;;
		*)
			[[ -z "$run" ]] || die "$usage"
			run="$1"
			shift
			;;
		esac
	done
	# a run id is one path element (evidence.NewRunID, PRE-07)
	[[ "$run" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || die "not a run id: \"$run\"; $usage"
	local dir
	dir="$(guest_evidence_dir "$uid")"
	mkdir -p "$LAB_EVIDENCE_DIR"
	vm_lxc "$server" exec "$instance" --project "$project" -- tar -C "$dir" -cf - "$run" |
		tar -xf - -C "$LAB_EVIDENCE_DIR" ||
		die "$server: cannot pull run $run from $project/$instance ($dir)"
	info "pulled $project/$instance:$dir/$run into $LAB_EVIDENCE_DIR/$run"
}
