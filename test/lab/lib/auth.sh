# shellcheck shell=bash
# Authorization on the LXD servers: one group per probe identity (lab-read,
# lab-files, lab-edit, lab-full, lab-outsider), the identities' TLS
# certificates in them,
# per-instance grants (LXD has no project-wide file access entitlement), and a
# bearer identity in lab-files where the server offers bearer identities for
# its remote API. Configured as root over each VM's unix socket.

readonly LAB_BEARER=lab-bearer
readonly LAB_BEARER_SHORT=lab-bearer-short

# vm_lxc SERVER ARGS...: runs lxc as root on SERVER's VM.
vm_lxc() {
	local server="$1"
	shift
	vm_ssh "$server" "sudo lxc $(printf '%q ' "$@")"
}

# lxd_has_extension SERVER EXTENSION
lxd_has_extension() {
	vm_lxc "$1" query /1.0 | jq -e --arg e "$2" '.api_extensions | index($e)' >/dev/null
}

bearer_token_file() { echo "$LAB_STATE_DIR/tokens/$1-bearer.token"; }
short_token_file() { echo "$LAB_STATE_DIR/tokens/$1-bearer-short.token"; }

# identity_entitlements IDENTITY: the instance entitlements of the identity's
# group (full is server admin instead).
identity_entitlements() {
	case "$1" in
	read | outsider) echo can_view ;;
	files) echo can_view can_access_files ;;
	edit) echo can_view can_edit ;;
	full) ;;
	esac
}

# identity_has_project IDENTITY PROJECT: whether the identity gets grants in
# PROJECT. outsider sees only default: in any other project LXD answers it
# as if nothing existed.
identity_has_project() {
	[[ "$1" != outsider || "$2" == default ]]
}

# auth_setup SERVER: the groups, the TLS identities and the bearer identity.
auth_setup() {
	local server="$1" id groups identities fp
	lxd_has_extension "$server" access_management_tls ||
		die "$server: LXD $(env_get ".servers[\"$server\"].version") has no fine-grained TLS identities (access_management_tls), which the lab needs"
	groups="$(vm_lxc "$server" query /1.0/auth/groups | jq -r '.[] | sub(".*/"; "")')"
	identities="$(vm_lxc "$server" auth identity list -f csv)"
	for id in "${LAB_IDENTITIES[@]}"; do
		if ! grep -qx "lab-$id" <<<"$groups"; then
			info "$server: creating group lab-$id"
			vm_lxc "$server" auth group create "lab-$id" >/dev/null
		fi
		if ! grep -q "^tls,[^,]*,lab-$id," <<<"$identities"; then
			info "$server: trusting identity $id"
			# through the API: the snap's lxc cannot read the VM's /tmp
			vm_lxc "$server" query -X POST /1.0/auth/identities/tls -d "$(jq -n \
				--arg name "lab-$id" --rawfile cert "$(identity_cert "$id")" --arg group "lab-$id" \
				'{name: $name, certificate: $cert, groups: [$group]}')" >/dev/null ||
				die "$server: cannot trust identity $id"
		fi
		fp="$(identity_fingerprint "$id")"
		env_set ".servers[\"$server\"].auth.identities[\"$id\"]" "$fp"
	done
	if ! vm_lxc "$server" query /1.0/auth/groups/lab-full | jq -e '(.permissions // [])[] | select(.entity_type == "server" and .entitlement == "admin")' >/dev/null; then
		vm_lxc "$server" auth group permission add lab-full server admin >/dev/null
	fi
	env_set ".servers[\"$server\"].auth.model" fine-grained
	if lxd_has_extension "$server" auth_bearer; then
		env_set ".servers[\"$server\"].auth.bearer" yes
		auth_bearer "$server" "$identities"
	else
		env_set ".servers[\"$server\"].auth.bearer" no
	fi
}

