# LXD SPIRE node attestation — lab test environment

Oct 11, 2026 · @Andrea Funtò · **Draft for review**

## Overview

This spec defines the lab: a scripted, disposable test environment with real LXD servers on libvirt VMs. It serves the whole project, in two **scenarios** that share the same LXD servers:

- **`probe`**: `lxd-probe` runs its gate runbooks (probe spec *Testing → Gate runbooks*) and records the fixtures that the fake LXD API replays (gate L15).
- **`plugins`**: `lxd-server-plugin` runs in a real SPIRE Server, `lxd-agent-plugin` in SPIRE Agents inside LXD instances, and instances attest end to end with each proof mode (attestor spec *Testing → Tests → End to end*, NFR-05).

A scenario is chosen per command (`--scenario`) or by default in the settings; it decides which VMs exist, what `deploy` installs and which acceptance tests run. A set of acceptance tests drives the lab on demand.

The unit and integration tests run against `test/fakelxd`, which until Lab 0 only encodes assumptions (PRT-10). What only a real LXD shows is exactly what the validation gates ask:

- the API itself: headers, envelopes, ETag and `If-Match` semantics, error codes, extensions per version (L1, L8, L14, L15);
- the guest side: what devLXD exposes, who can open its socket, how idmaps look from inside (L1, L3, L4);
- the lifecycle: identity keys across copy, move, restore and import, clustering, VMs and `lxd-agent` (L6, L7, L11);
- the authorization model: entitlements, restricted projects, bearer identities (L9, L13).

and, for the plugins, what no fake can show:

- a real SPIRE Server and Agent driving the challenge/response exchange, with the plugins started from their pinned checksums;
- the proofs against real instances: the file the agent writes and the server pulls (`file_pull`), the key the server writes and the agent reads through devLXD (`config_push`), the cleanup, and the agent SVID issued with the expected SPIFFE ID and selectors.

The probe spec's *Operational guidance → Lab setup* gives an indicative list of `lxc` commands, and the attestor spec names an "end-to-end lab" without defining it. This spec replaces the first and defines the second.

**Portability**: the lab runs on any Linux x86-64 machine with KVM, nested virtualization and enough resources. Every path, address, size and version is a setting with a default; a preflight check verifies the host, installs the missing software after confirmation, and refuses to start with a clear explanation when something cannot be fixed automatically. It follows the openstack-spiffe lab (`test/lab/` in that repository) in structure and conventions, so that one operator can run both; the two labs share no files and can be defined on the same host at the same time.

**Non-goals**:
- Running in CI. A bring-up takes about 15 minutes and needs nested virtualization.
- Deciding gates. The lab produces evidence; the operator records each decision with `lxd-probe util note --decision` (probe spec PRU-07, PRU-08).
- LXD from distribution packages (deb), Windows guests, arm64 hosts, LXD on the lab host itself.
- Load, performance or soak testing beyond what a gate runbook measures (L2 propagation delay, L14 race).

## Architecture

All VMs run on the lab host's libvirt (`qemu:///system`) on a dedicated NAT network, `lxd-lab`. In the `probe` scenario the lab host is the probe's server environment: `lxd-probe server` runs there and talks to each LXD over HTTPS, exactly as the server plugin does from `spire` in the `plugins` scenario. libvirt domains, the network and the storage pool are prefixed `lxd-lab`, distinct from openstack-spiffe's `spiffe-lab`.

| VM | OS (default) | Role | Scenarios | Default size (vCPU / RAM / disk) |
| --- | --- | --- | --- | --- |
| `lxd5` | Ubuntu 24.04 LTS | LXD 5.21 LTS (snap, channel `5.21/stable`), standalone | both | 4 / 8 GiB / 60 GiB |
| `lxd6` | Ubuntu 24.04 LTS | LXD current 6.x (snap, channel `latest/stable`), standalone; target of `lxd5`'s backup imports (L6) | both | 4 / 8 GiB / 60 GiB |
| `node1`–`node3` | Ubuntu 24.04 LTS | Three-node LXD cluster on the 6.x channel (L11, and clustered attestation); built only with `LAB_CLUSTER=yes` | both | 2 / 4 GiB / 40 GiB each |
| `spire` | Ubuntu 24.04 LTS | SPIRE Server with `lxd-server-plugin` (deb package), attesting through every LXD server of the lab; artifact server for the guests | `plugins` | 2 / 3 GiB / 20 GiB |

