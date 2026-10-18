### Gate runbooks

Each runbook gives the question, the matrix cells, the commands, the evidence, and the **decision rule**: which amendment of the attestor spec follows from each outcome. Commands are indicative and use shorthand (`$S` = `lxd-probe server`, `$G` = `lxd-probe guest`; the connection flags come from the `LXD_PROBE_*` environment, PRN-32).

#### L1 — Files API metadata and owner representation

**Question:** What does the files API return for owner, mode and type, in containers (unprivileged, privileged, `security.idmap.isolated`, `raw.idmap`) and VMs? Which header names are used? Does LXD report the in-container uid or the host-shifted uid? Do symlinks and directories show up distinguishably?

**Cells:** unpriv-container, priv-container, isolated-container, raw-idmap-container, VM (Ubuntu, Debian, Alpine).

**Procedure:**
1. In the guest: `$G preflight`, `$G idmap show`, `$G file layout`, `$G file put /run/lxd-probe/proof --mode 0600`.
2. On the server, as `files` (the least privilege the attestor uses for `file_pull`): `$S instance show NAME`, `$S file stat NAME /run/lxd-probe/proof`, the same for each layout item, `file get`, `file head` and `file dir` on them, with `--record`; and once as `read`, to record the 403 of an identity without `can_access_files`.
3. Offline, on the committed records: `lxd-probe util idmap check <instance record> <preflight record>`, `lxd-probe util idmap translate ...`; their records join each cell's run as its `util` environment.

**Decision rules:**
- Header names found → update attestor spec *Server-side plugin → LXD integration → Minimal API contract* with the exact names and check that the SDK's `GetInstanceFile` reads the same headers; remove `[V:L1]` tag.
- Owner reported as in-container uid (0) → attestor spec *Mode `file_pull`* step 6 uses the simple rule; the *UID translation* becomes a fallback for LXD versions that differ.
- Owner reported as host uid → attestor spec *UID translation* is normative; record the keys used and the matching test vectors.
- idmap keys cross-check mismatches `/proc/self/uid_map` → `volatile.idmap.current` is not usable; choose another source or restrict `file_pull` to privileged containers and VMs.
- Symlink/directory type not distinguishable → FR-S5 and attestor spec *Mode `file_pull`* must additionally require a type check by another call (or reject).

#### L2 — Live config update and propagation

**Question:** Is `PATCH` of `user.*` on a running instance accepted without restart, how fast is it visible through devLXD, is an event emitted?

