# LXD node attestor plugins — implementation spec

Oct 8, 2026 (merges lxd-instance-attestor-spec v0.3) · @Andrea Funtò

*Status*: draft v0.4, for spec-driven development. The repository holds stub plugins only: they accept an empty configuration and answer every attestation with `Unimplemented`. Implementation follows the order in *Testing → Implementation instructions*, starting with `lxd-probe` and the validation gates.

| Field | Value |
|---|---|
| Status | Draft v0.4 for spec-driven development (v0.3 plus the LXD Go client SDK as the basis of the LXD client) |
| Components | Agent NodeAttestor plugin + Server NodeAttestor plugin |
| Plugin name | `lxd_instance` (both sides) |
| Binaries | `lxd-agent-plugin` (SPIRE Agent), `lxd-server-plugin` (SPIRE Server) |
| Node definition | One LXD **instance** (container or virtual machine) running a SPIRE agent |
| Language / SDK | Go, `github.com/spiffe/spire-plugin-sdk`. LXD access through the **LXD Go client SDK** (`github.com/canonical/lxd/client` and `github.com/canonical/lxd/shared/...`), wrapped by `internal/lxd` (FR-S17). These packages are Apache-2.0 (their own `COPYING` files) although the rest of the LXD repository is AGPL-3.0-only; no other LXD package is imported (NFR-03, gate L10). |
| Guest OS | Linux (containers and VMs), amd64 and arm64. Windows guests out of scope (*Overview → Non-goals*) |
| Hard constraints | No CGO and no native libraries on either side; the agent MUST NOT require LXD client tools or `cloud-init` in the guest |
| Companion documents | `SPIRE vsphere_guest Node Attestor — Specification` v0.3 (same structure, same ID conventions, same threat-model method); `lxd-probe` specification v0.3, `lxd-probe.md` (validation harness for gates L1–L15, to be built and run first) |

**Keywords.** MUST, MUST NOT, SHOULD, MAY are used per RFC 2119. Requirements carry IDs (`FR-*`, `SEC-*`, `NFR-*`); validation gates carry `L*` IDs; threats `T*`. Statements about LXD behaviour are tagged **[D]** when confirmed in the LXD documentation consulted for this draft (*Testing → Sources consulted*) and **[V:Ln]** when they are assumptions that a validation gate (*Testing → Validation gates*) MUST confirm before the dependent code is written.

**Revision history.**

| Version | Changes |
|---|---|
| v0.1 | Initial draft. |
| v0.2 | `ready_signal` withdrawn; ETag 412 retry (FR-S14); UID shifting for `file_pull`; custom HTTP client; broker pattern moved into architecture and guidance. |
| v0.3 | Cross-references and ID gaps repaired (FR-A9, L5 kept as withdrawn); required API extensions reduced to what is used; FR-S14 rewritten (full re-verification on 412, same nonce, bounded budget, PATCH/PUT open point); UID rule rewritten around a single expected owner and `volatile.idmap.current` (*Proof of co-location → UID translation*); FR-S17 specifies the LXD client; licence-based rationale for not using the LXD SDK; minimal API contract (*LXD integration → Minimal API contract*); fixtures gate L15; broker generalised and marked not available in v1; `join_token` comparison restored; `lxprobe` introduced as step 0. |
| Oct 8, 2026 | Merged into `lxd-spire-plugins.md`, the repository's spec, and reorganised into its sections (Overview, Trust model, Agent-side plugin, Server-side plugin, Configuration, Testing) without changing requirements; IDs and tags unchanged; cross-references by section name; repository layout aligned with the repository; configuration samples use the packaged binaries. |
| Oct 8, 2026 (later) | Aligned with the lxprobe spec, now `lxd-probe.md`: v0.3 section map removed (no document cites v0.3 numbers any more); repository layout gains `internal/devlxd`, `internal/evidence`, `internal/probe` and `test/fakelxd`; *UID translation* names `util idmap check` for the cross-check; NFR-03 allow-list extended to the command line, configuration and logging libraries already in use. |
| Oct 9, 2026 | The validation probe is renamed `lxd-probe` (`cmd/lxd-probe/`). |
| v0.4 (Oct 9, 2026) | The LXD client is built on the LXD Go client SDK instead of a bespoke HTTP client: its `client` and `shared` packages are Apache-2.0 (per-package `COPYING`), so the licence question of L10 is closed and L10 keeps only the coverage check. FR-S17 rewritten as a wrapper with the project's own rules (fingerprint pin through the SDK's transport hook, body limits, explicit project, `RawQuery` where the SDK has no method); NFR-03 allow-list and tests adjusted. `GET /1.0/auth/identities/current` added to the API usage and the minimal API contract for the SEC-16 warning. NFR-05 no longer cites a gate of the vSphere spec. |

## Overview

This spec defines a matched pair of SPIRE plugins, `lxd_instance`, that attest an LXD instance's identity to a SPIRE Server.

- **Agent-side plugin** (`lxd-agent-plugin`): runs inside SPIRE Agent on the instance. Sends a claim and a nonce, then answers the server's co-location challenge.
- **Server-side plugin** (`lxd-server-plugin`): runs inside SPIRE Server. Resolves the instance through the LXD API, verifies the co-location proof, then emits the agent's SPIFFE ID and selectors.

### Goal

Let a SPIRE agent running inside an LXD instance prove *which LXD instance it is running in*, so that the SPIRE server can issue a node SVID whose SPIFFE ID and selectors are derived from facts **reported by the LXD API** about that instance.

### The central design problem

Unlike AWS, GCP or Azure, **LXD offers no signed instance identity document** and no way for the guest to obtain a proof that a third party can verify. What the guest-facing API (`devLXD`, `/dev/lxd/sock`) provides is:

- a read-only view of the instance's own data (**[D]** "Queries on /dev/lxd/sock only return information related to the requesting instance"); the caller is identified by the host from socket peer credentials (containers) or the vsock ID of the LXD agent (VMs) **[D]**;
- optionally a bearer token (LXD ≥ 6.6, API extension `auth_bearer_devlxd`) which authenticates the guest **to LXD** for extra endpoints; it is not something a SPIRE server can verify **[D]**.

So the guest cannot hand the SPIRE server any evidence about itself, and the server cannot learn anything about the guest from the SPIRE stream alone. The server therefore has to **go to LXD** (its REST API) for everything it asserts, and it has to prove that the party on the attestation stream really sits *inside* the instance it asked LXD about. That proof (a challenge/response through a channel that only exists inside that instance) is the core of this specification (*Trust model → Proof of co-location*). Every claim made about the node comes from LXD, never from the guest (SEC-15).

### In scope

Agent and server plugins, wire protocol, LXD API usage (through the LXD Go client SDK, wrapped by `internal/lxd`), an assessment of which claims are reliable (*Trust model → What claims can be made reliably*), threat model with countermeasures, configuration, observability, tests, validation gates, alternatives (including SPIRE `join_token`, *Non-goals → Alternatives and related designs*).

### Basic flow

1. The agent sends a *claim* (project, instance name, optional cloud-init instance-id) and a fresh nonce.
2. The server resolves the instance through the LXD API, runs the co-location challenge (*Trust model → Proof of co-location*), re-checks the instance, and returns the agent SPIFFE ID and selectors.

### Architecture

```text
  LXD host / cluster                          Instance X (container or VM)
  +-------------------------------+           +-----------------------------------+
  | LXD daemon                    |           | SPIRE agent                       |
  |  - instance X: volatile.*,    |           |  + lxd_instance agent plugin      |
  |    config, devices, status    |           |                                   |
  |  - files API, config API      |           |  (a) writes proof file in /run    |
  +---------------^---------------+           |      (default mode)               |
                  |                           |  (b) or reads the challenge from  |
                  | LXD REST API              |      /dev/lxd/sock                |
                  | (TLS, mTLS, port 8443)    +-----------------+-----------------+
  +---------------+---------------+                             |
  | SPIRE server                  |<--- attestation stream -----+
  |  + lxd_instance server plugin |     (SPIRE TLS, trust bundle)
  +-------------------------------+
```

### Wire protocol

Protobuf messages carried in the SPIRE `payload`/`challenge`/`response` byte fields. File `internal/proto/lxd_instance.proto` is normative; field numbers MUST NOT change or be reused after the first release.