The LXD VMs are the same in both scenarios, and so are their baseline instances and identities: the `plugins` scenario adds to the `probe` scenario's lab, it does not replace it. `up --scenario plugins` on a `probe` lab only adds `spire` and the `spire` identity; `down` is the only command that removes them.

Each VM runs with the host's CPU model (`host-passthrough`) and nested KVM, since LXD runs virtual machines inside it. Each LXD server uses a ZFS pool on a loop file (`LAB_LXD_STORAGE`, `zfs` or `dir`), its own `lxdbr0` bridge with NAT for its instances, and listens on `:8443` on the lab network. The lab does not need to reach the instances directly: everything goes through the LXD API or through `lxc` on the LXD VM.

**Snap refreshes are held** on every LXD VM (`snap refresh --hold lxd`, and `snap refresh --hold` for the automatic refreshes of every other snap), so that the LXD version cannot change during a session, and LXD's base snap is not refreshed under it: an automatic refresh of `core26` during `up` once made `lxc` fail with "cannot locate base snap". `lab.sh refresh` updates the channels deliberately and records the new revisions.

**Baseline instances** (the probe spec's lab matrix, *Operational guidance → Lab matrix*), created by `up` on `lxd5` and `lxd6` in the `default` project:

| Instance | Kind | Image (default) | Configuration |
| --- | --- | --- | --- |
| `c-unpriv` | container | `ubuntu:24.04` | default (unprivileged) |
| `c-priv` | container | `ubuntu:24.04` | `security.privileged=true`, `security.nesting=true` |
| `c-iso` | container | `ubuntu:24.04` | `security.idmap.isolated=true` |
| `c-raw` | container | `ubuntu:24.04` | `raw.idmap="both 1000 1000"` |
| `v-ubuntu` | VM | `ubuntu:24.04` | 1 vCPU, 1 GiB |
| `v-debian` | VM | `images:debian/12/cloud` | 1 vCPU, 1 GiB |
| `v-alpine` | VM | `images:alpine/3.24/cloud` | 1 vCPU, 1 GiB, secure boot off |

Ubuntu comes from Canonical's `ubuntu:` remote: `images:` publishes only Ubuntu desktop images. `c-priv` needs nesting: under LXD's AppArmor profile, a privileged Ubuntu 24.04 container cannot set up the mount namespaces of systemd's services, and boots without network; it stays fully privileged (identity uid map). Alpine's images are not signed, so `v-alpine` boots without secure boot, through `security.secureboot=false` on LXD 5.21 and `boot.mode=uefi-nosecureboot` on LXD 6 (API extension `instance_boot_mode`).

The baseline instances belong to the `probe` scenario's runbooks; the `plugins` scenario boots its own guests per test (*Acceptance tests*). Each guest gets, through cloud-init, an unprivileged user `probe` with uid 1000 (L4 uses uids 0, 1000 and 65534), and no default user; the user Alpine's image ships with uid 1000 is removed first. A **restricted project**, `restricted` (`restricted=true`), holds one `c-unpriv` of its own (L9). It shares the default project's images, but has profiles of its own (LXD restricts no project without them): its `default` profile has a root disk on the `default` pool and a NIC on a bridge of its own, `lxdbr1`, since LXD keeps instance DNS names unique per network and its `c-unpriv` shares its name with the default project's. On the cluster, `c-unpriv` runs on `node1` and `v-ubuntu` on `node2`, so that requests through `node1` reach an instance on another member (L11). Instances a runbook needs only for itself (L6 copies, L12 shared disks) are created and deleted by that runbook, not by `up`.

**Identities**: the bring-up creates five client certificates (ECDSA P-256), one per probe identity of the lab matrix, and trusts them on every LXD server:

| Identity | Grants (fine-grained authorization) |
| --- | --- |
| `read` | `can_view` on each baseline instance of `default` and `restricted` |
| `files` | `read` + `can_access_files` |
| `edit` | `read` + `can_edit` |
| `full` | server `admin` |
| `outsider` | `can_view` on each baseline instance of `default` only |

Grants go through LXD groups (`lab-read`, `lab-files`, `lab-edit`, `lab-full`, `lab-outsider`). `outsider` is a trusted identity scoped out of `restricted`: LXD answers it there with 404 Not Found, exactly as for an instance that does not exist, while an untrusted certificate gets 403 Forbidden (LXD 5.21 and 6.9 alike). The four other identities keep both projects, so that L9 repeats the whole matrix inside `restricted`. They are per instance, since LXD has no project-wide entitlement for file access, plus `can_view` on each project, without which LXD grants nothing in it; runbooks that create instances grant them the same way (`lab_grant`). The lab needs fine-grained TLS identities (the `access_management_tls` extension, present in both 5.21 and 6.x): `up` fails on a server without them. When a server offers bearer identities for its remote API (`auth_bearer`, LXD 6), the bring-up also creates a bearer identity in the `lab-files` group and stores its token (L13). `env.json` records each server's authorization model and whether it has bearer identities.

In the `plugins` scenario, a sixth certificate, `spire`, is SPIRE Server's identity on every LXD server (attestor spec *LXD integration → Least privilege*): its group, `lab-spire`, grants `can_view` and `can_access_files` on the instances of `default` (`file_pull`), plus `can_edit` when `config_push` is enabled (`LAB_SPIRE_MODES`). It is never one of the probe's identities, so that the end-to-end tests run with exactly the privileges the attestor spec prescribes. Keys and tokens never leave the lab's state directory.

**Deployment per scenario**:

- `probe`: `deploy` builds `lxd-probe` on the lab host (`CGO_ENABLED=0`, linux/amd64, `go build` with the version from `git describe`, recorded in `env.json` with the binary's SHA-256) and pushes it into every baseline instance as `/usr/local/bin/lxd-probe` (mode 0755, owned by root) with `lxc file push` on the LXD VM. The guest environment runs there; its evidence goes to `/var/lib/lxd-probe/evidence` when it runs as root and to `/var/lib/lxd-probe/evidence-1000` as the `probe` user (each mode 0700, owned by its user), and is pulled back with `lab.sh pull`. The snapshot does not hold the binary, so `deploy` follows `reset`.
- `plugins`: `deploy` builds the packages with `make snapshot` (goreleaser) on the lab host and verifies them against the checksums file, as an operator would; nothing is compiled in the VMs. On `spire` it installs the pinned SPIRE release (verified) and `lxd-server-plugin` (deb), configured with the trust domain `lxd.lab`, the plugin's checksum (`make checksum`), one LXD server entry per lab server with its endpoint, pin and the `spire` identity, and the proof modes of `LAB_SPIRE_MODES`; it starts SPIRE Server and waits for its health check. It then publishes what the guests install on `http://spire.lab:8080/` (the SPIRE tarball, the agent plugin's deb and rpm and its linux/amd64 archive for guests without a package manager we package for), and writes the guests' cloud-init configurations (`guest/<image>.yaml` in the state directory): they install SPIRE Agent and `lxd-agent-plugin`, with SPIRE's trust bundle embedded, the plugin's checksum, the proof directory, and a SPIRE Agent unit running as root (`file_pull` requires it, attestor spec FR-A7).

**Guest network path** (`plugins`): guests reach `spire.lab:8081` (SPIRE Server) and `:8080` (artifacts) through their server's `lxdbr0` NAT and the lab network; `lxdbr0`'s DNS forwards to the LXD VM's resolver, which resolves the `.lab` names. SPIRE Server reaches the LXD servers on `:8443`; nothing reaches the guests directly.

## Settings

`test/lab/lab.env` holds every setting with its default, and documents it. A git-ignored `test/lab/lab.local.env` overrides it for one machine, and environment variables override both. The settings are:

- **Versions**: `LAB_UBUNTU_RELEASE` (default `24.04`) for the VMs; `LAB_LXD5_CHANNEL` (default `5.21/stable`) and `LAB_LXD6_CHANNEL` (default `latest/stable`), with optional `LAB_LXD5_REVISION` and `LAB_LXD6_REVISION` to pin a snap revision; the guest images (`LAB_IMAGE_UBUNTU`, `LAB_IMAGE_DEBIAN`, `LAB_IMAGE_ALPINE`), as `REMOTE:ALIAS`; `LAB_UBUNTU_IMAGE_URL` for the VMs' cloud image, derived from the release by default and overridable for mirrors.
- **Scenarios**: `LAB_SCENARIOS` (default `probe`; `probe`, `plugins`, or `probe plugins`) is the default of every command's `--scenario`. The default becomes `probe plugins` once the plugins attest.
- **Plugins** (`plugins` scenario): `LAB_SPIRE_VERSION` and `LAB_SPIRE_SHA256`, empty by default, meaning the most recent SPIRE release compatible with the `spire-plugin-sdk` version in `go.mod`, resolved at `up` with its checksum from the release's checksums file (a pinned version needs its SHA-256); `LAB_TRUST_DOMAIN` (default `lxd.lab`); `LAB_SPIRE_MODES` (default `file_pull config_push`).
- **Topology**: `LAB_CLUSTER` (default `no`) adds the three cluster nodes; `LAB_LXD_STORAGE` (default `zfs`).
- **Network**: `LAB_SUBNET` (default `10.251.0.0/24`), `LAB_NETWORK` (default `lxd-lab`), and the host part of each VM's address: the lab host at `.1`, `lxd5` at `.10`, `lxd6` at `.11`, `node1`–`node3` at `.21`–`.23`, `spire` at `.30`. libvirt's DNS resolves `lxd5.lab` and the other names, and its DHCP gives each VM a fixed address from a fixed MAC.
- **Sizes**: vCPUs, RAM and disk of each VM, and of the guest VMs inside LXD. They apply when a VM is created; `up` warns when an existing VM differs from the settings.
- **Locations**: `LAB_STATE_DIR` (default `test/lab/.state`, git-ignored: PKI, SSH key, tokens, `env.json`), `LAB_EVIDENCE_DIR` (default `evidence` in the repository, git-ignored: the working runs of `lab.sh probe`; sanitized evidence is committed by hand under `docs/validation/<run_id>/`), `LAB_CACHE_DIR` (default `${XDG_CACHE_HOME:-~/.cache}/lxd-spiffe-lab`: cloud images, kept across `down`), `LAB_POOL` and `LAB_POOL_DIR` (default a libvirt pool `lxd-lab` under `/var/lib/libvirt/images/lxd-lab`).
- **Behaviour**: `LAB_ASSUME_YES` answers yes to the preflight's installation prompts.

`up` records every resolved value in `env.json`: the scenarios built, the VMs' image checksum, the SPIRE version and checksum, each LXD server's snap channel, revision and version (`GET /1.0` `environment.server_version`), its certificate fingerprint (the pin), its `api_extensions` hash, its authorization model, and the image fingerprints of the baseline instances. `lab.sh status` shows them; every probe record already carries the server version and extension hash (PRE-02), so evidence and fixtures can be traced to the exact lab.

## Preflight

`lab.sh preflight [--no-install]` checks that the machine can run the lab; `up` runs it first. Each check prints `ok`, `warn` or `fail` with what to do; all checks run, and any `fail` makes it exit non-zero. It is the openstack-spiffe lab's preflight with the lab's own figures:

- **Operating system**: Linux on x86-64; Debian/Ubuntu (`apt`) or Fedora/RHEL-like (`dnf`) hosts get missing software installed, others a list to install by hand; systemd running.
- **Virtualization**: `vmx`/`svm` and `/dev/kvm`; nested virtualization enabled (`kvm_intel`/`kvm_amd` `nested`), with instructions to enable it persistently when off (preflight never reloads kernel modules).
- **Resources**, computed from the configured sizes: vCPUs against logical CPUs (`warn` above 1.5× overcommit, `fail` above 3×); RAM plus 4 GiB for the host against the memory available now, counting only the lab's VMs not already running; disk for the VMs' actual use and their snapshots, plus 20%, under `LAB_POOL_DIR`, and about 2 GiB of downloads under `LAB_CACHE_DIR`. With the defaults (`probe`): 8 vCPUs, 16 GiB of RAM, about 80 GiB of disk; `plugins` adds 2 vCPUs and 3 GiB; `LAB_CLUSTER=yes` adds 6 vCPUs and 12 GiB.
- **Software** (installed after one confirmation): libvirt, `virsh`, `virt-install`, `qemu-img`, QEMU/KVM; `cloud-localds` or `xorriso`/`genisoimage`; `ssh`, `ssh-keygen`, `openssl`, `curl`, `jq`, `git`, `make`; Go at the version `go.mod` requires; for `plugins`, goreleaser v2.
- **Permissions**: the user manages `qemu:///system` (the `libvirt` group, or polkit); `sudo` is used for installation only.
- **Network**: `LAB_SUBNET` overlaps no host route and no other libvirt network, and `LAB_NETWORK` is absent or the lab's own; the Ubuntu cloud image host, the Snap Store and `images.lxd.canonical.com` are reachable over HTTPS, and for `plugins` SPIRE's releases.

## Lifecycle

Everything lives in `test/lab/`. `lab.sh` is the entry point; each command is idempotent and prints what it does. `preflight`, `up`, `deploy`, `reset` and `test` take `--scenario probe|plugins|all` (default `LAB_SCENARIOS`); `all` means both. A command never removes what another scenario built.

- `preflight [--no-install]`: see above.
- `up`: runs `preflight`, generates the lab PKI (the five client identities), creates the pool, network and VMs from the Ubuntu cloud image with cloud-init, installs LXD from the configured channels and holds refreshes, initializes each server with a preseed (storage, `lxdbr0`, `core.https_address` on the lab network), forms the cluster when `LAB_CLUSTER=yes`, then creates the identities, groups, the `restricted` project and the baseline instances, starts any that is stopped, and waits until every instance is running (VMs: until `lxd-agent` answers) with its `probe` user. With `plugins` it also creates `spire` and the `spire` identity. It then records `env.json`, takes the snapshot on the first success, and runs `deploy` for the same scenarios. A second `up` on a complete lab only verifies it.
- `snapshot` / `reset`: saves the VMs' disks as the lab's baseline, or returns every VM to it. Safety comes before speed: the VMs run virtual machines of their own, whose nested state a memory snapshot may not restore faithfully, so `snapshot` stops the baseline instances, shuts the VMs down, takes an internal disk-only snapshot of each, and boots them again; `reset` powers the VMs off, reverts them and boots them cold. Both then check that every server answers with its recorded pin and version, and start the baseline instances themselves (LXD does not always start again, after a host shutdown, a VM that powered off quickly), waiting until they run. This is the normal start of a session, and what a runbook that changes instances (L6, L11, L12) runs before and after.
- `deploy`: installs what the scenarios need (*Architecture → Deployment per scenario*). `probe`: builds `lxd-probe`, pushes it into every baseline instance, and writes, for each server and identity, an environment file `probe/<server>-<identity>.env` (mode 0600) in the state directory with the `LXD_PROBE_*` variables (endpoint, pin, client certificate and key or, for `<server>-bearer.env` where the server has bearer identities, the token file; project `default`; evidence directory `LAB_EVIDENCE_DIR`; probe spec PRN-32). `plugins`: builds and verifies the packages, installs SPIRE Server and the server plugin on `spire`, publishes the guest artifacts and writes the guests' cloud-init. `deploy` after `reset` is the inner loop after a code change.
- `guest-up <server> <name> [--image <image>]` (`plugins`): launches a guest with the SPIRE Agent cloud-init, for manual end-to-end checks; `guest-down` deletes it.
- `probe <server> [--as <identity>] -- <args>`: runs `lxd-probe <args>` on the lab host with that server's and identity's environment file (default identity `full`; `bearer` where the server has one), for manual runbook steps; flags in `<args>` override the file. `guest <server> <instance> [--project <project>] [--user <uid>] -- <args>`: runs `lxd-probe <args>` inside an instance with `lxc exec`, as root unless `--user` gives a uid (the gid is the same number). Both pass `LXD_PROBE_RUN_ID`, `LXD_PROBE_GATE`, `LXD_PROBE_CELL`, `LXD_PROBE_LOG_LEVEL` and `LXD_PROBE_RECORD_HOST` through from the caller, so that one run spans both environments.
- `token <server> [--expiry <durations>]`: on a server with bearer identities, creates a second bearer identity, `lab-bearer-short` in `lab-files`, on first use, and issues it a token (`lxc auth identity token issue --expiry`, default `2M`), which revokes the one issued before. The token goes to `tokens/<server>-bearer-short.token` and its environment file to `probe/<server>-bearer-short.env`, so that `probe --as bearer-short` uses it. `lab-bearer`'s long-lived token is never touched (L13).
- `pull <server> <instance> [--project <project>] [--user <uid>] <run_id>`: copies the run `<run_id>` of the guest environment, written by `lab.sh guest` as root or as `<uid>`, from the instance into `LAB_EVIDENCE_DIR/<run_id>/`.
- `refresh`: moves each LXD server whose revision the settings do not pin to the newest revision of its channel. It shows the changes and asks for confirmation (LXD cannot go back to an older revision), lifts the hold, refreshes, holds again, records the server in `env.json` anew, and then offers to replace the snapshot. Servers already up to date, and pinned ones, are left alone.
- `test [-run REGEX] [--collect] [--record-host]`: runs the acceptance tests (`go test -tags lab ./test/lab/acceptance`). Gate sessions write their evidence to `LAB_EVIDENCE_DIR` only; `--collect` also copies it into the repository (*Acceptance tests*). The evidence records this machine's host name as `redacted` unless `--record-host` is given (probe spec PRE-03); `probe` and `guest` pass `LXD_PROBE_RECORD_HOST` through.
- `status`, `ssh <vm>`, `logs <vm> [unit]`: inspection; `status` shows the VMs, the servers' versions, pins and authorization models, and the baseline instances.
- `down`: destroys the VMs with their snapshots, the network, the storage pool and the state directory, after confirmation. The download cache stays.

Downloads are verified as an operator would: the Ubuntu cloud image against its `SHA256SUMS`; snaps by snapd's own assertions; LXD images by LXD's simplestreams signatures.

## Acceptance tests

The acceptance tests are Go tests in `test/lab/acceptance`, behind the `lab` build tag, so that `go test ./...` never runs them. They read `env.json`, reach the VMs with the system `ssh` client, and run the `lxd-probe` that `deploy` built; they add no dependencies to `go.mod`.

There are three kinds:

- **Lab checks** pass or fail: the lab is what this spec says it is.
- **End-to-end tests** (`plugins`) pass or fail like the openstack-spiffe lab's: each boots the guests it needs from the cloud-init `deploy` wrote, checks attestation, and deletes them, so tests are independent.
- **Gate sessions** (`probe`) run a gate's runbook (probe spec *Testing → Gate runbooks*) over its cells, one run per server (a run directory records one endpoint), in `LAB_EVIDENCE_DIR`. A run directory is a working one: its `aliases.json` maps aliases back to MAC addresses and names (PRE-09). What a session checks with `util sanitize --check` is the run's sanitized copy (`util sanitize --out`, without `aliases.json`), and with `lab.sh test --collect` that copy goes to `docs/validation/<run_id>/` and its fixtures replace the session's own under `test/fixtures/<lxd-version>/`: S0-1's top-level files, or a session's subdirectory (`devlxd/`, `files/`), each leaving the others alone. Fixtures recorded as an untrusted client land under `unknown` (LXD tells untrusted clients no version) and join the server's version. Sessions fail only when the probe itself fails (exit codes 1, 3, 5, 6 or 7, or a missing record) or the evidence is not sanitized: an `unexpected` outcome is evidence, not a failure. They never decide a gate; they end by printing the decision rules of the gate next to the records, for the operator's `util note --decision`.

`lab.sh test` runs the tests of the selected scenarios: the lab checks of every built scenario, then the gate sessions or the end-to-end tests. Tests run one after another; each session starts from `reset` when it changes instances. The tests of later chunks are written together with those chunks; until then they do not exist, rather than being skipped.

| ID | Test | Checks | Chunk |
| --- | --- | --- | --- |
| LAB-1 | Servers | Every server answers `server info` with its recorded pin, version and extension hash; LXD refuses TLS 1.2 or the finding is recorded (Q3) | 4.5 |
| LAB-2 | Identities | `server whoami` as each identity (and the bearer identity where there is one) on each server reports exactly its own group; `server instance show c-unpriv` as `read` succeeds; in `restricted` as `outsider` it fails with 404 under `--expect-hidden`, the same answer as for a missing instance, and with an untrusted certificate with 403 under `--expect-denied` | 4.5 |
| LAB-3 | Baseline | Every baseline instance of each server is running, with the type, image, configuration and bridge of the table above, and the `lxd-probe` that `deploy` built (its SHA-256) | 4.5 |
| S0-1 | Lab 0, server half | As `full`, `server info --probe-tls12` (L8), `whoami` and `instance show` for every baseline instance on `lxd5` and `lxd6`, with `--record`, plus the provoked 403 (untrusted certificate), 404 (missing instance) and hidden 404 (`outsider` in `restricted`); the 412 of a stale ETag waits for `server config set` (stage 1). The sanitized copy is checked; the fixtures are replayed by `test/fixtures`'s Go test, which decodes each one into the LXD type the probe uses and stands in for `util fixtures verify` until stage 2 | 4.5 |
| S0-2 | Lab 0, guest half | As root, `guest preflight` and `guest devlxd dump` in every baseline instance of `lxd5` and `lxd6`, one run per server and instance, with `--record`; L3 evidence. `lab.sh pull` brings each run back; with `--collect`, the sanitized runs go to `docs/validation/` and the devLXD fixtures to `test/fixtures/<lxd-version>/devlxd/<instance>/`, which `test/fixtures`'s replay test decodes too. What other uids can do (L4) is left to `guest devlxd access` | 5 |
| G-L4 | Gate L4 | As root in `c-unpriv`, `c-priv` and `v-ubuntu` of each server: `guest preflight` and `guest devlxd access --as-uid 0,1000,65534 --path /1.0 --path /1.0/config/cloud-init.user-data`; who can use devLXD, and read cloud-init data through it | 6 |
| G-L1 | Gate L1 | In `c-unpriv`, `c-priv`, `c-iso`, `c-raw`, `v-ubuntu`, `v-debian` and `v-alpine` of each server: as root, `guest preflight`, `guest idmap show`, `guest file layout` and `guest file put /run/lxd-probe/proof`; then as `files`, `server instance show` and `server file stat`, `get`, `head` and `dir` on the proof and each layout item, with `--record`, and once as `read` for the 403. One run per server and instance, guest and server environments in it; the files-API fixtures go to `test/fixtures/<lxd-version>/files/<instance>/`. Step 3 (`util idmap`) runs offline on the committed records with chunk 9 | 7 |
| G-L14 | Gate L14 | As `edit` in `c-unpriv` and `v-ubuntu` of each server, the L14 runbook on `user.lxd-probe.*` keys: `config set` with `patch` and `auto` (waiting), `patch` and `put` with a stale ETag, `config unset` with each method, `config race` with 8 writers; once as `files`, a `set` for the 403. Each run removes its keys at the end, so no reset is needed; with `--record`, its fixtures (`PATCH`, `PUT`, 412, operations) go to `test/fixtures/<lxd-version>/config/<instance>/`, for the attestor's `config_push` tests | 8 |
| G-L2 | Gate L2 | In `c-unpriv` and `v-ubuntu` of each server: `guest devlxd watch` of a `user.lxd-probe.*` key for 30 s while the server (as `edit`) writes it five times about 2 s apart; each write's delay until the guest sees it (p50, p95), whether events arrive, and the estimated clock offset with its error bound. The key is removed afterwards | 9 |
| G-L1 step 3 | Gate L1, offline | `util idmap check` on each committed G-L1 run's `instance show` and `preflight` records; the records join that run as its `util` environment | 9 |
| G-L9 | Gate L9 | On each server, as each probe identity (`read`, `files`, `edit`, `full`, `outsider`, and `bearer` where there is one): `server whoami`, and `server permissions` on `c-unpriv` in `default` and in `restricted` | 10 |
| Decisions | All gates | The operator's gate decisions, each a `util note --decision` naming its evidence runs, in one run (`<timestamp>-decisions`) under `docs/validation/`; `util report` shows them with their gates | 10 |
| G-L13 | Gate L13 | On each server with bearer identities: `server whoami` and `server bearer check` with `lab-bearer`'s token; a token from `lab.sh token` for 2 minutes, checked with `--wait-expiry --max-wait 5m`; then two tokens issued one after the other, the first checked with `--expect-denied` (revocation). A server without bearer identities gets a `util note` saying so | 11 |
| G-L12 | Gate L12 | On each server, on copies of `c-unpriv` (A, B), `c-priv` and `v-ubuntu` made for the session, the L12 cells (probe spec runbook): a host directory over the proof directory, hot-plugged and then present at boot (systemd mounts its own `/run` over it at boot); a custom volume; the same through a profile; a disk below the proof directory; a disk over `/run`; and a volume shared by the VM and both containers. In each: `guest file put` of the proof in a writer, `server file stat` of it in A and `server instance show` of both as `full` (the session's instances are granted to no other identity), then `util overlap --proof-dir /run/lxd-probe` on those records. A cell LXD refuses to build gets a `util note` with the error; nested LXD gets a note that it is out of scope. The session deletes what it creates | 11 |
| G-L7 | Gate L7 | On each server, for `v-ubuntu`, `v-debian` and `v-alpine`: `server file stat /etc/hostname` as `files` once while stopped (Q2); then three starts (a start, then two restarts), each followed at once by `server file stat /etc/hostname --wait 5m`, which times the files API from the start; then `guest preflight`. The VMs are running again at the end | 11 |
| G-L14 idle | Gate L14, Q5 | `server instance watch --for 5m` as `read` on idle `c-unpriv` and `v-ubuntu` of both servers at once; the ETag changes are the spurious-412 rate | 11 |
| G-L6 | Gate L6 | From `reset`: on `lxd5`, copies of `c-unpriv` and `v-ubuntu` with the stand-in key, then the L6 operations (probe spec runbook) with `lxd6` as the other server, through an `lxc` remote added for the session (it trusts `lxd5` on `lxd6`); `instance watch` on each copy, `instance show` before and after, `util diff`, `config get` of the marker, and a `util note` per step with the `lxc` command. Ends with `reset`, which also removes the remote's trust. The probe spec's lab matrix names a second standalone server for the import; `lxd6` serves as one | 11 |
| G-L11 | Gate L11 | The L11 runbook; needs `LAB_CLUSTER=yes`, deferred until the cluster is built | — |
| R-1, R-2 | Rehearsals | `server rehearse file-pull` and `config-push` against each instance class | 12 |
| LAB-4 | SPIRE (`plugins`) | SPIRE Server is healthy with the plugin loaded from its checksum; `spire` reaches every LXD server with the `spire` identity, whose `whoami` shows exactly the `lab-spire` grants; a guest resolves and reaches `spire.lab` | plugins (e) |
| E2E-1 | Attestation | On `lxd5` and `lxd6`, an unprivileged container, a privileged container and an Ubuntu VM attest with `file_pull`; `spire-server agent list` shows the expected SPIFFE ID (with `server_id`, FR-S7) and selectors | plugins (e) |
| E2E-2 | `config_push` | The same with `config_push`; the challenge key is gone from the instance after attestation | plugins (e) |
| E2E-3 | Refusals | Attestation fails with the uniform error for an instance outside the allowed projects, a claim naming another instance, and an identity lacking `can_access_files` | plugins (e) |
| E2E-4 | Re-attestation and identity change | An agent restarted with its data wiped attests again; a copy of an attested instance gets a different SPIFFE ID or is refused, as gate L6 decided | plugins (e) |
| E2E-5 | Cluster | With `LAB_CLUSTER=yes`, an instance on `node2` attests through `node1` | plugins (e) |

Every failing check prints what it observed (exit code, record path, observations), so that a failure can be diagnosed without re-running it.

## Implementation plan for chunk 4.5

Chunk 4.5 builds the lab with the `probe` scenario and without the cluster (`LAB_CLUSTER=yes` is refused as "not built yet" until the stage-2 chunk that runs L11 adds it), and the `--scenario` switch with `plugins` refused as "not built yet". The `plugins` scenario is built by the plugins' end-to-end step (attestor spec *Implementation instructions*, step 7 (e)), when there is something to attest with; this spec defines it now so that 4.5 lays out the VMs, identities, network and settings it will need.

1. `lab.env` and `lab.sh preflight`, adapted from the openstack-spiffe lab. Done when preflight reports correctly on this machine, and with `--no-install` in a clean Ubuntu and a clean Fedora container.
2. `up` for `lxd5` and `lxd6` (pool, network, VMs, LXD from the snap channels with refreshes held, preseed), `down`, `status`, `ssh`, `logs`. Done when both servers answer `lxc info` and `GET /1.0` over the lab network.
3. Lab PKI, identities and groups, the `restricted` project, the baseline instances, `env.json`, `snapshot` and `reset`.
4. `deploy`, `probe`, `guest`, `refresh`. Only `refresh`'s "up to date" and "pinned" paths could be verified when it was built: both channels were at their newest revision, and LXD cannot be downgraded to fake an older one. The refresh itself is verified the first time a channel moves.
5. The acceptance harness and LAB-1 to LAB-3, then S0-1: the server half of Lab 0, whose fixtures are committed under `test/fixtures/<lxd-version>/`.
6. Probe spec: *Operational guidance → Lab setup* points to this spec; the delivery plan's Lab 0 and Lab 1 checkpoints name S0-1, S0-2 and the stage-1 sessions. README: a short "Lab" section.

## Resolved questions

- Storage: ZFS on a loop file by default; `dir` stays available through `LAB_LXD_STORAGE`.
- Cluster: built by the stage-2 chunk that runs gate L11, not by 4.5 (step 6 above).
- LXD 6.x: the `latest/stable` channel with refreshes held; it moves only on `lab.sh refresh`, and `LAB_LXD6_REVISION` pins a revision.
- `plugins` scenario: built with the plugins' end-to-end step (e), not by 4.5.

Answered by the bring-up (step 3):

- LXD 5.21 (5.21.8) offers fine-grained TLS identities (`access_management_tls`), so the identities differ on both servers and the legacy fallback is dropped. Bearer identities exist only on LXD 6.
- Alpine 3.20 is no longer published; `images:alpine/3.24/cloud` boots as a VM (without secure boot) and its `lxd-agent` answers, so L7 covers Alpine.