**Commands:** `$G devlxd watch user.lxd-probe.t1 --for 30s` while `$S config set NAME user.lxd-probe.t1 vN --method patch --wait`, five times about 2 s apart, in a container and a VM. The delay of each write is the guest's change timestamp minus the write's completion, both UTC; the offset between the two clocks is estimated per cell (the guest's clock read through `lxc exec`, with half the round trip as error bound) and recorded with the result.

**Decision:** propagation delay p95 sets the default `proof_timeout` floor; absence of events fixes polling as normative in attestor spec *Mode `config_push`*; restart triggered → `config_push` is rejected as a mode.

#### L3 — devLXD metadata

**Command:** `$G devlxd dump`. **Decision:** identifies which fields (instance name/project) the guest can obtain on its own; updates the claim tables of attestor spec *Trust model → What claims can be made reliably* and the "what the agent may send" list.

#### L4 — Who can open `/dev/lxd/sock`

**Commands:** `$G preflight`, `$G devlxd access --as-uid 0,1000,65534 --path /1.0 --path /1.0/config/cloud-init.user-data` in an unprivileged container, a privileged container, and a VM. The second path answers whether a non-root process can read cloud-init data through devLXD (attestor spec *Alternatives → Keys delivered through cloud-init*). **Decision:** if non-root users can open the socket, `config_push` challenge data is readable by any local process → FR/SEC for `config_push` states that the challenge is not secret and that proof strength relies on the nonce binding only; add the corresponding threat entry. If only root can, record it as a control.

#### L5 — Withdrawn

Kept for reference stability. No runbook.

#### L6 — Identity stability (`volatile.uuid` and friends)

**Question:** Which of `volatile.uuid`, `volatile.uuid.generation`, `volatile.cloud-init.instance-id` change on: copy, move, rename, snapshot restore, backup export/import elsewhere, instance recovery?

**Procedure:** start `$S instance watch NAME --for 10m`; perform the operations with `lxc` and note each with `util note`. Use `util diff` between instance records before/after.

In the lab, the operations run on copies of `c-unpriv` and `v-ubuntu` made for the runbook, never on the baseline instances, and the runbook ends with `lab.sh reset`. Each copy carries a stand-in key before the first operation: a random, non-secret marker in `user.lxd-probe.l6-key` and the same marker in `cloud-init.user-data`, so that each step also shows whether a key in instance config travels with the result (attestor spec *Alternatives → Keys delivered through cloud-init*). The operations: local copy, snapshot and restore, rename, copy and move to the other server (through an `lxc` remote between the two servers, added for the runbook), export and import on the other server, export and import on the same server while the original still exists, and recovery (`lxd recover` of an instance whose database entry was removed; recorded as not testable with the reason if the lab cannot do it). For every step: `$S instance show` of the source and the result, before and after, `util diff` between them, and `$S config get` of the marker key.

**Decision:** confirms or amends the [V:L6] statements of attestor spec *Claims about identity* and *Claims about provenance and configuration*; a duplicate `volatile.uuid` across servers confirms that the SPIFFE ID must include `server_id` (FR-S7) and that the ambiguity rule of T7 applies; rollback-detection (`generation`) rules in FR-S10 are confirmed or adapted.

#### L7 — VMs and `lxd-agent`

**Cells:** Ubuntu, Debian, Alpine VMs, and two RHEL 9 rebuilds with SELinux enforcing, Rocky Linux and AlmaLinux (session-only instances in the lab); the probe does not run in Windows guests (PRN-22). **Commands:** `$G preflight` at boot (via a systemd unit or cloud-init runcmd, repeated to time the agent), `$S file stat` at increasing delays after start. In the lab: `$S file stat NAME /etc/hostname` once while the VM is stopped (Q2), then, three times per VM, a restart with `lxc` followed by `$S file stat NAME /etc/hostname --wait 5m`, which times the files API from the restart; then `$G preflight`. In every VM, a `util note` records how `lxd-agent` is started (its systemd unit or OpenRC service, which the SPIRE agent is to be ordered after) and, on the RHEL rebuilds, the SELinux mode and the AVC denials of the boot. The RHEL rebuilds are also run with the targeted SELinux policy enforcing, with and without the `lxd-agent` drop-in that the decision documents, and a root-owned 0600 proof under `/run/spire/lxd` is read through the files API. **Decision:** the agent-start-order documentation for operators; which image families are supported; Windows guests stay untested and unsupported.

#### L8 — Versions and extensions

**Command:** `$S info --probe-tls12` on each LXD server, which also learns whether TLS 1.2 is accepted (Q3); S0-1's records provide it (Lab 0). **Decision:** minimum LXD version list; the table of required extensions in attestor spec *API usage and required extensions* is confirmed (`projects`, `instance_generation_id`, ...); each additional extension found necessary is added.

#### L9 — Entitlements

**Procedure:** for each identity (read / files / edit / full): `$S whoami`, `$S permissions NAME`; plus the manual negative tests with `lxc` recorded as notes; repeat inside a **restricted project**.

**Decision:** minimal entitlement set per mode for attestor spec *Proof of co-location → Privilege cost of each mode*; whether the identity can see its own privileges (SEC-16 warning feasible or not).

#### L10 — Client coverage

Non-lab. The licence question is closed in attestor spec v0.4 (the SDK's `client` and `shared` packages are Apache-2.0). Evidence: the pinned SDK version (`go.mod`) recorded with `util note`; `lxd-probe` is the first consumer of the FR-S17 client, and `util report` lists the endpoints reached and unreached. **Decision:** confirm that every call of attestor spec *API usage and required extensions* is covered, by an SDK method or by `RawQuery`, and list the calls that need `RawQuery`.

#### L11 — Clusters

**Cells:** a 3-member cluster on LXD 5.21 LTS and one on 6.x, with an instance on member 1 and one on member 2 (in the lab: `c-unpriv` and `v-ubuntu`). **Procedure:** through each member in turn, `$S instance show` and `$S file stat` of both instances (every record names the member that answered, PRE-02; the `instance show` record also gives the instance's `location`), and `$S config set NAME user.lxd-probe.l11 vN --method patch --if-match auto` followed by `unset` (forwarded writes with `If-Match`); `$S instance list --filter "config.volatile.uuid eq <uuid>"` through a member that does not hold the instance (FR-S18 across members). Then, with `$S instance watch` on both instances: the instance on member 1 stopped, moved to member 3 (`lxc move --target`), started and read through member 1; member 2 evacuated (`lxc cluster evacuate`) and restored, with reads of its instance throughout. On 6.x, `volatile.cluster.group` of the instances is recorded. Live migration is not run (it needs `migration.stateful` for VMs and CRIU for containers), and the differences between the snap and deb packages are not testable in the lab (Ubuntu 24.04 has no LXD deb): both are recorded with `util note`. The runbook ends with `lab.sh reset` of the cluster; with `--record`, its responses are the cluster fixtures (L15). **Decision:** whether forwarding is transparent (reads, files, `PATCH` with `If-Match`, the filtered list); the accuracy of `location`, also after a move and an evacuation; whether `volatile.uuid` and the generation survive a move between members and an evacuation; rules for FR-S5 during migration (the status and errors an attestation sees in flight); the differences between 5.21 and 6.x, the former being the production baseline.

#### L12 — Shared devices and nested LXD

**Procedure:** build instances A and B sharing a disk device or bind mount over `/run/lxd-probe`; from B write a file at the path A would use. **Commands:** `$S instance show` on both, `util overlap`. In the lab the cells are: a host directory mounted by two containers at the proof directory, hot-plugged and present at boot; a custom storage volume attached to two containers there; the same through a profile, so that only `expanded_devices` shows it; a disk at an ancestor (`/run`); a disk below the proof directory (information only until the L12 decision, which rejects it); and a custom volume shared by a VM and an unprivileged and a privileged container. In each, B writes with `$G file put` and A's file is read with `$S file stat`; `util overlap --proof-dir /run/lxd-probe` runs on the records. The runbook deletes what it creates. **Decision:** the overlap check is complete enough or must additionally forbid listed device types; nested LXD inside a container is out of scope unless shown to defeat the check.

#### L13 — Bearer identities and short-lived tokens

**Commands:** `$S bearer check --auth bearer --wait-expiry`. In the lab (servers with bearer identities only; LXD 5.21 has none, recorded with `util note`): `bearer check` with the long-lived token; a token issued for 2 minutes, checked with `--wait-expiry`; and a token checked after a new one was issued for the same identity (revocation), with `--expect-denied`. **Decision:** whether the plugin can run without long-lived client certificates; the expiry behaviour determines the rotation requirements.

#### L14 — Instance update semantics

**Commands:**
```
$S config set NAME user.lxd-probe.a 1 --method patch --if-match auto --wait
$S config set NAME user.lxd-probe.a 2 --method patch --if-match stale
$S config set NAME user.lxd-probe.a 3 --method put   --if-match stale
$S config unset NAME user.lxd-probe.a --method patch-empty
$S config unset NAME user.lxd-probe.a --method patch-null
$S config unset NAME user.lxd-probe.a --method put
$S config race NAME user.lxd-probe.r --writers 8
```
**Decision:**
- `PATCH` honours `If-Match` (412 on stale) → FR-S14 uses `PATCH`.
- `PATCH` ignores `If-Match` → FR-S14 uses `PUT` of the whole object with `If-Match` (read-modify-write).
- No way to remove a single key except `PUT` → key cleanup (FR-S13) uses `PUT`.
- Spurious 412 rate in the race test > a few percent → raise the retry budget and the jitter; record the figure.
- Whether writes are synchronous or asynchronous and the typical `wait` duration → default `proof_timeout`.

**Idle ETag (Q5):** `$S instance watch NAME --for 5m` on an idle container and an idle VM; the ETag changes counted while nothing writes are the rate of spurious 412 the retry budget must absorb.

#### L15 — Fixtures

**Procedure:** run the commands above with `--record test/fixtures/<lxd-version>/`; add the provocations of 403 (identity without permission), 404 (unknown instance/file), 412 (stale ETag). **Commands:** `util sanitize`, `util sanitize --check`, `util fixtures verify`. **Decision:** the committed fixtures become the contract of the fake LXD API (PRT-10); any decode failure results in a client struct fix.

#### L16 — Project entitlements and the network project

**Question:** Does an instance grant work for an identity whose group has no project `can_view`, so that the attestor's least-privilege table (project `can_view_instances` plus instance entitlements) is complete as written? Which entitlement lets the identity read `GET /1.0/projects/{name}` with its `config`, and so `features.networks`, which `selectors.network_project` needs?

**Cells:** `lxd5` and `lxd6`; projects `default` and two made for the session, `l16-on` (`features.networks=true`) and `l16-off` (`features.networks=false`); groups and identities made for the session and deleted at its end: `l16-min`, a group without project `can_view`; `l16-view`, with project `can_view` on the three projects, then project `can_view_instances` and instance `can_view` and `can_access_files` on `c-unpriv` (in that order: LXD checks the project grant at each grant); `l16-project`, with project `can_view` on the three projects only; `outsider` for the hidden project; the clusters `c5` and `c6` for the project read only. **Procedure:** for `l16-min`, each grant of the minimal set (project `can_view_instances`, instance `can_view`, instance `can_access_files`), with LXD's answer recorded by `util note`; as `l16-view` and `l16-project`: `$S instance show c-unpriv`, `$S file stat c-unpriv /etc/hostname`, `$S instance list --filter "config.volatile.uuid eq <uuid>"`, and `$S project show` of `default`, `l16-on` and `l16-off`; `$S project show` of a missing project (404), as `outsider` of `restricted` (`--expect-hidden`) and with an untrusted certificate (`--expect-denied`); on each cluster, `$S project show default` through each member. With `--record test/fixtures/<lxd-version>/projects/`. **Decision:** (1) if the minimal set needs project `can_view` (LXD refuses its grants without it, or the reads fail), project `can_view` joins every mode of the attestor's *Least privilege* table, and `selectors.network_project` needs no further entitlement; otherwise the table stands and the option adds project `can_view`. (2) The entitlement that shows a project's `config` to the identity; if only a server-level grant does, `selectors.network_project` is withdrawn. The project fixtures become the contract of the project read.