```proto
syntax = "proto3";

package spire.plugin.lxd_instance.v1;

option go_package = "github.com/dihedron/lxd-spiffe/internal/proto;lxdinstancev1";

enum InstanceType {
  INSTANCE_TYPE_UNSPECIFIED = 0;  // devLXD not available; allowed
  INSTANCE_TYPE_CONTAINER = 1;
  INSTANCE_TYPE_VIRTUAL_MACHINE = 2;
}

enum ProofMode {
  PROOF_MODE_UNSPECIFIED = 0;  // rejected
  PROOF_MODE_FILE_PULL = 1;
  PROOF_MODE_CONFIG_PUSH = 2;
  reserved 3;                    // was PROOF_MODE_READY_SIGNAL, withdrawn in v0.2
  reserved "PROOF_MODE_READY_SIGNAL";
}

enum ClaimSource {
  CLAIM_SOURCE_UNSPECIFIED = 0;
  CLAIM_SOURCE_CONFIGURED = 1;
  CLAIM_SOURCE_DEVLXD_METADATA = 2;
  CLAIM_SOURCE_HOSTNAME = 3;
}

// What the guest says about itself. Untrusted lookup hints.
message Claim {
  string project = 1;                    // may be empty; <= 63 chars
  string instance_name = 2;              // required; <= 63 chars
  string cloud_init_instance_id = 3;     // optional UUID
  ClaimSource name_source = 4;
}

// Agent -> Server, sent as the attestation payload.
message AttestRequest {
  uint32 version = 1;                    // MUST be 1
  Claim claim = 2;
  bytes agent_nonce = 3;                 // exactly 32 bytes
  InstanceType instance_type = 4;        // from devLXD when available
  repeated ProofMode proof_modes = 5;    // <= 2, no duplicates
  string plugin_version = 6;             // <= 128 bytes
}

// Server -> Agent.
message Challenge {
  uint32 version = 1;
  ProofMode mode = 2;
  bytes server_nonce = 3;                // FILE_PULL: exactly 32 bytes. Other modes: empty.
  string proof_path = 4;                 // FILE_PULL: absolute path under the agent's proof_dir
  string proof_key = 5;                  // CONFIG_PUSH: devLXD config key to read
  reserved 6;                            // was ready_target, withdrawn in v0.2
  reserved "ready_target";
  uint32 timeout_ms = 7;
}

// Agent -> Server.
message ChallengeResponse {
  uint32 version = 1;
  ProofMode mode = 2;                    // MUST equal Challenge.mode
  bool done = 3;                         // guest-side step completed
  bytes echo = 4;                        // CONFIG_PUSH: the 64 hex bytes read via devLXD. Others: empty.
}
```

Final response: SPIRE `AttestResponse` with `agent_id`, `selectors`, `can_reattest`. Any validation failure is answered with the uniform error of SEC-10.

### Repository layout and build

```text
cmd/lxd-agent-plugin/                # agent plugin binary (SPIRE Agent starts it without arguments)
cmd/lxd-server-plugin/               # server plugin binary (SPIRE Server starts it without arguments)
cmd/lxd-probe/                       # validation harness (own specification, lxd-probe.md)
internal/command/                    # command line commands shared by the binaries (version)
internal/plugin/agent/lxdinstance/   # agent NodeAttestor (FR-A*)
internal/plugin/server/lxdinstance/  # server NodeAttestor (FR-S*)
internal/plugin/config/              # plugin_data decoding, unknown keys rejected
internal/plugin/logging/             # log/slog to SPIRE's hclog
internal/proto/lxd_instance.proto
internal/claim/                      # claim collection
internal/devlxd/                     # devLXD client on the SDK's ConnectDevLXD; shared by the agent and lxd-probe
internal/proof/                      # nonce, hash, constant-time compare
internal/agentproof/                 # file_pull, config_push (agent side)
internal/lxd/                        # wrapper around the LXD Go client SDK (FR-S17): pinning, limits, instance read, files, ops; shared with lxd-probe
internal/policy/                     # privilege/raw checks, device overlap, uid mapping/idmap logic
internal/selectors/
internal/limits/
internal/evidence/                   # lxd-probe only: evidence records, sanitizer, reports
internal/probe/                      # lxd-probe only: command implementations
pkg/metadata/                        # build metadata injected at link time
docs/                                # release documents; docs/validation/ (gate evidence), docs/threat-model.md
test/fixtures/                       # responses recorded from real LXD (gate L15)
test/fakelxd/                        # fake LXD API (replays fixtures)
test/                                # fuzz corpora
```
Build: `make` (development build), `make snapshot` and `make release`, through goreleaser (`.goreleaser.yaml`): `CGO_ENABLED=0`, `-trimpath`, `-buildvcs`; linux/amd64 and linux/arm64; deb and rpm packages, SBOM, signed checksums file; `make checksum` prints the SHA-256 for `plugin_checksum`. From LXD, only the Apache-2.0 SDK packages `github.com/canonical/lxd/client` and `github.com/canonical/lxd/shared/...` are imported (NFR-03).

### Non-goals

Workload attestation; Windows guests (LXD containers are Linux only, and Windows VMs are not expected to run `lxd-agent`, **[V:L7]**); Incus (a fork with a similar but separate API, may be a later port); attesting the LXD *host* (see *Alternatives → Host-resident SPIRE agent*); vTPM-based attestation (*Alternatives → vTPM*).

#### Alternatives and related designs

The designs below were considered and are not part of this one; some are recommended alongside it.

##### Host-resident SPIRE agent with an LXD workload attestor

Run **one SPIRE agent per LXD host** (attested by whatever suits the host: platform attestor, x509pop, TPM) and let containers reach its Workload API socket (a `disk`/`proxy` device). A custom *workload* attestor maps the calling PID to its instance through the LXD API and emits LXD selectors. No per-instance agent or node attestation is needed, identity is rooted in the host, which is the trust root anyway, and no extra SPIRE privileges on LXD are needed beyond the host's local socket. Limits: instances become *workloads* of a host node rather than nodes; it does not suit VMs or instances that need their own agent; a compromised host agent affects all its instances. **Recommended for evaluation when most instances are containers.**

##### Narrow challenge broker (pattern; not available in v1)

The SPIRE server needs a broad LXD entitlement in either mode: `can_access_files` (read and write any file in the instance) or `can_edit` (change any configuration). A small service **inside the LXD trust boundary** can hold that credential and offer the SPIRE server a narrow API, which cuts the blast radius of a compromised SPIRE server (T10) at the cost of one more component to run and secure.

Minimal contract for a later specification:
- operations: *resolve instance* (returns only the facts the plugin consumes, *LXD integration → Minimal API contract*), *read proof* (the broker derives the path from the agent nonce hash, SEC-04; the caller cannot choose a path), *put challenge* and *delete challenge* (only the key `user.spire.challenge.<id>`);
- mutual TLS with the SPIRE server's pinned certificate; project allow-list; per-caller rate limits; audit log; no generic pass-through to LXD;
- no state: nonces stay in the SPIRE server.

Status: **out of scope for v1.** The server plugin is built on the interfaces `InstanceReader`, `FileReader` and `ChallengeWriter` (*Testing → Implementation instructions* item 6) so a broker-backed implementation can be added without any protocol change. Until then, operators rely on the dedicated identity, dedicated and restricted projects, and `file_pull` (*Configuration → Operational guidance*).

##### SPIRE `join_token` (the built-in attestor), and as an add-on

Mechanics: the operator runs `spire-server token generate` (one SPIFFE ID, short TTL), places the token in the instance (cloud-init user data, or an LXD `user.*` key, or a file pushed by the orchestrator), and the agent presents it once.

| Aspect | `join_token` | `lxd_instance` (this spec) |
|---|---|---|
| Pre-shared secret | Yes: a bearer token that must be delivered and can leak | None: server-initiated challenge |
| Visible to | Anyone who can read the delivery channel (instance config is visible to every `can_view` user; cloud-init user data likewise) | Challenge values live seconds; the proof is a hash |
| Claims about the instance | Only the SPIFFE ID chosen at token creation; **no LXD-derived selectors**, nothing verified | Selectors from LXD (*Trust model → What claims can be made reliably*, *Server-side plugin → Selectors*), re-verified at attestation |
| Binding to *this* instance | Operator discipline only | Enforced through `volatile.uuid` and the proof |
| Re-attestation | Not possible (single use); evict and reissue | Configurable (`reattest`) |
| LXD privileges for SPIRE | None | Per mode (*Proof of co-location → Privilege cost of each mode*) |
| Automation | Orchestration must mint and deliver a token per instance | None per instance |

Assessment: `join_token` is acceptable for bootstrapping a first test environment or where SPIRE must have no LXD access at all, but it does not answer the requirement of asserting facts about the node. Its strength (no LXD privileges) and weakness (bearer secret, no claims) are the mirror image of this plugin.

**Optional later phase: `bootstrap_token` as a second factor.** The orchestrator mints a stateless, HMAC-signed, short-lived token bound to `server_id`, project, name and `volatile.uuid`, delivered through a channel that does not use instance config (for example a file pushed into `/run` by the orchestrator). The server plugin then requires it in addition to the co-location proof. It limits the damage of a weakness in the proof channel at the cost of orchestration work and a single-use cache in the server. Not part of v1.

##### `x509pop` or other pre-provisioned credentials

A certificate delivered to the instance (image build, cloud-init, secrets) with SPIRE's `x509pop`. Strong cryptographic proof of possession, but a long-lived secret in the instance and no LXD claims. May complement, not replace, this plugin.

##### vTPM

LXD VMs can have a software TPM device (`tpm`). Without a hardware-rooted endorsement chain on the host, it adds little to what the LXD API already states. Candidate for a separate future plugin if the hosts have attested hardware TPMs.

##### Source IP binding

The server could compare the agent's source address with the instance's reported addresses, but SPIRE's server plugin API does not give the plugin the caller's address, addresses are guest-controllable (R3), and NAT hides them. Not feasible.

## Trust model

### Trust boundaries

| ID | Boundary | Notes |
|---|---|---|
| TB1 | Instance ↔ LXD host | Container: shared kernel, isolation by namespaces/LXD. VM: hypervisor (QEMU/KVM). Containers are a weaker boundary than VMs, and *privileged* containers weaker still (*Trust model → Claims that look reliable but are not*). |
| TB2 | Agent ↔ SPIRE server | SPIRE TLS; server authenticated through the bootstrap trust bundle. |
| TB3 | SPIRE server ↔ LXD API | TLS with pinned certificate fingerprint or CA; mutual authentication with a dedicated client identity. |
| TB4 | Plugin binary ↔ SPIRE process | go-plugin subprocess, checksum pinned. |
| TB5 | In-instance process ↔ in-instance process | Not a boundary: any root process in the instance *is* the node (*Trust model → Trust assumptions*). |

