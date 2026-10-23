# shellcheck shell=bash
# The plugins scenario: SPIRE Server with lxd-server-plugin on the spire VM,
# its LXD identity (spire, group lab-spire) on every server, the artifacts the
# guests install from http://spire.lab:8080/, and the guests' cloud-init with
# SPIRE Agent and lxd-agent-plugin. See .specs/lxd-test-environment.md.

readonly LAB_SPIRE_ID=spire
readonly LAB_SPIRE_GROUP=lab-spire
readonly SPIRE_SERVER_PORT=8081
readonly SPIRE_ARTIFACTS_PORT=8080
# where the guests and the spire VM keep SPIRE's files
readonly SPIRE_PREFIX=/opt/spire
readonly SPIRE_ETC=/etc/spire

spire_dir() { echo "$LAB_STATE_DIR/spire"; }
guest_config() { echo "$LAB_STATE_DIR/guest/$1.yaml"; }

# --- Versions -----------------------------------------------------------------

# sdk_minor: the major.minor of the plugin SDK in go.mod (e.g. 1.15), the
# SPIRE releases it is compatible with.
sdk_minor() {
	local v
	v="$(awk '$1 == "github.com/spiffe/spire-plugin-sdk" { print $2; exit }' "$REPO_DIR/go.mod")"
	v="${v#v}"
	echo "${v%.*}"
}

spire_tarball_url() { echo "https://github.com/spiffe/spire/releases/download/v$1/spire-$1-linux-amd64-musl.tar.gz"; }

# spire_resolve: the SPIRE release and its SHA-256, once per lab: the
# pinned LAB_SPIRE_VERSION (with LAB_SPIRE_SHA256), or the most recent
# release of the SDK's minor version, with the checksum it publishes.
spire_resolve() {
	[[ -z "$(env_get '.spire.version // empty')" ]] || return 0
	local minor version sha
	minor="$(sdk_minor)"
	if [[ -n "$LAB_SPIRE_VERSION" ]]; then
		[[ -n "$LAB_SPIRE_SHA256" ]] || die "LAB_SPIRE_VERSION=$LAB_SPIRE_VERSION needs LAB_SPIRE_SHA256"
		version="${LAB_SPIRE_VERSION#v}" sha="$LAB_SPIRE_SHA256"
	else
		version="$(curl -fsSL --proto '=https' 'https://api.github.com/repos/spiffe/spire/releases?per_page=50' |
			jq -r --arg m "$minor." '[.[] | select(.prerelease | not) | .tag_name | ltrimstr("v") | select(startswith($m))] | .[]' |
			sort -V | tail -n1)"
		[[ -n "$version" ]] || die "no SPIRE release $minor.x found: set LAB_SPIRE_VERSION and LAB_SPIRE_SHA256"
		sha="$(curl -fsSL --proto '=https' "$(spire_tarball_url "$version" | sed 's/\.tar\.gz$/_sha256sum.txt/')" | awk '{ print $1; exit }')"
		[[ "$sha" =~ ^[0-9a-f]{64}$ ]] || die "no published checksum for SPIRE $version"
	fi
	env_set .spire.version "$version"
	env_set .spire.sha256 "$sha"
	info "SPIRE $version (${sha:0:12}), compatible with spire-plugin-sdk $minor"
}

# --- LXD identity ---------------------------------------------------------------

# ensure_spire_pki: the spire identity's certificate, like the probe's.
ensure_spire_pki() {
	[[ -f "$(identity_cert "$LAB_SPIRE_ID")" && -f "$(identity_key "$LAB_SPIRE_ID")" ]] && return
	info "creating the client certificate of identity $LAB_SPIRE_ID"
	(
		umask 077
		openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
			-days 730 -subj "/CN=lxd-lab-$LAB_SPIRE_ID" \
			-addext "keyUsage=critical,digitalSignature" \
			-addext "extendedKeyUsage=clientAuth" \
			-keyout "$(identity_key "$LAB_SPIRE_ID")" -out "$(identity_cert "$LAB_SPIRE_ID")" 2>/dev/null
	) || die "cannot create the certificate of identity $LAB_SPIRE_ID"
}

