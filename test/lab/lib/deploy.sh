# shellcheck shell=bash
# Deployment of the probe scenario: lxd-probe built on this host, pushed into
# every baseline instance, and one environment file per server and identity
# for running it here. The snapshot does not hold the binary: "deploy" after
# "reset" is the inner loop after a code change.

readonly LAB_GUEST_PROBE=/usr/local/bin/lxd-probe
# The guests' evidence: root's, and the probe user's (uid 1000).
readonly LAB_GUEST_EVIDENCE=/var/lib/lxd-probe/evidence

probe_bin() { echo "$LAB_STATE_DIR/bin/lxd-probe"; }
probe_env_dir() { echo "$LAB_STATE_DIR/probe"; }
probe_env_file() { echo "$(probe_env_dir)/$1-$2.env"; }

# guest_evidence_dir UID: where lxd-probe writes its evidence in a guest when
# it runs as UID.
guest_evidence_dir() {
	if [[ "$1" == 0 ]]; then
		echo "$LAB_GUEST_EVIDENCE"
	else
		echo "$LAB_GUEST_EVIDENCE-$1"
	fi
}

# probe_build: builds lxd-probe for the guests and this host (static,
# linux/amd64), with the repository's version, and records it in env.json.
probe_build() {
	local version commit pkg=github.com/dihedron/lxd-spiffe/pkg/metadata sha
	version="$(git -C "$REPO_DIR" describe --tags --always --dirty)"
	commit="$(git -C "$REPO_DIR" rev-parse HEAD)"
	info "building lxd-probe $version"
	mkdir -p "$(dirname "$(probe_bin)")"
	(cd "$REPO_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X $pkg.Version=$version -X $pkg.GitCommit=$commit" \
		-o "$(probe_bin)" ./cmd/lxd-probe) || die "cannot build lxd-probe"
	sha="$(sha256sum "$(probe_bin)" | cut -d' ' -f1)"
	env_set .probe.version "$version"
	env_set .probe.sha256 "$sha"
}

# probe_push SERVER: copies lxd-probe to the server's VM, then into every
# baseline instance, with the guests' evidence directories.
probe_push() {
	local server="$1" project name script=""
	scp -q "${SSH_OPTS[@]}" "$(probe_bin)" "$LAB_USER@$(vm_ip "$server"):lxd-probe" ||
		die "$server: cannot copy lxd-probe to the VM"
	while read -r project name _; do
		script+="sudo lxc file push lxd-probe $name$LAB_GUEST_PROBE --project $project --uid 0 --gid 0 --mode 0755"$'\n'
		script+="sudo lxc exec $name --project $project -- sh -c 'mkdir -p $(guest_evidence_dir 0) $(guest_evidence_dir 1000) && chmod 0700 $(guest_evidence_dir 0) $(guest_evidence_dir 1000) && chown 1000:1000 $(guest_evidence_dir 1000)'"$'\n'
	done < <(baseline_instances "$server")
	info "$server: installing lxd-probe in the baseline instances"
	vm_ssh "$server" "set -e; $script" >/dev/null || die "$server: cannot install lxd-probe in the instances"
}

# probe_env_common SERVER: the variables every environment file of SERVER
# has.
probe_env_common() {
	printf '%s=%q\n' \
		LXD_PROBE_ENDPOINT "$(env_get ".servers[\"$1\"].endpoint")" \
		LXD_PROBE_PIN "$(env_get ".servers[\"$1\"].fingerprint")" \
		LXD_PROBE_PROJECT default \
		LXD_PROBE_EVIDENCE_DIR "$LAB_EVIDENCE_DIR"
}

# probe_env_bearer SERVER IDENTITY TOKEN_FILE: the environment file of a
# bearer identity.
probe_env_bearer() {
	local file
	file="$(probe_env_file "$1" "$2")"
	mkdir -p "$(probe_env_dir)"
	chmod 0700 "$(probe_env_dir)"
	(
		umask 077
		{
			probe_env_common "$1"
			printf '%s=%q\n' LXD_PROBE_AUTH bearer LXD_PROBE_TOKEN_FILE "$3"
		} >"$file"
	)
	chmod 0600 "$file"
}

# probe_env_tls SERVER IDENTITY [COMMON]: the environment file of a TLS
# identity.
probe_env_tls() {
	local file common="${3:-$(probe_env_common "$1")}"
	file="$(probe_env_file "$1" "$2")"
	mkdir -p "$(probe_env_dir)"
	chmod 0700 "$(probe_env_dir)"
	(
		umask 077
		{
			printf '%s\n' "$common"
			printf '%s=%q\n' LXD_PROBE_AUTH tls \
				LXD_PROBE_CLIENT_CERT "$(identity_cert "$2")" \
				LXD_PROBE_CLIENT_KEY "$(identity_key "$2")"
		} >"$file"
	)
	chmod 0600 "$file"
}

# probe_env SERVER: the environment files of SERVER, one per identity (and
# one for its bearer identity), for "lab.sh probe" (probe spec PRN-32).
probe_env() {
	local server="$1" id common
	mkdir -p "$(probe_env_dir)"
	chmod 0700 "$(probe_env_dir)"
	common="$(probe_env_common "$server")"
	for id in "${LAB_IDENTITIES[@]}"; do
		probe_env_tls "$server" "$id" "$common"
	done
	if [[ "$(env_get ".servers[\"$server\"].auth.bearer // \"no\"")" == yes ]]; then
		probe_env_bearer "$server" bearer "$(bearer_token_file "$server")"
	else
		rm -f "$(probe_env_file "$server" bearer)"
	fi
}

# deploy_probe: the probe scenario's deployment on every server.
deploy_probe() {
	local server
	probe_build
	for server in $(lab_servers); do
		probe_push "$server"
		probe_env "$server"
	done
	info "lxd-probe $(env_get .probe.version) is deployed: lab.sh probe <server> -- <args>, lab.sh guest <server> <instance> -- <args>"
}