### Trust assumptions

- The LXD daemon, its host, and the administrators with server-level rights are in the trusted computing base. The design limits and detects rogue *instance-level* users but cannot defend against LXD administrators or a compromised host (T17).
- Any process with root in the instance is inside the node's trust boundary. Node identity is *instance identity*.
- LXD correctly identifies the caller of devLXD and correctly reports instance state.
- Instance names are unique **per project**, not globally, and projects are an administrative namespace (*Trust model → Claims about provenance and configuration*).

### What claims can be made reliably

This section is the result of the analysis requested for the project. Every claim has a **reliability tier**, defined by *who can change the value*:

| Tier | Meaning | May be used for |
|---|---|---|
| **R1 Authoritative** | Maintained by LXD itself (`volatile.*` keys, derived fields). Not writable by guests or by instance editors; changes only through LXD operations. | Identity, ID derivation, strong selectors |
| **R2 Admin-controlled** | Set by LXD users holding edit rights on the instance, profile, project or network. As trustworthy as the LXD RBAC around them. | Selectors, after a documented RBAC review (T9) |
| **R3 Guest-asserted** | Originates in the guest, including anything obtained through devLXD that the guest can change. | Lookup hints only. **Never** a selector or an ID component. |
| **R4 Unavailable** | LXD cannot provide it. | Must be obtained by other means or accepted as a gap. |

#### Claims about identity

| Claim | LXD source | Tier | Notes |
|---|---|---|---|
| Instance exists and is **running** | `GET /1.0/instances/{name}` → `status` | R1 | A point-in-time statement (TOCTOU, T8). Frozen, stopped, or error states are rejected. |
| Globally unique instance UUID | `volatile.uuid` **[D]** ("globally unique across all servers and projects") | R1 | Basis of the SPIFFE ID. Behaviour on copy, move, backup import and restore to be confirmed **[V:L6]**. |
| Instance generation | `volatile.uuid.generation` **[D]** | R1 | Detects snapshot restore/rollback. |
| cloud-init instance-id | `volatile.cloud-init.instance-id` **[D]** | R1 | UUID exposed to cloud-init; used as a second consistency check on the claim. |
| vsock ID (VMs) | `volatile.vsock_id` **[D]** | R1 | Informational. |
| Type (container / VM), architecture | instance `type`, `architecture` | R1 | |
| Cluster member running it | `location` | R1, volatile in time | Changes on migration/evacuation. Opt-in selector only. |
| Instance **name** | instance `name` | R2 | Unique per project; editors with sufficient rights can rename (stopped instance) or recreate. Used as the *lookup key*, not as the ID. |
| **Project** | `project` | R2 | Administrative namespace; the project allow-list is the main scoping control (FR-S3). |
| Creation time, last-used time | `created_at`, `last_used_at` | R1 | Informational. |

#### Claims about provenance and configuration

| Claim | LXD source | Tier | Notes |
|---|---|---|---|
| Base image fingerprint | `volatile.base_image` **[V:L6]** | R1, *historic* | States which image the instance was created from, not what runs in it today. Useful for "only instances cloned from golden image X". |
| Image properties | `image.os`, `image.release`, `image.serial`… (config) | R2 | Copied from image metadata at creation; editable. |
| Profiles applied | `profiles` | R2 | A profile edit changes many instances at once. |
| Posture: privileged container | `security.privileged` (expanded config) | R2 | **Key for policy**: root in a privileged container is effectively host root (*Trust model → Claims that look reliable but are not*). |
| Posture: nesting, raw overrides | `security.nesting`, `raw.lxc`, `raw.qemu`, `raw.idmap`, `raw.apparmor`… | R2 | Overrides can weaken isolation; configurable denylist (FR-S5). |
| VM posture | `security.secureboot`, TPM device presence | R2 | Statements about VM *configuration*, not measured boot. |
| Devices | `expanded_devices` (disk, nic, proxy, unix-char…) | R2 | Disk devices matter for co-location checks (T2). |
| Network attachment | NIC device `network` / `parent`, `volatile.<dev>.hwaddr` **[D]** | R2 (attachment), R1 (assigned MAC) | The assigned MAC is configuration; a guest with network privileges can change it inside the guest. |
| Resource limits | `limits.*` | R2 | |
| Free-form tags | `user.*` **[D]** ("Free-form user key/value storage") | R2 | Any editor of the instance can set them. Opt-in allow-listed keys only. |
| Cluster group | `volatile.cluster.group` (LXD ≥ 6.6, release notes) **[V:L8]** | R1/R2 | Placement information; opt-in. |

#### Claims that look reliable but are not

- **Container isolation strength**: all containers share the host kernel; a *privileged* container's root can generally take over the host and therefore every other instance and its identity. The plugin MUST expose `privileged` as a selector and, by default, refuse privileged containers (`policy.allow_privileged = false`, FR-S5).
- **IP addresses** reported by `/1.0/instances/{name}/state`: LXD reads them from inside the instance's network namespace, so root in the instance can choose them (R3). The SPIRE server plugin API does not expose the caller's source address, so IP binding is not feasible (*Alternatives → Source IP binding*).
- **Instance name from the guest** (hostname, `meta-data`): the guest controls its hostname (R3). It is a lookup hint; the challenge proves whether it points at the right instance.

#### What LXD cannot attest (R4)

- Integrity of the software inside the instance (no measured boot, no agent integrity); that is the job of workload attestation and image provenance controls.
- Integrity of the LXD host or cluster member (no remote attestation of the LXD daemon).
- *Which process* inside the instance is calling.
- A signed, verifiable identity document, or a trusted clock inside the instance.

#### Conclusion for the design

1. The node identity is derived from `volatile.uuid` plus an operator-assigned `server_id` (R1).
2. Selectors default to R1 facts plus a small R2 set (project, profiles, privileged flag, networks). R2 selectors are Medium trust and documented as such.
3. Everything is fetched from LXD; the guest contributes only a lookup claim, a nonce and the proof.
4. The proof is verified through the LXD API, and its **privilege cost on the LXD side is the main security trade-off** (*Proof of co-location → Privilege cost of each mode*).

### Proof of co-location

The server needs evidence that the stream party can act *inside the instance it resolved*. All modes bind the evidence to a fresh server nonce and to the agent's nonce. Because the agent cannot send LXD-verifiable data, **the server always uses its own LXD credential to reach into the instance**; the modes differ in which direction data travels and which LXD entitlement is needed.

#### Mode `file_pull` (default)

Rationale: only code that can write inside the instance's filesystem can create the file, and a **root-owned file in a root-only directory under `/run`** is evidence of *privileged* presence in the instance, which is a stronger guest-side gate than "can open a socket" (T13).

1. Server picks `server_nonce` (32 bytes) and `proof_path = <proof_dir>/proof-<id>` where `id = hex(SHA-256(agent_nonce))[:16]` (SEC-04); `proof_dir` is the server's configured copy of the agent path (default `/run/spire/lxd`). Sends `Challenge{mode=FILE_PULL, server_nonce, proof_path}`.
2. Agent computes `proof = SHA-256( "spire-lxd-v1" 0x00 ‖ u16be(len(project)) ‖ project ‖ u16be(len(name)) ‖ name ‖ server_nonce ‖ agent_nonce )` using the claim values it sent, writes `hex(proof)` (64 bytes, no newline) as in FR-A7, replies `ChallengeResponse{done=true}`.
3. Server reads the file with `GET /1.0/instances/{name}/files?path=<proof_path>&project=<p>` and requires: regular file; owner equal to the single expected owner of step 6 and mode 0600 as reported by the files API **[V:L1]**; size exactly 64 bytes; content equal to the recomputed hex proof (constant time).
4. Proceed to FR-S10 and issue the identity.
5. **Isolation check (T2)**: before step 1 the server inspects `expanded_devices` and rejects the attestation if any non-root `disk` device has a `path` equal to, or an ancestor of, `proof_dir` (a shared mount could let another instance forge the file). The check is a best effort and is documented as such.
6. **Expected owner.** Exactly **one** owner value is accepted per attestation. It is fixed by the instance class and by what the files API reports for it (gate L1 decides which representation LXD uses): virtual machines and privileged containers → uid 0; unprivileged containers → uid 0 if LXD reports the in-container uid, otherwise the host uid that namespace uid 0 maps to, computed as in *Proof of co-location → UID translation*. Accepting "either of two values" is not allowed. The server SHOULD also stat `proof_dir` through the files API and require a directory with the same expected owner and mode 0700 (defence in depth; the agent already enforces it locally, FR-A7).

Required LXD entitlements: instance `can_view` and `can_access_files` (*LXD integration → Least privilege*). The plugin only *reads* files.

##### UID translation (applies only if gate L1 shows that LXD reports host-side owners)