# auth_spire SERVER: group lab-spire with can_view on project default, and
# the spire identity in it; instance grants come per guest (spire_grant).
auth_spire() {
	local server="$1"
	if ! vm_lxc "$server" query /1.0/auth/groups | jq -e --arg g "/1.0/auth/groups/$LAB_SPIRE_GROUP" 'index($g)' >/dev/null; then
		info "$server: creating group $LAB_SPIRE_GROUP"
		vm_lxc "$server" auth group create "$LAB_SPIRE_GROUP" >/dev/null
	fi
	if ! vm_lxc "$server" auth identity list -f csv | grep -q "^tls,[^,]*,lab-$LAB_SPIRE_ID,"; then
		info "$server: trusting identity $LAB_SPIRE_ID"
		vm_lxc "$server" query -X POST /1.0/auth/identities/tls -d "$(jq -n \
			--arg name "lab-$LAB_SPIRE_ID" --rawfile cert "$(identity_cert "$LAB_SPIRE_ID")" --arg group "$LAB_SPIRE_GROUP" \
			'{name: $name, certificate: $cert, groups: [$group]}')" >/dev/null ||
			die "$server: cannot trust identity $LAB_SPIRE_ID"
	fi
	if ! vm_lxc "$server" query "/1.0/auth/groups/$LAB_SPIRE_GROUP" | jq -e '(.permissions // [])[] | select(.entity_type == "project" and .url == "/1.0/projects/default" and .entitlement == "can_view")' >/dev/null; then
		vm_lxc "$server" auth group permission add "$LAB_SPIRE_GROUP" project default can_view >/dev/null
	fi
	env_set ".servers[\"$server\"].auth.spire" "$(identity_fingerprint "$LAB_SPIRE_ID")"
}

# spire_entitlements [--no-files]: the instance entitlements of lab-spire:
# can_view, can_access_files (file_pull, unless --no-files), can_edit when
# LAB_SPIRE_MODES has config_push.
spire_entitlements() {
	echo can_view
	[[ "${1:-}" == --no-files ]] || echo can_access_files
	[[ " $LAB_SPIRE_MODES " == *" config_push "* ]] && echo can_edit
	return 0
}

# spire_permissions SERVER GROUP-ACTION INSTANCE ENTITLEMENT...: adds or
# removes lab-spire's grants on an instance of default.
spire_permissions() {
	local server="$1" action="$2" instance="$3" entitlement script=""
	shift 3
	for entitlement in "$@"; do
		script+="sudo lxc auth group permission $action $LAB_SPIRE_GROUP instance $instance $entitlement project=default || $([[ $action == remove ]] && echo true || echo false)"$'\n'
	done
	vm_ssh "$server" "set -e; $script" >/dev/null
}

# --- Deployment -----------------------------------------------------------------

# plugins_build: the packages, built and checked against their checksums
# file as an operator would; records the version and the binaries' checksums
# (plugin_checksum).
plugins_build() {
	local dist="$REPO_DIR/dist" sums version
	info "building the packages (make snapshot)"
	make -C "$REPO_DIR" snapshot >"$LAB_STATE_DIR/snapshot.log" 2>&1 || die "make snapshot failed: see $LAB_STATE_DIR/snapshot.log"
	sums="$(ls "$dist"/*_checksums.txt)"
	(cd "$dist" && sha256sum -c --quiet --ignore-missing "$sums") || die "the packages do not match $sums"
	version="$(sed -n 's/^lxd-spiffe_\(.*\)_checksums\.txt$/\1/p' <<<"${sums##*/}")"
	env_set .plugins.version "$version"
	env_set .plugins.server_checksum "$(sha256sum "$dist/lxd-server-plugin_linux_amd64_v1/lxd-server-plugin" | cut -d' ' -f1)"
	env_set .plugins.agent_checksum "$(sha256sum "$dist/lxd-agent-plugin_linux_amd64_v1/lxd-agent-plugin" | cut -d' ' -f1)"
}

# plugin_artifact NAME SUFFIX: the path of a package in dist/, e.g.
# plugin_artifact lxd-agent-plugin linux_amd64.deb.
plugin_artifact() { echo "$REPO_DIR/dist/$1_$(env_get .plugins.version)_$2"; }

# spire_proof_mode: proof_mode for LAB_SPIRE_MODES, unless a test chose one.
spire_proof_mode() {
	local mode
	mode="$(env_get '.spire.proof_mode // empty')"
	if [[ -z "$mode" ]]; then
		local pull=no push=no
		[[ " $LAB_SPIRE_MODES " == *" file_pull "* ]] && pull=yes
		[[ " $LAB_SPIRE_MODES " == *" config_push "* ]] && push=yes
		case "$pull$push" in
		yesyes) mode=auto ;;
		noyes) mode=config_push ;;
		*) mode=file_pull ;;
		esac
	fi
	echo "$mode"
}