# auth_bearer SERVER IDENTITIES: the bearer identity in lab-files, and its
# token in the state directory (issuing a new one revokes the previous).
auth_bearer() {
	local server="$1" token file
	file="$(bearer_token_file "$server")"
	if ! grep -q "^bearer,[^,]*,$LAB_BEARER," <<<"$2"; then
		info "$server: creating bearer identity $LAB_BEARER"
		vm_lxc "$server" auth identity create "bearer/$LAB_BEARER" --group lab-files >/dev/null
		rm -f "$file"
	fi
	[[ -s "$file" ]] && return
	token="$(vm_lxc "$server" auth identity token issue "bearer/$LAB_BEARER" | tail -n1)"
	[[ -n "$token" ]] || die "$server: no token issued for bearer/$LAB_BEARER"
	mkdir -p "${file%/*}"
	chmod 0700 "${file%/*}"
	(
		umask 077
		printf '%s\n' "$token" >"$file"
	)
	env_set ".servers[\"$server\"].auth.bearer_token" "$file"
}

# cmd_token SERVER [--expiry DURATIONS]: issues a short-lived token to
# lab-bearer-short (created in lab-files on first use), which revokes the
# one issued before, and writes its environment file, for "probe --as
# bearer-short" (L13). lab-bearer's long-lived token is never touched.
cmd_token() {
	require_state
	local usage="usage: lab.sh token <server> [--expiry <durations>]"
	local server="${1:-}" expiry=2M file token
	[[ -n "$server" ]] || die "$usage"
	shift
	require_server "$server"
	while (($#)); do
		case "$1" in
		--expiry)
			[[ $# -ge 2 ]] || die "$usage"
			expiry="$2"
			shift 2
			;;
		*) die "$usage" ;;
		esac
	done
	[[ "$(env_get ".servers[\"$server\"].auth.bearer // \"no\"")" == yes ]] ||
		die "$server has no bearer identities (LXD $(env_get ".servers[\"$server\"].version"))"
	if ! vm_lxc "$server" auth identity list -f csv | grep -q "^bearer,[^,]*,$LAB_BEARER_SHORT,"; then
		info "$server: creating bearer identity $LAB_BEARER_SHORT"
		vm_lxc "$server" auth identity create "bearer/$LAB_BEARER_SHORT" --group lab-files >/dev/null
	fi
	token="$(vm_lxc "$server" auth identity token issue "bearer/$LAB_BEARER_SHORT" --expiry "$expiry" | tail -n1)"
	[[ -n "$token" ]] || die "$server: no token issued for bearer/$LAB_BEARER_SHORT"
	file="$(short_token_file "$server")"
	mkdir -p "${file%/*}"
	chmod 0700 "${file%/*}"
	(
		umask 077
		printf '%s\n' "$token" >"$file"
	)
	probe_env_bearer "$server" bearer-short "$file"
	info "$server: issued a token to $LAB_BEARER_SHORT for $expiry: lab.sh probe $server --as bearer-short -- <args>"
}

# lab_grant SERVER PROJECT INSTANCE...: gives the probe identities' groups
# their entitlements on the instances of PROJECT, and can_view on PROJECT
# (LXD grants nothing in a project the group cannot view), for what is
# missing. Runbooks call it for the instances they create.
lab_grant() {
	local server="$1" project="$2" id entitlement instance have script=""
	shift 2
	for id in "${LAB_IDENTITIES[@]}"; do
		[[ -n "$(identity_entitlements "$id")" ]] || continue
		identity_has_project "$id" "$project" || continue
		have="$(vm_lxc "$server" query "/1.0/auth/groups/lab-$id" | jq -r '
			(.permissions // [])[]
			| if .entity_type == "project" then
				"project/\(.url | sub("^/1\\.0/projects/"; ""))/\(.entitlement)"
			elif .entity_type == "instance" then
				(.url | capture("^/1\\.0/instances/(?<name>[^?]+)(\\?project=(?<project>.*))?$")) as $u
				| "\($u.project // "default")/\($u.name)/\(.entitlement)"
			else empty end')"
		grep -qx "project/$project/can_view" <<<"$have" ||
			script+="sudo lxc auth group permission add lab-$id project $project can_view"$'\n'
		for instance in "$@"; do
			for entitlement in $(identity_entitlements "$id"); do
				grep -qx "$project/$instance/$entitlement" <<<"$have" && continue
				script+="sudo lxc auth group permission add lab-$id instance $instance $entitlement project=$project"$'\n'
			done
		done
	done
	[[ -z "$script" ]] && return
	info "$server: granting the identities access to $project/{$(
		IFS=,
		echo "$*"
	)}"
	vm_ssh "$server" "set -e; $script" >/dev/null || die "$server: cannot grant the identities access to $project/$*"
}