- **Source.** The map in use by a *running* container is `volatile.idmap.current` (R1). `volatile.idmap.next` is the map for the next start and MUST NOT be used for a running instance (they differ after, for example, `security.idmap.isolated` is changed, until a restart). `security.idmap.isolated`, `security.idmap.base` and `security.idmap.size` are settings, not data sources. Key semantics **[V:L1]**; the value is a JSON array of `{Isuid, Isgid, Hostid, Nsid, Maprange}` entries.
- **Algorithm.** Consider only entries with `Isuid = true`. Find the entries whose namespace range contains namespace uid 0 (`Nsid ≤ 0 < Nsid + Maprange`). Exactly one entry must match, otherwise the attestation fails. The expected host uid is `Hostid + (0 − Nsid)`.
- **Fail closed** when `volatile.idmap.current` is missing or not parseable, when entries overlap, or when a value does not fit in 32 bits. When `raw.idmap` is set on the instance, v1 rejects it for `file_pull` if host-side reporting applies (keeps the mapping a single range).
- Only the uid is checked (plus the mode); group ownership is not part of the rule.
- The translation lives in `internal/policy` and is shared with `lxd-probe`, which runs it (`util idmap translate`) and cross-checks `volatile.idmap.current` against the guest's own `/proc/self/uid_map` (`util idmap check`).

#### Mode `config_push`

For deployments where the files API is not permitted but the server may edit instance configuration.

1. Server writes `user.spire.challenge.<id>` = `hex(server_nonce)` on the instance (`PATCH /1.0/instances/{name}`, `If-Match` ETag, FR-S14 ETag Retry Logic), waits for the operation (FR-S13), then sends `Challenge{mode=CONFIG_PUSH, proof_key}`.
2. Agent reads the value through devLXD (`GET /1.0/config/<proof_key>`) and returns it in `echo`.
3. Server compares with `hex(server_nonce)` in constant time, then removes the key (second `PATCH`, awaited), also on failure and timeout.

Properties: devLXD returns only the calling instance's data **[D]**, so another instance cannot read the value. The guest-side gate is weaker than `file_pull` (any process that can open `/dev/lxd/sock` can read the value; who that is inside the instance is **[V:L4]**). The value is visible to everyone with `can_view` on the instance for a few seconds. Requires `can_edit` on the instance, which is a broad entitlement (*Proof of co-location → Privilege cost of each mode*). Needs `security.devlxd=true` (default **[D]**), live update of `user.*` keys on a running instance, and immediate devLXD visibility **[V:L2]** (the option reference lists `user.*` with live update "No", which MUST be checked).

#### Mode negotiation

`proof_mode ∈ {"file_pull","config_push","auto","none"}` (server). `auto` takes the first of `file_pull`, `config_push` that appears in the agent's `proof_modes` **and** is enabled on the server. A server never downgrades below the configured mode silently. `none` (claim only, no proof) is rejected at `Configure` unless `i_understand_claim_only_is_insecure = true`, and then logs WARN on every attestation (SEC-02).

#### Privilege cost of each mode (the main trade-off)

| Mode | LXD entitlements for the SPIRE identity | If that identity is stolen | Guest-side gate |
|---|---|---|---|
| `file_pull` | instance `can_view`, `can_access_files` | read and **write** arbitrary files in the targeted instances (the entitlement covers push and pull); no host-level change by itself | root write to a 0700 `/run` directory |
| `config_push` | instance `can_view`, `can_edit` | edit instance configuration, including devices and `raw.*`; **host-level impact is possible** in projects that are not restricted | any process reaching `/dev/lxd/sock` |
| none/claim-only | `can_view` | read-only | none (rejected by default) |

Entitlement names as listed in the LXD permission reference **[D]**; whether they are sufficient and minimal in practice **[V:L9]**.

Recommendations (SEC-16, *Configuration → Operational guidance*):
- Use a **dedicated LXD identity** for SPIRE, limited to the projects that hold SPIRE-managed instances (a dedicated project is preferred), never a server administrator.
- For `config_push`, require **restricted projects** (`restricted=true` with restrictions on devices, privileged containers and low-level settings) so `can_edit` cannot reach the host.
- **Narrow challenge broker (recommended pattern, not available in v1):** a small service inside the LXD trust boundary that holds the broad credential (`can_access_files` and/or `can_edit`) and offers the SPIRE server only the three operations the plugin needs: resolve an instance (read-only), read the proof file at the derived path, write/delete the challenge key. It matters for **both** modes: `can_access_files` also allows pushing and pulling arbitrary files. Until a broker exists, rely on the dedicated identity, dedicated and restricted projects, and prefer `file_pull` (see *Alternatives → Narrow challenge broker*).

### Threat model

#### Assets

| ID | Asset | Why it matters |
|---|---|---|
| AS1 | Node SVID / node identity | Gateway to workload SVIDs for everything in the instance |
| AS2 | SPIRE's LXD client credential | Read, and for some modes write, access to instances |
| AS3 | LXD API availability | Shared by all administrators |
| AS4 | SPIRE server availability | Attestation endpoint is reachable before authentication |
| AS5 | Selector truthfulness | Registration entries authorize workloads by them |
| AS6 | Integrity of plugin binaries and config | Compromise means identity forgery |

#### Actors

| ID | Actor | Capability |
|---|---|---|
| A1 | Remote network attacker | Can reach the SPIRE server port |
| A2 | Malicious instance on the same LXD host or cluster | Root in another instance; may know a victim's name |
| A3 | Unprivileged user in the legitimate instance | Can run code, cannot read root-only data |
| A4 | Root in the legitimate instance | Fully controls that node |
| A5 | LXD user with instance-level edit rights (create, copy, rename, edit config/profiles, import) | Can create lookalikes and change R2 data |
| A6 | Network MITM (agent↔server, server↔LXD) | Intercepts or alters traffic |
| A7 | LXD administrator / compromised host or daemon | Controls all claims |
| A8 | Backup/image operator | Can import exported instances, possibly with duplicate identifiers |
| A9 | Supply-chain attacker | Tampers with dependencies, build, release |
| A10 | Attacker holding SPIRE's LXD credential | Acts as the SPIRE server towards LXD |

#### Threats and countermeasures (STRIDE)

| ID | Threat | Actor | STRIDE | Countermeasures | Residual |
|---|---|---|---|---|---|
| T1 | **Claim spoofing**: agent in instance Y claims project/name of X | A2 | S | Co-location proof (SEC-01): the secret is only retrievable through X's filesystem or devLXD; Y can only write/read its own | Low |
| T2 | **Forged proof via shared storage**: X and Y share a disk device and Y writes the proof file | A2, A5 | S | Proof path under tmpfs `/run`; ownership uid mapping check / mode 0600 checked; isolation check of `expanded_devices` for overlapping non-root disk devices (FR-S5); document that shared mounts at `/run` are unsupported | Low–Medium: depends on custom mounts, gate L12 |
| T3 | Nonce/payload replay | A1, A6 | S | 256-bit server nonce, single use, TTL 30 s, proof hash binds both nonces and the claim (SEC-03) | Negligible |
| T4 | MITM agent↔server | A6 | S,T,I | SPIRE TLS with trust bundle; docs warn against `insecure_bootstrap` (SEC-14). The proof is useless without being in the instance | Depends on bootstrap hygiene |
| T5 | MITM or impersonation of the LXD API | A6 | S,T,I | TLS with pinned server certificate fingerprint or CA, mutual TLS; `dev_mode` is the only bypass (SEC-07) | Low |
| T6 | **Name collision**: same instance name in several projects or servers | A5 | S | Project allow-list; exactly one match across servers and projects (SEC-05); `cloud_init_instance_id` consistency check; ID based on `volatile.uuid` | Low |
| T7 | **Duplicate UUID** after backup import/restore on another server | A8 | S | `server_id` is part of the ID; the same UUID found on two configured servers is ambiguous and fails closed; operators keep `server_id` unique; behaviour confirmed in gate L6 | Medium until L6 |
| T8 | Stale or changed instance (stopped, frozen, migrated, restored, renamed) during attestation | A5, A7 | S,T | `Running` required; full re-read after the proof comparing uuid, generation, project, name (SEC-06, FR-S10); ETag on writes | Low |
| T9 | **Selector forgery** by users with edit rights (profiles, `user.*`, image properties, networks) | A5 | E,T | Trust tier per selector (*Server-side plugin → Selectors*); R2 selectors Medium; allow-listed `config`/`user` keys; guidance on LXD RBAC and restricted projects; audit log of issued selectors | Medium: depends on RBAC discipline |
| T10 | **Over-privileged SPIRE credential** amplifies a server compromise | A10 | E | Least-privilege entitlements per mode (*Proof of co-location → Privilege cost of each mode*); dedicated identity and projects; restricted projects for `config_push`; credential from file with permission checks (SEC-08); startup warning if the identity looks like an administrator (SEC-16); narrow broker pattern (*Alternatives → Narrow challenge broker*, not available in v1); alert on lifecycle events whose requestor is the SPIRE identity and that touch anything other than the challenge key | Medium: inherent to LXD's coarse entitlements |
| T11 | DoS of the LXD API through unauthenticated attestation attempts | A1 | D | Syntactic validation first (FR-S2); global concurrency cap, token bucket, per-claim cooldown with backoff, circuit breaker (SEC-09); firewall the SPIRE port to instance networks | Medium |
| T12 | Enumeration oracle (does instance X exist?) | A1 | I | One uniform error for every failure; detail only in server logs (SEC-10) | Low |
| T13 | **Unprivileged in-instance user attests as the node** | A3 | S,E | `file_pull` needs root (0700 directory, uid 0 file mapping); `config_push` only needs access to devLXD; the agent MUST run as root; `reattest = "deny"` once a node is attested; guidance on `/dev/lxd/sock` access **[V:L4]**; `security.devlxd=false` where devLXD is not needed | Low for `file_pull`, Medium otherwise |
| T14 | **Rollback, clone or restore** keeps or duplicates identity | A5, A8 | S | Copy gets a new `volatile.uuid` **[V:L6]**; `volatile.uuid.generation` checked in FR-S10 and available as selector; short agent SVID TTL (SEC-12); evict agents on restore | Medium: restoring the *same* instance is the same instance |
| T15 | **Privileged container or raw overrides weaken the boundary** | A4, A5 | E | `privileged` selector; `allow_privileged=false` default; `deny_config_keys` (e.g. `raw.lxc`, `raw.idmap`) | Low if configured |
| T16 | Challenge visible to LXD viewers (`config_push`) or readable proof files | A5, A7 | I | Single-use, short-lived values; proof is a hash; key/file removed after use (best effort); mode `file_pull` preferred | Low |
| T17 | Compromised or malicious LXD daemon/host | A7 | all | Not preventable; mitigate with separate credentials, audit log correlation by `volatile.uuid`, network restriction | Accepted |
| T18 | Parsing attacks (LXD JSON, devLXD responses, proof file, protobuf) | A1, A2 | T,D | Size caps, strict decoding, no reflection on untrusted data, fuzz tests (SEC-11) | Low |
| T19 | Plugin or dependency tampering | A9 | T | Pinned `plugin_checksum`, reproducible builds, pinned dependencies, SBOM, `govulncheck`, signatures (SEC-13) | Low |
| T20 | **LXD API drift or missing extensions** produce silent misbehaviour | – | D,T | Required API extensions verified on every connect (FR-S12); fail closed; contract tests against fixtures recorded from each supported LXD version (L15) | Low |
| T21 | Cluster migration or evacuation during attestation | A7 | D | Files/config calls go through the cluster API; one retry on a transient redirect; otherwise fail and let the agent retry | Operational |
| T22 | **Async operation race** (`config_push`): the agent reads before the update completes, or cleanup never completes | – | D | Await operations (FR-S13); handle ETag mismatches explicitly; metric and WARN on failed cleanup | Low |
| T23 | Log and telemetry leaks | any | I | Redaction of nonces, proofs, credentials; no identifiers as metric labels | Low |
| T24 | **Nested LXD** (instance that itself hosts LXD) misattributed | A5 | S | The node is the innermost instance; the server is configured per LXD that really manages it; documented | Operational |