# server_conf: SPIRE Server's configuration, one servers entry per running
# LXD server (an unreachable server fails every attestation closed, so only
# the chosen ones are listed).
server_conf() {
	local server entries=""
	for server in $(lab_servers); do
		entries+="$(
			cat <<ENTRY
      "$server" = {
        url                     = "$(env_get ".servers[\"$server\"].endpoint")"
        client_cert_file        = "$SPIRE_ETC/lxd/$LAB_SPIRE_ID.crt"
        client_key_file         = "$SPIRE_ETC/lxd/$LAB_SPIRE_ID.key"
        server_cert_fingerprint = "sha256:$(env_get ".servers[\"$server\"].fingerprint")"
        projects                = ["default"]
      }
ENTRY
		)"$'\n'
	done
	cat <<CONF
server {
  bind_address = "0.0.0.0"
  bind_port    = "$SPIRE_SERVER_PORT"
  trust_domain = "$LAB_TRUST_DOMAIN"
  data_dir     = "/var/lib/spire/server"
  log_level    = "DEBUG"
  ca_ttl       = "168h"
$([[ -z "$LAB_SPIRE_AGENT_TTL" ]] || echo "  agent_ttl    = \"$LAB_SPIRE_AGENT_TTL\"")
}

plugins {
  DataStore "sql" {
    plugin_data {
      database_type     = "sqlite3"
      connection_string = "/var/lib/spire/server/datastore.sqlite3"
    }
  }
  KeyManager "disk" {
    plugin_data {
      keys_path = "/var/lib/spire/server/keys.json"
    }
  }
  NodeAttestor "lxd_instance" {
    plugin_cmd      = "/usr/bin/lxd-server-plugin"
    plugin_checksum = "$(env_get .plugins.server_checksum)"
    plugin_data {
      proof_mode = "$(spire_proof_mode)"
      # the lab attests a privileged container (E2E-1)
      policy {
        allow_privileged = true
      }
      servers = {
$entries      }
    }
  }
}
CONF
}