#### Security requirements

| ID | Requirement |
|---|---|
| SEC-01 | Identity MUST NOT be issued on a claim alone; a co-location proof is mandatory unless SEC-02 is overridden. |
| SEC-02 | `proof_mode = "none"` is rejected at `Configure` unless `i_understand_claim_only_is_insecure = true`; WARN on every attestation and a metric while active. |
| SEC-03 | Nonces: 32 bytes from `crypto/rand`, single use, compared in constant time, TTL ≤ 30 s. |
| SEC-04 | Proof paths and keys MUST be derived as `...<hex(SHA-256(agent_nonce))[:16]>`; no client-chosen names. |
| SEC-05 | Zero or multiple instance matches MUST fail closed with the uniform error. |
| SEC-06 | Instance state, identity and generation MUST be re-verified after the proof and before returning the response. |
| SEC-07 | TLS to LXD with the server certificate pinned (fingerprint) or verified against a configured CA, mutual TLS, minimum TLS 1.3 where LXD requires it; `dev_mode` is the only bypass and logs WARN on every connection. |
| SEC-08 | Credentials come from files with permission checks (0600/0400); inline secrets are rejected unless `dev_mode`. |
| SEC-09 | Global concurrency cap (default 16), token bucket (default 20/s), per-claim cooldown (5 s after a failure, exponential up to 5 min). |
| SEC-10 | Failures towards the agent are uniform (`PermissionDenied: attestation failed`); detail only in server log with a correlation ID. |
| SEC-11 | All untrusted inputs bounded: payload ≤ 4 KiB, strings ≤ 128 bytes, proof file exactly 64 bytes, LXD/devLXD responses ≤ 1 MiB, LXD and devLXD JSON decoded into typed structs that ignore unknown fields and validate required ones (FR-S17). Fuzz targets for every parser. |
| SEC-12 | Documentation MUST recommend agent SVID TTL ≤ 1 h and explain `reattest`. |
| SEC-13 | Release pipeline: pinned dependencies, `govulncheck`, SBOM, signatures, reproducible flags. |
| SEC-14 | Documentation MUST state that `insecure_bootstrap` voids the T4 protections. |
| SEC-15 | Selectors and ID components MUST come only from LXD-sourced R1/R2 data; guest-asserted values (R3) MUST NOT be offered as selectors. |
| SEC-16 | The server MUST write only the key `user.spire.challenge.<id>` and read only the proof path it derived; it MUST log a WARN at startup when its LXD identity appears to hold server-level administrator rights, as reported by `GET /1.0/auth/identities/current` when the server offers it (*LXD integration → API usage and required extensions*) **[V:L9]**. |

## Agent-side plugin

### Functional requirements

| ID | Requirement |
|---|---|
| FR-A1 | Implement the SPIRE `NodeAttestor` agent interface (`AidAttestation` stream) plus `Configure`/`GetPluginInfo`. |
| FR-A2 | **Claim collection**, in order of precedence per field: (1) explicit plugin configuration (`claim.project`, `claim.instance_name`); (2) devLXD: `GET /1.0/meta-data` for `instance-id` and `local-hostname` **[V:L3]**; (3) OS hostname (name only). The claim records its `source` for diagnostics. The agent MUST NOT fail only because devLXD is absent when the configured proof mode does not need it. |
| FR-A3 | devLXD access (optional, `use_devlxd = auto/required/off`): HTTP/1.1 over the Unix socket `/dev/lxd/sock` (configurable path), per-request timeout 5 s, response body ≤ 64 KiB, typed JSON decoding that ignores unknown fields and validates required ones. `GET /1.0` yields `instance_type` and `api_version`; failures are non-fatal under `auto`. |
| FR-A4 | Generate `agent_nonce`: 32 bytes from the OS CSPRNG. |
| FR-A5 | Send the first message (`AttestRequest`, *Overview → Wire protocol*) with claim, nonce, instance type (if known), supported proof modes and plugin version. |
| FR-A6 | On `Challenge`, execute the requested mode (*Trust model → Proof of co-location*) and send `ChallengeResponse`. |
| FR-A7 | `file_pull`: MUST run as root (euid 0) and fail clearly otherwise; create the proof directory (default `/run/spire/lxd`) with mode 0700 if missing; refuse if it exists with another owner, other mode, or is a symlink; create the proof file with `O_CREAT\|O_EXCL\|O_NOFOLLOW`, mode 0600, content = the 64 lowercase hex characters of the proof (*Proof of co-location → Mode `file_pull`*), `fsync`; best-effort removal when the stream ends. The proof path MUST equal the path in the challenge and MUST be inside the configured `proof_dir`. |
| FR-A8 | `config_push`: read `/1.0/config/<proof_key>` through devLXD (key must match `^user\.spire\.challenge\.[0-9a-f]{16}$`), validate the value as 64 hex characters, return it in `echo`. |
| FR-A9 | *Withdrawn in v0.2 (was the experimental `ready_signal` mode). ID kept so that references stay stable.* |
| FR-A10 | Never log nonces, proofs or challenge values. Claim fields may be logged at INFO. |
| FR-A11 | Enforce an overall attestation timeout (default 30 s) and per-step timeouts. |
| FR-A12 | Static binary, no CGO, linux/amd64 and linux/arm64. No dependency on `lxc`, `lxd-agent`, `cloud-init`, `curl` or any external program. |

## Server-side plugin

### Functional requirements

| ID | Requirement |
|---|---|
| FR-S1 | Implement the SPIRE `NodeAttestor` server interface (`Attest` stream) with challenge/response. |
| FR-S2 | Validate the payload strictly *before* contacting LXD: version, nonce length, claim string syntax (project and instance names as accepted by LXD: letters, digits, hyphen, ≤ 63 chars), size ≤ 4 KiB, proof modes. |
| FR-S3 | **Resolve the instance.** For each configured LXD server and each project in its allow-list (or only the claimed project, which MUST be in the allow-list): `GET /1.0/instances/{name}?project=<p>` (recursion giving `expanded_config` and `expanded_devices`). If a `cloud_init_instance_id` is claimed, it MUST equal `volatile.cloud-init.instance-id`. **Exactly one** match across all servers and projects is required; zero or more than one → fail closed (SEC-05). |
| FR-S4 | Obtain all decision data from **one** instance read (including its `ETag`); a later re-read is used only for the post-proof check (FR-S10) and for ETag failure retries. |
| FR-S5 | **Policy checks** on the data read: `status` is `Running`; project is in the allow-list; `type` matches the agent hint when given; `volatile.uuid` is present and a valid UUID; `security.privileged` is not `true` unless `policy.allow_privileged`; none of `policy.deny_config_keys` is set; for the selected proof mode, mode-specific isolation checks (*Proof of co-location → Mode `file_pull`* point 5, *Proof of co-location → Mode `config_push`*) and, for `file_pull`, the expected-owner rule (*Proof of co-location → Mode `file_pull`* point 6, *Proof of co-location → UID translation*). |
| FR-S6 | Run the proof-of-co-location challenge (*Trust model → Proof of co-location*) before issuing any identity. |
| FR-S7 | Build the agent SPIFFE ID from `volatile.uuid` and the configured `server_id`: `spiffe://<trust-domain>/spire/agent/lxd_instance/<server_id>/<volatile.uuid>`, via the Go template `agent_path_template` (default `{{ .PluginName }}/{{ .ServerID }}/{{ .InstanceUUID }}`). |
| FR-S8 | Return the selectors (*Server-side plugin → Selectors*) of type `lxd_instance` that are enabled by configuration. |
| FR-S9 | `can_reattest` follows `reattest = "allow" \| "deny"` (default `allow`; `deny` is RECOMMENDED where operations accept evicting an agent before a re-attestation, SEC-12). |
| FR-S10 | **Post-proof re-verification**: re-read the instance; it MUST still be `Running` with the same `volatile.uuid` and the same `volatile.uuid.generation` and project/name; otherwise fail (SEC-06). |
| FR-S11 | Support multiple LXD servers/clusters (`servers` map) with independent credentials, pinned certificates, project allow-lists and `server_id`. |
| FR-S12 | On connect and reconnect, read `GET /1.0` and verify the **required API extensions** (*LXD integration → API usage and required extensions*) and server authentication state; refuse to attest through a server that lacks them (fail closed, log once). |
| FR-S13 | **Operation synchronization**: instance updates are background operations; for every write (`config_push` challenge and its cleanup) the server MUST wait for the operation to finish successfully within a bounded timeout before the next protocol step. A failed challenge write fails the attestation; a failed cleanup is logged at WARN and counted (`config_push_cleanup_failures_total`). |
| FR-S14 | **Conditional writes and 412 retries.** Writes carry the ETag of the last read in `If-Match`. The LXD REST documentation describes `If-Match` for `PUT`, warns that `PATCH` does not work in every case, and does not say how a single key is deleted; whether `PATCH` honours `If-Match` and how the challenge key is removed (`PATCH` with an empty value, or a `PUT` of the whole object) is decided by gate L14, which fixes the final form of this requirement. On `412 Precondition Failed` the server MUST: (1) re-read the instance; (2) re-run **all** checks of FR-S5 on the new data and compare `volatile.uuid`, `volatile.uuid.generation`, project and name with the first read, aborting on any difference; (3) retry with the **same** challenge value, exponential backoff with jitter, at most 3 retries and never beyond the remaining `proof_timeout`. If a `PUT` is required, the body is the *just re-read* object with only the challenge key changed. Exhausted retries fail the attestation with reason `precondition_failed` and the uniform error (SEC-10). The same rules apply to the cleanup write. Only the key `user.spire.challenge.<id>` may change (SEC-16). |
| FR-S15 | Maintain one authenticated, reconnecting client per LXD server with timeouts, exponential backoff and a circuit breaker (T11), implemented with the client of FR-S17. |
| FR-S16 | Expose structured audit logs and metrics (*Server-side plugin → Observability*). |
| FR-S17 | **LXD client** (package `internal/lxd`): a thin wrapper around the LXD Go client SDK (`github.com/canonical/lxd/client`, types from `github.com/canonical/lxd/shared/api`) that adds the project's rules; callers never use the SDK directly. (1) **TLS**: the SDK's TLS configuration (minimum TLS 1.3, required by LXD **[D]**) with mutual TLS; the client certificate and key are loaded from files and reloaded when they change (`GetClientCertificate`, installed through the SDK's `TransportWrapper`); server verification either by comparing the SHA-256 fingerprint of the presented leaf certificate with the pinned value in `VerifyConnection`, installed through `TransportWrapper` (LXD certificates are self-signed by default; standard chain verification is replaced only in this pinned mode), or by chain verification against `ca_bundle_file` (`TLSCA`); no unpinned `InsecureSkipVerify` outside `dev_mode`. (2) **HTTP**: redirects never followed (a 3xx is an error), every call bound to a context deadline (the SDK's `...WithContext` connection and request variants). (3) **Scope**: the project is always set explicitly (`UseProject`). (4) **Envelope** **[D]**: the SDK parses `sync` / `async` / `error`; the wrapper maps its errors (`api.StatusErrorCheck` and the HTTP status) to internal error kinds; vendor error text goes to logs only. (5) **Operations**: the SDK's operation wait, bounded by a timeout, and a check of the operation's final status and error. (6) **Limits**: response bodies limited in the transport (JSON ≤ 1 MiB, files API ≤ 4 KiB). (7) **Decoding**: the SDK's typed structs from `shared/api`; the wrapper validates the fields it consumes (*LXD integration → Minimal API contract*); decision data is never read from untyped maps. (8) `ETag` captured on reads and sent as `If-Match` on writes (FR-S14). (9) **Gaps**: calls the SDK has no method for (expected: `PATCH` of an instance, `GET /1.0/auth/identities/current`) use the SDK's `RawQuery`, decoded into `shared/api` types where they exist. (10) No credentials or tokens in logs; a `User-Agent` naming plugin and version. (11) Verified against fixtures recorded from real LXD (*Testing → Tests*, gate L15). The SDK version is pinned in `go.mod` (a pseudo-version: the Go module has no release tags) and updated deliberately, with the fixtures replayed. |

### Selectors

Type: `lxd_instance`. Values are escaped according to SPIRE selector rules and capped at 256 characters. `Trust` follows the tiers of *Trust model → What claims can be made reliably*.

| Selector | Trust | Default | Notes |
|---|---|---|---|
| `lxd_instance:server:<server_id>` | High | on | Operator-configured |
| `lxd_instance:uuid:<volatile.uuid>` | High (R1) | on | |
| `lxd_instance:project:<name>` | Medium (R2) | on | |
| `lxd_instance:type:<container\|virtual-machine>` | High (R1) | on | |
| `lxd_instance:arch:<architecture>` | High (R1) | off | |
| `lxd_instance:privileged:<true\|false>` | Medium (R2) | on (containers) | Basis of "unprivileged only" registration entries |
| `lxd_instance:profile:<name>` | Medium (R2) | on | One per applied profile |
| `lxd_instance:network:<project>/<network>` | Medium (R2) | on | One per NIC device with a managed `network`; unmanaged NICs give `nic-parent:<parent>` instead |
| `lxd_instance:member:<cluster-member>` | High, volatile | off | Changes on migration |
| `lxd_instance:base-image:<fingerprint>` | High, historic (R1) | off | Only if `volatile.base_image` is present **[V:L6]** |
| `lxd_instance:image:<os>/<release>` | Medium (R2) | off | From `image.os`, `image.release` |
| `lxd_instance:secureboot:<true\|false>` | Medium (R2) | off | VMs |
| `lxd_instance:config:<key>:<value>` | Medium (R2) | off | Only keys in `selectors.config_keys` |
| `lxd_instance:user:<key>:<value>` | Medium (R2) | off | Only keys in `selectors.user_keys` |
| `lxd_instance:generation:<uuid>` | High (R1) | off | Pins entries to one "timeline"; changes on restore |
| `lxd_instance:name:<name>` | Low (R2, renamable) | off | |

Guest-asserted data (hostname, addresses, OS data) MUST NOT become selectors (SEC-15). Selector sets are configurable with `selectors.enable`.

### LXD integration

#### API usage and required extensions

| Purpose | Call | Notes |
|---|---|---|
| Capability check | `GET /1.0` | Verify `api_extensions`, authentication state, server version, clustering |
| Resolve instance | `GET /1.0/instances/{name}?project=<p>` | `volatile.*`, `expanded_config`, `expanded_devices`, `status`, `type`, `location`, `profiles`; keep `ETag` |
| Read proof | `GET /1.0/instances/{name}/files?path=…&project=<p>` | `file_pull` |
| Write/remove challenge | `PATCH` (or `PUT`, gate L14) `/1.0/instances/{name}?project=<p>` with `If-Match` | `config_push`; background operation, `GET /1.0/operations/{id}/wait` (FR-S13, FR-S14) |
| Own identity (optional) | `GET /1.0/auth/identities/current` | SEC-16 startup warning; skipped when the server does not offer it **[V:L9]** |

Required API extensions **[D]** (names from the LXD API extension list): `projects` and `instance_generation_id`. The list is minimal on purpose: an extension is required only if the plugin calls the feature, so `event_lifecycle_name_and_project` and `api_filtering` are **not** required in v1 (nothing uses events or filtered listing). Fine-grained TLS identities (`access_management_tls`) are a deployment prerequisite for the least-privilege groups of *LXD integration → Least privilege* but are not checked at runtime. The minimum LXD version is fixed by gate L8 (the 5.21 LTS and 6.x series are the candidates).

#### Least privilege

| Mode | Grants |
|---|---|
| all | instance `can_view` on the SPIRE-managed instances |
| `file_pull` | + instance `can_access_files` |
| `config_push` | + instance `can_edit`, restricted projects mandatory in practice |

The SPIRE identity MUST be a dedicated client certificate (or bearer identity, gate L13) in an authorization group scoped to the project(s) of SPIRE-managed instances. Entitlements are as listed in the LXD permission reference **[D]**; exact sufficiency is gate L9.

#### Caching

No caching of outcomes. A cache of at most 10 s for resolved instance names is optional; never across restarts.

#### Minimal API contract