# spire_install: SPIRE Server, the server plugin and the artifact server on
# the spire VM.
spire_install() {
	local tarball deb version
	version="$(env_get .spire.version)"
	tarball="$(fetch "$(spire_tarball_url "$version")" "$(env_get .spire.sha256)")"
	deb="$(plugin_artifact lxd-server-plugin linux_amd64.deb)"
	mkdir -p "$(spire_dir)"
	server_conf >"$(spire_dir)/server.conf"
	info "spire: installing SPIRE $version and lxd-server-plugin $(env_get .plugins.version)"
	scp -q "${SSH_OPTS[@]}" "$tarball" "$deb" "$(spire_dir)/server.conf" \
		"$(identity_cert "$LAB_SPIRE_ID")" "$(identity_key "$LAB_SPIRE_ID")" \
		"$LAB_USER@$(vm_ip spire):" || die "spire: cannot copy SPIRE and the plugin"
	vm_ssh spire "set -e
		sudo systemctl stop spire-server 2>/dev/null || true
		id spire >/dev/null 2>&1 || sudo useradd --system --home /var/lib/spire --shell /usr/sbin/nologin spire
		sudo rm -rf $SPIRE_PREFIX /var/lib/spire/server
		sudo mkdir -p $SPIRE_PREFIX $SPIRE_ETC/lxd /var/lib/spire/server
		sudo tar -xzf ${tarball##*/} -C $SPIRE_PREFIX --strip-components=1
		sudo dpkg -i ${deb##*/} >/dev/null
		sudo install -m 0644 server.conf $SPIRE_ETC/server.conf
		sudo install -m 0444 $LAB_SPIRE_ID.crt $SPIRE_ETC/lxd/$LAB_SPIRE_ID.crt
		sudo install -m 0400 -o spire $LAB_SPIRE_ID.key $SPIRE_ETC/lxd/$LAB_SPIRE_ID.key
		rm -f $LAB_SPIRE_ID.key
		sudo chown -R spire:spire /var/lib/spire
		sudo tee /etc/systemd/system/spire-server.service >/dev/null <<'UNIT'
[Unit]
Description=SPIRE Server (lxd-spiffe lab)
After=network-online.target
Wants=network-online.target

[Service]
User=spire
ExecStart=$SPIRE_PREFIX/bin/spire-server run -config $SPIRE_ETC/server.conf
Restart=on-failure

[Install]
WantedBy=multi-user.target
UNIT
		sudo systemctl daemon-reload
		sudo systemctl enable --now spire-server >/dev/null 2>&1" || die "spire: cannot install SPIRE Server"
	spire_wait_healthy
}

# spire_server ARGS...: runs spire-server on the spire VM against the running
# server.
spire_server() { vm_ssh spire "sudo $SPIRE_PREFIX/bin/spire-server $(printf '%q ' "$@")"; }

spire_wait_healthy() {
	local deadline=$((SECONDS + 120))
	until spire_server healthcheck >/dev/null 2>&1; do
		((SECONDS < deadline)) || die "spire: SPIRE Server is not healthy (lab.sh logs spire spire-server)"
		sleep 2
	done
	info "spire: SPIRE Server is healthy, proof_mode $(spire_proof_mode)"
}

# spire_artifacts: what the guests install, on http://spire.lab:8080/, and
# their SHA-256 in env.json.
spire_artifacts() {
	local tarball files=() f
	tarball="$(fetch "$(spire_tarball_url "$(env_get .spire.version)")" "$(env_get .spire.sha256)")"
	files=("$tarball" "$(plugin_artifact lxd-agent-plugin linux_amd64.deb)" "$(plugin_artifact lxd-agent-plugin linux_amd64.rpm)"
		"$(plugin_artifact lxd-agent-plugin Linux_x86_64.tar.gz)")
	for f in "${files[@]}"; do
		[[ -f "$f" ]] || die "missing artifact $f"
	done
	scp -q "${SSH_OPTS[@]}" "${files[@]}" "$LAB_USER@$(vm_ip spire):" || die "spire: cannot copy the artifacts"
	vm_ssh spire "set -e
		sudo rm -rf /srv/lab-artifacts && sudo mkdir -p /srv/lab-artifacts
		sudo mv $(printf '%q ' "${files[@]##*/}") /srv/lab-artifacts/
		sudo chmod 0644 /srv/lab-artifacts/*
		sudo tee /etc/systemd/system/lab-artifacts.service >/dev/null <<'UNIT'
[Unit]
Description=lxd-spiffe lab artifacts for the guests
After=network-online.target

[Service]
DynamicUser=yes
ExecStart=/usr/bin/python3 -m http.server $SPIRE_ARTIFACTS_PORT --directory /srv/lab-artifacts

[Install]
WantedBy=multi-user.target
UNIT
		sudo systemctl daemon-reload
		sudo systemctl enable lab-artifacts >/dev/null 2>&1
		sudo systemctl restart lab-artifacts" || die "spire: cannot publish the artifacts"
	env_set .plugins.agent_deb "${files[1]##*/}"
	env_set .plugins.agent_deb_sha256 "$(sha256sum "${files[1]}" | cut -d' ' -f1)"
}

# guest_ubuntu_config: the cloud-init of Ubuntu guests: SPIRE Agent and the
# agent plugin from the artifacts, checked against their SHA-256, SPIRE's
# trust bundle embedded, and a SPIRE Agent unit running as root (FR-A7)
# after lxd-agent in a VM (attestor spec, Operational guidance item 10).
guest_ubuntu_config() {
	local version base="http://spire.lab:$SPIRE_ARTIFACTS_PORT" bundle
	version="$(env_get .spire.version)"
	bundle="$(spire_server bundle show -format pem)" || die "spire: cannot read the trust bundle"
	cat <<CLOUD
#cloud-config
write_files:
  - path: $SPIRE_ETC/bundle.pem
    permissions: "0644"
    content: |
$(sed 's/^/      /' <<<"$bundle")
  - path: $SPIRE_ETC/agent.conf
    permissions: "0644"
    content: |
      agent {
        data_dir          = "/var/lib/spire/agent"
        log_level         = "DEBUG"
        server_address    = "spire.lab"
        server_port       = "$SPIRE_SERVER_PORT"
        socket_path       = "/run/spire/agent/public/api.sock"
        trust_bundle_path = "$SPIRE_ETC/bundle.pem"
        trust_domain      = "$LAB_TRUST_DOMAIN"
      }
      plugins {
        NodeAttestor "lxd_instance" {
          plugin_cmd      = "/usr/bin/lxd-agent-plugin"
          plugin_checksum = "$(env_get .plugins.agent_checksum)"
          plugin_data {
            claim {
              project = "default"
            }
          }
        }
        KeyManager "disk" {
          plugin_data {
            directory = "/var/lib/spire/agent"
          }
        }
        WorkloadAttestor "unix" {
          plugin_data {}
        }
      }
  - path: /etc/systemd/system/spire-agent.service
    permissions: "0644"
    content: |
      [Unit]
      Description=SPIRE Agent (lxd-spiffe lab)
      After=network-online.target lxd-agent.service
      Wants=network-online.target

      [Service]
      ExecStart=$SPIRE_PREFIX/bin/spire-agent run -config $SPIRE_ETC/agent.conf
      Restart=on-failure
      RestartSec=5

      [Install]
      WantedBy=multi-user.target
runcmd:
  - set -e
  - cd /tmp
  - curl -fsSLO $base/spire-$version-linux-amd64-musl.tar.gz
  - curl -fsSLO $base/$(env_get .plugins.agent_deb)
  - echo "$(env_get .spire.sha256)  spire-$version-linux-amd64-musl.tar.gz" | sha256sum -c
  - echo "$(env_get .plugins.agent_deb_sha256)  $(env_get .plugins.agent_deb)" | sha256sum -c
  - mkdir -p $SPIRE_PREFIX /var/lib/spire/agent
  - tar -xzf spire-$version-linux-amd64-musl.tar.gz -C $SPIRE_PREFIX --strip-components=1
  - dpkg -i $(env_get .plugins.agent_deb)
  - systemctl daemon-reload
  - systemctl enable --now spire-agent
CLOUD
}

# deploy_plugins: the plugins scenario's deployment.
deploy_plugins() {
	[[ "$(vm_state spire)" == running ]] || die "spire is not running: lab.sh up --scenario plugins"
	plugins_build
	spire_install
	spire_artifacts
	local server
	for server in $(lab_servers); do
		probe_env_tls "$server" "$LAB_SPIRE_ID"
	done
	mkdir -p "$(dirname "$(guest_config ubuntu)")"
	guest_ubuntu_config >"$(guest_config ubuntu)"
	info "the plugins $(env_get .plugins.version) are deployed: lab.sh guest-up <server> <name>"
}

# cmd_spire_mode MODE: changes the server plugin's proof_mode (file_pull,
# config_push or auto) and restarts SPIRE Server; "default" goes back to
# LAB_SPIRE_MODES.
cmd_spire_mode() {
	require_state
	local mode="${1:-}"
	case "$mode" in
	file_pull | config_push | auto) env_set .spire.proof_mode "$mode" ;;
	default) env_set .spire.proof_mode "" ;;
	*) die "usage: lab.sh spire-mode file_pull|config_push|auto|default" ;;
	esac
	select_scenarios "$(env_get '.scenarios | join(" ")')"
	server_conf >"$(spire_dir)/server.conf"
	scp -q "${SSH_OPTS[@]}" "$(spire_dir)/server.conf" "$LAB_USER@$(vm_ip spire):" || die "spire: cannot copy server.conf"
	vm_ssh spire "sudo install -m 0644 server.conf $SPIRE_ETC/server.conf && sudo systemctl restart spire-server" ||
		die "spire: cannot restart SPIRE Server"
	spire_wait_healthy
}

# --- Guests -----------------------------------------------------------------------

# cmd_guest_up SERVER NAME [--vm] [--privileged] [--no-files] [--target M]
# [--project P] [--claim-name N]: launches an Ubuntu guest with the SPIRE
# Agent cloud-init, and grants lab-spire its entitlements on it (--no-files:
# without can_access_files). --project puts the guest in another project
# (and claims it; lab-spire gets no grants there) and --claim-name makes the
# agent claim another instance's name.
cmd_guest_up() {
	require_state
	local usage="usage: lab.sh guest-up <server> <name> [--vm] [--privileged] [--no-files] [--target <member>] [--project <project>] [--claim-name <name>]"
	local server="${1:-}" name="${2:-}" args=() files="" project=default claim_name="" config user_data
	[[ -n "$server" && -n "$name" ]] || die "$usage"
	shift 2
	require_server "$server"
	config="$(guest_config ubuntu)"
	[[ -f "$config" ]] || die "no guest cloud-init: lab.sh deploy --scenario plugins"
	while (($#)); do
		case "$1" in
		--vm) args+=(--vm -c "limits.cpu=$LAB_GUEST_VM_VCPUS" -c "limits.memory=${LAB_GUEST_VM_RAM_GIB}GiB") ;;
		--privileged) args+=(-c security.privileged=true -c security.nesting=true) ;;
		--no-files) files=--no-files ;;
		--target | --project | --claim-name)
			[[ $# -ge 2 ]] || die "$usage"
			case "$1" in
			--target) args+=(--target "$2") ;;
			--project) project="$2" ;;
			--claim-name) claim_name="$2" ;;
			esac
			shift
			;;
		*) die "$usage" ;;
		esac
		shift
	done
	# the claim of the agent: its project, and a name other than its own if asked
	user_data="$LAB_STATE_DIR/guest/$server-$name.yaml"
	sed -e "s/^\( *\)project = \"default\"$/\1project = \"$project\"/" \
		${claim_name:+-e "s/^\( *\)project = \"$project\"$/&\n\1instance_name = \"$claim_name\"/"} \
		"$config" >"$user_data"
	scp -q "${SSH_OPTS[@]}" "$user_data" "$LAB_USER@$(vm_ip "$server"):guest-$name.yaml" || die "$server: cannot copy the guest cloud-init"
	info "$server: launching guest $project/$name"
	# the grants come before the agent's first attestation
	vm_ssh "$server" "set -e; sudo lxc init $LAB_IMAGE_UBUNTU $(printf '%q ' "$name" "${args[@]}") --project $project >/dev/null
		sudo lxc config set $name --project $project cloud-init.user-data=\"\$(cat guest-$name.yaml)\"" ||
		die "$server: cannot create guest $name"
	if [[ "$project" == default ]]; then
		# shellcheck disable=SC2046 # one entitlement per word
		spire_permissions "$server" add "$name" $(spire_entitlements $files) || die "$server: cannot grant lab-spire access to $name"
	fi
	vm_lxc "$server" start "$name" --project "$project" >/dev/null || die "$server: cannot start guest $name"
}