The fields below are the **only** ones the plugin consumes. They are the author's recollection of the LXD API plus the documentation consulted (*Testing → Sources consulted*) and MUST be confirmed by recorded fixtures (gate L15) before the client is written; the fixtures, not this table, are then the contract.

| Call | Fields and headers consumed | Status |
|---|---|---|
| `GET /1.0` | `api_extensions`, `api_version`, `auth`, `environment.server_version`, `environment.server_clustered` | **[V:L8, L15]** |
| `GET /1.0/instances/{name}?project=` | `name`, `project`, `status`, `status_code`, `type`, `architecture`, `location`, `profiles`, `config`, `devices`, `expanded_config`, `expanded_devices`, `created_at`, `last_used_at`; header `ETag` | **[V:L15]** |
| `GET /1.0/instances/{name}/files?path=&project=` | response body; headers `X-LXD-uid`, `X-LXD-gid`, `X-LXD-mode`, `X-LXD-type` (names recalled, not found in the documentation read) | **[V:L1]** |
| `PATCH`/`PUT /1.0/instances/{name}?project=` | request body with `config`; async envelope with `operation`; header `If-Match` | **[V:L14]** |
| `GET /1.0/operations/{id}/wait?timeout=` | operation `status`, `status_code`, `err` | **[D]** envelope, **[V:L14]** endpoint details |
| `GET /1.0/auth/identities/current` | identity type, groups and effective permissions (field names to be confirmed); 404 when the endpoint is absent | **[V:L9, L15]** |
| Error envelope | `type`, `error`, `error_code`; HTTP 400/401/403/404/409/412/500 | **[D]** |

### Observability

- Structured logs: `correlation_id`, `server_id`, `project`, `instance_uuid` (on success), `proof_mode`, `outcome`, `reason_code`. Reason codes: `ok, payload_invalid, instance_not_found, instance_ambiguous, not_running, scope_denied, policy_denied, proof_timeout, proof_mismatch, lxd_unavailable, extension_missing, rate_limited, precondition_failed`.
- Metrics (SPIRE telemetry): `attestations_total{outcome}`, `lxd_request_duration`, `proof_duration`, `inflight`, `claim_only_mode_active`, `config_push_cleanup_failures_total`. No names or UUIDs as labels.
- One audit log line per issued ID including the selectors emitted.

## Configuration

### Agent (`agent.conf`)

```hcl
NodeAttestor "lxd_instance" {
  plugin_cmd      = "/usr/bin/lxd-agent-plugin"
  plugin_checksum = "<sha256>"
  plugin_data {
    claim {
      project       = "spire-nodes"    # optional, strongly recommended
      instance_name = ""               # empty = auto (devLXD meta-data, then hostname)
    }
    use_devlxd      = "auto"           # auto | required | off
    devlxd_socket   = "/dev/lxd/sock"
    proof_dir       = "/run/spire/lxd"
    proof_timeout   = "15s"
  }
}
```

### Server (`server.conf`)

```hcl
NodeAttestor "lxd_instance" {
  plugin_cmd      = "/usr/bin/lxd-server-plugin"
  plugin_checksum = "<sha256>"
  plugin_data {
    agent_path_template = "{{ .PluginName }}/{{ .ServerID }}/{{ .InstanceUUID }}"
    proof_mode          = "file_pull"      # file_pull | config_push | auto
    proof_timeout       = "10s"
    proof_dir           = "/run/spire/lxd"
    reattest            = "allow"          # allow | deny

    policy {
      allow_privileged = false
      deny_config_keys = ["raw.lxc", "raw.idmap"]
    }

    servers = {
      "lxd-prod" = {
        url                     = "https://lxd.example.org:8443"
        client_cert_file        = "/etc/spire/lxd-client.crt"
        client_key_file         = "/etc/spire/lxd-client.key"      # 0400
        server_cert_fingerprint = "sha256:<hex>"                   # or ca_bundle_file
        projects                = ["spire-nodes"]
      }
    }

    selectors {
      enable      = ["server","uuid","project","type","privileged","profile","network"]
      config_keys = []
      user_keys   = []
    }
    limits   { max_inflight = 16, rate_per_sec = 20, failure_cooldown = "5s" }
    dev_mode = false
  }
}
```
Validation at `Configure`: unknown keys rejected; missing pin/CA (SEC-07); inline secrets (SEC-08); `proof_mode="none"` without acknowledgement (SEC-02); required mode/permission combinations that are inconsistent.

### Operational guidance (must appear in user docs)

1. Create a **dedicated LXD identity and authorization group** for SPIRE with only the entitlements of *LXD integration → Least privilege*, scoped to the SPIRE-managed project(s); never use a server administrator credential (T10).
2. Keep SPIRE-managed instances in **dedicated projects**; for `config_push`, set those projects to `restricted=true` with restrictions on devices and privileged containers. A narrow challenge broker (*Alternatives → Narrow challenge broker*) is the recommended pattern for both modes in highly sensitive environments, but it is not available in v1; until it exists, rely on dedicated identities, restricted projects and `file_pull`.
3. Prefer `file_pull`. Do not mount shared storage over `/run` in instances (T2).
4. Run the agent as root; avoid world-accessible `/dev/lxd/sock` for unrelated users where possible; set `security.devlxd=false` on instances that do not need it (T13).
5. Restrict who can edit profiles, `user.*` keys, image properties and network assignments for anything used in registration entries (T9).
6. Pin the LXD server certificate fingerprint; rotate the SPIRE client certificate regularly; keep `server_id` unique across all LXD servers and clusters (T5, T7).
7. Use a short agent SVID TTL; consider `reattest = "deny"`; on restore, rollback or decommission, evict the agent and remove the entries (T14).
8. Firewall the SPIRE server endpoint to the instance networks and the LXD API to the SPIRE server and administrators (T11).
9. Audit LXD lifecycle events whose requestor is the SPIRE identity and that touch anything other than `user.spire.challenge.*` or read operations.

## Testing

### Non-functional requirements

| ID | Requirement |
|---|---|
| NFR-01 | Attestation p95 ≤ 4 s (`file_pull`) and ≤ 6 s (`config_push`) with a healthy LXD; provisional until gates L1/L2 measure them. |
| NFR-02 | Server sustains ≥ 20 attestations/s within the limits of SEC-09. |
| NFR-03 | Agent and server: static binaries, no CGO, linux/amd64 and linux/arm64; agent ≤ 20 MB. A CI check enforces a **dependency allow-list**: the standard library, the SPIRE plugin SDK with its transitive requirements, protobuf/gRPC, and the command line, configuration and logging libraries the repository already uses (`github.com/jessevdk/go-flags`, `github.com/joho/godotenv`, `github.com/hashicorp/hcl`, `github.com/hashicorp/go-hclog`), and the LXD Go client SDK (`github.com/canonical/lxd/client`, `github.com/canonical/lxd/shared/...`) with its transitive requirements; no other package of `github.com/canonical/lxd`. The SDK adds about 5 MB to a binary that imports it; the CI build reports the agent's size against the 20 MB limit. |
| NFR-04 | Fails closed on every error path; no panics on untrusted input (recover and return the uniform error). |
| NFR-05 | Compatible with the SPIRE release that matches the SPIRE plugin SDK pinned in `go.mod` (challenge/response API, `can_reattest`); the end-to-end lab (*Testing → Implementation instructions*, step 7 (e)) runs against that release and records it. |
| NFR-06 | `golangci-lint`, `gosec`, `govulncheck` clean. |

### Tests

- **Unit**: proof computation vectors (including length-prefix edge cases), claim parsing, payload bounds, selector escaping, policy checks, device-overlap check, config validation, uniform error mapping; expected-owner selection per instance class; **idmap translation** (*Proof of co-location → UID translation*), table-driven: single range, several ranges, `Nsid` ≠ 0, range boundaries (`Nsid + Maprange`), 32-bit overflow, missing or garbled `volatile.idmap.current`, `volatile.idmap.next` differing from `.current` (must be ignored), gid-only entries (ignored), overlapping entries (fail closed); a property test that only namespace uid 0 can translate to the accepted owner.
- **Server integration** with a fake LXD API (an `httptest` server implementing instances, files, operations and ETag semantics that **replays recorded fixtures**, gate L15; it MUST NOT define behaviour that no fixture shows); cases: happy path per mode; ambiguous or missing instance; project outside the allow-list; stopped/frozen; privileged; wrong owner or mode; wrong content; late proof; uuid or generation change between proof and re-check; operation failure; extension missing; rate limiting; **412 handling (FR-S14)**: retry succeeds, retries exhausted (`precondition_failed`, uniform error), status/uuid/generation/project/name changed between retries aborts, retry budget bounded by `proof_timeout`, 412 on the cleanup write, and the `PUT` variant if gate L14 requires it.
- **LXD client** (FR-S17), testing the wrapper's own rules against the fake LXD API: pin match and mismatch, rotated client certificate reload, TLS version below 1.3 refused, redirect refused, oversize body refused, explicit project on every call, `If-Match` sent from the captured `ETag`, `RawQuery` calls decoded, missing consumed field rejected, async wait timeout, error mapping to internal kinds, no credentials in logs. Envelope parsing and unknown-field tolerance belong to the SDK and are covered by the fixtures (contract tests).
- **Contract**: every call of *LXD integration → API usage and required extensions* decoded from real fixtures of each supported LXD version; a live smoke job against a real LXD where CI allows.
- **Abuse tests** mapped to threats: attacker instance proves with its own file (T1); replay of an earlier payload and response (T3); concurrent attestations of one claim (T11); uniform error bytes (T12); malformed inputs and fuzzing (T18); shared-disk overlap (T2).
- **Agent tests** with a temporary directory tree and a fake devLXD socket: permission and symlink refusals, `O_EXCL` collisions, non-root refusal, oversized responses.
- **End to end** (lab): LXD 5.21 LTS and current 6.x; Ubuntu, Debian and Alpine guests; containers (unprivileged and privileged) and VMs; standalone and clustered; each mode.
- **Acceptance**: every FR/SEC ID referenced by a test; gates L1–L15 recorded in `docs/validation/`; coverage ≥ 80 % on parsing/proof/policy packages.