# cmd_guest_down SERVER NAME [--project P]: deletes the guest and, in
# default, lab-spire's grants on it.
cmd_guest_down() {
	require_state
	local server="${1:-}" name="${2:-}" project=default
	[[ -n "$server" && -n "$name" ]] || die "usage: lab.sh guest-down <server> <name> [--project <project>]"
	shift 2
	[[ "${1:-}" != --project ]] || project="${2:?usage: --project <project>}"
	require_server "$server"
	if [[ "$project" == default ]]; then
		spire_permissions "$server" remove "$name" can_view can_access_files can_edit
	fi
	vm_lxc "$server" delete --force "$name" --project "$project" >/dev/null 2>&1 || true
	rm -f "$LAB_STATE_DIR/guest/$server-$name.yaml"
	info "$server: guest $project/$name deleted"
}

# cmd_guest_copy SERVER SRC DST [--keep-agent]: copies the running container
# SRC (project default) to DST, grants lab-spire its entitlements on DST and
# starts it. Without --keep-agent the SPIRE Agent's data (its SVID and key)
# is moved aside in SRC while the copy is made, so that the copy has to
# attest; with it the copy inherits them (E2E-4).
cmd_guest_copy() {
	require_state
	local usage="usage: lab.sh guest-copy <server> <src> <dst> [--keep-agent]"
	local server="${1:-}" src="${2:-}" dst="${3:-}" keep=no
	[[ -n "$server" && -n "$src" && -n "$dst" ]] || die "$usage"
	shift 3
	[[ "${1:-}" != --keep-agent ]] || keep=yes
	require_server "$server"
	local agent=/var/lib/spire/agent
	if [[ "$keep" == no ]]; then
		vm_ssh "$server" "sudo lxc exec $src --project default -- sh -c 'systemctl stop spire-agent && mv $agent $agent.moved'" ||
			die "$server: cannot move the agent's data aside in $src"
	fi
	info "$server: copying $src to $dst"
	local status=0
	vm_ssh "$server" "sudo lxc copy $src $dst --project default" || status=$?
	if [[ "$keep" == no ]]; then
		vm_ssh "$server" "sudo lxc exec $src --project default -- sh -c 'mv $agent.moved $agent && systemctl start spire-agent'" ||
			die "$server: cannot restore the agent's data in $src"
	fi
	((status == 0)) || die "$server: cannot copy $src (a running container is needed)"
	# the grants come once the copy exists and before it starts
	# shellcheck disable=SC2046 # one entitlement per word
	spire_permissions "$server" add "$dst" $(spire_entitlements) || die "$server: cannot grant lab-spire access to $dst"
	vm_lxc "$server" start "$dst" --project default >/dev/null || die "$server: cannot start $dst"
}