### Validation gates (to resolve with a spike **before** implementing the dependent code)

| ID | Assumption to verify on real LXD, containers and VMs |
|---|---|
| L1 | The files API returns owner uid/gid, mode and type for a file in `/run` of a container and of a VM (requiring the `lxd-agent`); pulls work on tmpfs; measure latency. **Decide the owner representation for unprivileged containers: does LXD report the in-container uid (0) or the host-shifted uid?** Record it per instance class and LXD version; it fixes the single expected owner of *Proof of co-location → Mode `file_pull`*. Confirm the key names and semantics of `volatile.idmap.base` / `current` / `next` and cross-check `volatile.idmap.current` against the guest's own `/proc/self/uid_map`. Check how symlinks and directories are reported. |
| L2 | `PATCH` of a `user.*` key on a **running** instance is accepted without restart (the option reference says live update "No"), is visible immediately at `/1.0/config/<key>` through devLXD, and its operation completes quickly; whether devLXD emits a config event. |
| L3 | Content of devLXD `/1.0/meta-data` and `/1.0/config` listing (instance-id, local-hostname; is the instance name or project obtainable at all?). The public devLXD API description lists `/1.0` fields `api_version, instance_type, location, state, auth` only. |
| L4 | Which in-instance UIDs can open `/dev/lxd/sock` (container, unprivileged and privileged; VM through `lxd-agent`). |
| L5 | *Withdrawn in v0.2 (was `ready_signal`). ID kept so that references stay stable.* |
| L6 | `volatile.uuid`, `volatile.uuid.generation`, `volatile.cloud-init.instance-id`, `volatile.base_image` across: copy, move, rename, snapshot restore, backup export/import on another server, instance recovery. Is a duplicate `volatile.uuid` possible across servers? |
| L7 | VMs: availability and start-up order of `lxd-agent` per image family, Windows guests, `security.devlxd` interplay, impact on `file_pull` and `config_push`. |
| L8 | Minimum LXD versions (5.21 LTS vs 6.x) for each required API extension and for `volatile.cluster.group`; snap and deb differences. |
| L9 | The exact entitlement set sufficient for each mode; whether the SPIRE identity can discover its own privileges (to implement the SEC-16 warning); behaviour of **restricted projects** with `can_edit`. |
| L10 | **Client coverage.** The licence question is closed (v0.4): `client/COPYING` and `shared/COPYING` of the LXD repository are Apache-2.0 **[D]**, and only those packages are imported. Remaining: confirm that the SDK, directly or through `RawQuery` (FR-S17 (9)), covers every call of *LXD integration → API usage and required extensions* (`lxd-probe` is its first consumer); record the pinned SDK version; confirm that the dependency allow-list of NFR-03 holds. |
| L11 | Clustering: API forwarding for files and instance updates, `location` accuracy, behaviour during migration and evacuation. |
| L12 | Shared disk devices, bind mounts and nested LXD: can another instance write to the proof path; effectiveness of the overlap check. |
| L13 | Whether LXD bearer identities (introduced in 6.6/6.7 for devLXD; "alternative to certificates" in the release notes) are usable by an external client for the remote API, with short-lived tokens, as a replacement for long-lived client certificates. |
| L14 | Instance-update semantics: synchronous or background, wait endpoint and timeouts; whether `PATCH` honours `If-Match` (the documentation describes it for `PUT`); that a stale ETag yields 412; **how a single `user.*` key is removed** (`PATCH` with an empty value, or only `PUT` of the whole object); whether the ETag also changes on LXD-internal `volatile.*` updates of an idle running instance, and how often (spurious 412 rate); behaviour of concurrent writers. |
| L15 | **Fixtures.** Record real responses for every call of *LXD integration → API usage and required extensions* from LXD 5.21 LTS and 6.x (unprivileged and privileged container, VM, clustered), including the error cases 403, 404 and 412, into `test/fixtures/`. The fake LXD API replays them; `lxd-probe util fixtures verify` checks that the client's typed structs decode them. |

Evidence for each gate is produced with `lxd-probe` (its own specification, one runbook per gate) and stored in `docs/validation/`; when a gate disproves an assumption, this specification is amended before the dependent code is written.

### Implementation instructions (for the implementing agent, Claude)

1. Do **not** implement `file_pull`, `config_push`, the `internal/lxd` client or the owner/UID logic before the matching gates are resolved or waived in writing: L1 (owner representation, idmap keys), L2 and L3 (`config_push` and claim collection), L9 (privilege statements), L10 (client coverage), L14 (update, ETag and delete semantics), L15 (fixtures). Parts that do not depend on gates may start immediately: proto, claim parsing, proof computation, selectors, limits, policy skeleton.
2. Build **`lxd-probe`** first, from its own specification (`lxd-probe.md`): it runs the experiments of gates L1–L15, records evidence and the fixtures that this project's fake LXD API must replay, and is the first consumer of `internal/lxd`. Its findings are folded back into this specification (next revision) before the dependent code is written.
3. Keep requirement IDs in test names and comments.
4. Treat every value from LXD, devLXD, the guest filesystem and the agent as untrusted; no untrusted data in error strings, shell commands or log format strings.
5. All failure paths return the uniform error (SEC-10).
6. Prefer small interfaces (`InstanceReader`, `FileReader`, `ChallengeWriter`, `DevLXDClient`) so each part is mockable. Use the LXD Go client SDK only through `internal/lxd` and `internal/devlxd` (FR-S17), so that the rest of the code depends on the small interfaces, not on the SDK.
7. Delivery order: (0) `lxd-probe` and the gate evidence, then a specification revision; (a) proto, claims, proof, policy skeleton, selectors, fuzz; (b) the `internal/lxd` client against recorded fixtures, then the server with the fake LXD (`file_pull`, with the owner rule decided by L1); (c) agent `file_pull`; (d) `config_push` with the ETag/412 logic as fixed by L14; (e) end-to-end lab; (f) packaging and documentation.
8. When a gate disproves an assumption tagged **[V:Ln]**, propose a spec amendment instead of silently diverging.

### Sources consulted

Documentation read for this draft (LXD 6.9 documentation unless noted); statements tagged **[D]** rely on these:

- [Communication between instance and host (devLXD)](https://documentation.ubuntu.com/lxd/latest/dev-lxd/)
- [How to authenticate to the DevLXD API](https://documentation.ubuntu.com/lxd/latest/howto/devlxd_authenticate/)
- [devLXD OpenAPI description](https://raw.githubusercontent.com/canonical/lxd/main/doc/devlxd-api.yaml)
- [Instance options (including `volatile.*`, `user.*`, `security.devlxd*`)](https://documentation.ubuntu.com/lxd/latest/reference/instance_options/)
- [Permissions reference (entitlements)](https://documentation.ubuntu.com/lxd/latest/reference/permissions/)
- [Remote API authentication](https://documentation.ubuntu.com/lxd/latest/authentication/)
- [Events](https://documentation.ubuntu.com/lxd/latest/events/)
- [API extensions](https://canonical.com/lxd/docs/latest/api-extensions/)
- [LXD 6.6 release notes](https://github.com/canonical/lxd/releases/tag/lxd-6.6) and [LXD 6.7 release notes](https://newreleases.io/project/github/canonical/lxd/release/lxd-6.7)
- [cloud-init LXD datasource](https://docs.cloud-init.io/en/latest/reference/datasources/lxd.html)
- [LXD now re-licensed and under a CLA](https://stgraber.org/2023/12/12/lxd-now-re-licensed-and-under-a-cla/) (AGPL-3.0 from 2023-12-12; earlier Go packages Apache-2.0)
- LXD repository licence files, read from the Go module of Oct 9, 2026: `COPYING` (AGPL-3.0-only), `client/COPYING` and `shared/COPYING` (Apache-2.0), and the README section *Client SDK packages* ("These SDKs are licensed as Apache-2.0")
- [How `security.idmap.isolated` works (volatile.idmap.base/current/next)](https://discuss.linuxcontainers.org/t/how-security-idmap-isolated-works-in-detail/13266) (forum thread, not official documentation)
- [LXD REST API conventions (return values, ETag/If-Match for PUT, PATCH vs PUT, background operations)](https://raw.githubusercontent.com/canonical/lxd/main/doc/rest-api.md)

Items not found in these sources are tagged **[V:Ln]** and listed in *Testing → Validation gates*. Notable gaps: the contents of `/1.0/meta-data`, the files API headers and the owner representation for unprivileged containers, live update of `user.*` keys, `If-Match` on `PATCH` and single-key removal, VM SMBIOS identifiers, Windows guest support, and the precise privilege boundary of each entitlement.
