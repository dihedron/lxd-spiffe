# Threat model

The threat model follows STRIDE (spoofing, tampering, repudiation, information disclosure, denial of service, elevation of privilege). It lists what the design protects, against whom, every threat considered with its countermeasures, and the risk that remains.

## Assets

| ID | Asset | Why it matters |
| ----- | ---------------------------------- | -------------------------------------------------- |
| AS1 | The node SVID, the node identity | The gateway to the workload SVIDs of everything in the instance |
| AS2 | SPIRE's LXD credential | Read access to instances, and file or configuration access depending on the mode |
| AS3 | The LXD API's availability | Shared by every administrator and tool |
| AS4 | SPIRE Server's availability | The attestation endpoint is reachable before authentication |
| AS5 | The truth of the selectors | Registration entries authorize workloads by them |
| AS6 | The integrity of the plugin binaries and their configuration | A compromise means forged identities |

## Actors

| ID | Actor | Capability |
| ----- | ---------------------------------- | -------------------------------------------------- |
| A1 | Remote network attacker | Reaches the SPIRE Server port |
| A2 | Malicious instance on the same LXD host or cluster | Root in another instance; may know a victim's name |
| A3 | Unprivileged user in the legitimate instance | Runs code; cannot read root-only data |
| A4 | Root in the legitimate instance | Controls that node entirely |
| A5 | LXD user with instance-level edit rights | Creates, copies, renames, edits configuration and profiles, imports: can create lookalikes and change R2 data |
| A6 | Network attacker in the middle | Intercepts or alters agent-to-server or server-to-LXD traffic |
| A7 | LXD administrator, compromised host or daemon | Controls every claim |
| A8 | Backup or image operator | Can import exported instances, with duplicate identifiers |
| A9 | Supply-chain attacker | Tampers with dependencies, the build or the release |
| A10 | Holder of SPIRE's stolen LXD credential | Acts as SPIRE towards LXD |

## The threat register

::: landscape

| ID | Threat | Actor | STRIDE | Countermeasures | Residual |
| ---- | -------------------------- | ------- | ------- | ------------------------------------------------------------------ | -------------- |
| T1 | **Claim spoofing**: an agent in instance Y claims X's project and name | A2 | S | The proof of co-location: the challenge can only be answered through X's filesystem or X's devLXD; Y can only write and read its own | Low |
| T2 | **Proof forged through shared storage**: X and Y share a disk and Y writes X's proof | A2, A5 | S | Proof under tmpfs `/run`; owner and mode checked; the isolation check refuses non-root disks at, above or below the proof directory. Shared disks did let another instance forge the proof in the lab, and the check refused every case | Low |
| T3 | Replay of a payload or response | A1, A6 | S | 256-bit server nonce, single use, valid for `proof_timeout` (at most 2 minutes); the proof binds both nonces and the claim | Negligible |
| T4 | Attacker between agent and SPIRE Server | A6 | S, T, I | SPIRE's TLS with the trust bundle; `insecure_bootstrap` voids this protection and must not be used; a proof is useless outside the instance | Depends on bootstrap hygiene |
| T5 | Attacker impersonating the LXD API | A6 | S, T, I | TLS 1.3 with the server certificate pinned or verified against a CA; mutual TLS; only the explicit development mode skips verification, with a warning on every connection | Low |
| T6 | **Name collision**: one instance name in several projects or servers | A5 | S | Project allow-list; exactly one match across servers and projects; cloud-init ID consistency; the ID built from `volatile.uuid` | Low |
| T7 | **Duplicate UUID** after a backup import, on another server or the same one: a twin would get the original's SPIFFE ID | A5, A8 | S | `server_id` in the ID; the uniqueness check refuses when more than one instance of the allowed projects has the UUID (needs project `can_view_instances`); operators keep `server_id` unique. Imports keep the UUID (lab-verified) | Low |
| T8 | Instance stopped, frozen, migrated, restored or renamed during the attestation | A5, A7 | S, T | `Running` required; the post-proof read compares UUID, generation, project, name and location; ETags on writes | Low |
| T9 | **Selector forgery** by users with edit rights (profiles, `user.*`, image properties, networks) | A5 | E, T | A trust level per selector; R2 selectors medium-trust; `config` and `user` keys allow-listed; guidance on LXD access control and restricted projects; every issued selector logged | Medium: depends on access control discipline |
| T10 | **An over-privileged SPIRE credential** amplifies a compromise of SPIRE Server | A10 | E | Least-privilege grants per mode; dedicated identity and projects; restricted projects for `config_push`; credentials from permission-checked files, a short-lived bearer token possible on 6.x; a warning when the identity looks like an administrator; audit of LXD events requested by the SPIRE identity | Medium: inherent to LXD's coarse entitlements |
| T11 | Denial of service of the LXD API through unauthenticated attestation attempts | A1 | D | Syntactic validation first; concurrency cap, token bucket, per-claim cooldown, circuit breaker; firewall the SPIRE port to instance networks | Medium |
| T12 | Enumeration: does instance X exist? | A1 | I | One uniform error for every failure; detail in SPIRE Server's log only | Low |
| T13 | **An unprivileged user in the instance attests as the node** | A3 | S, E | `file_pull` needs root (0700 directory, owner uid 0); `config_push` needs devLXD, which only root can use; the agent must run as root; `reattest = "deny"` where wanted | Low, while LXD restricts devLXD to root (checked per LXD version) |
| T14 | **Rollback, clone or restore** keeps or duplicates an identity | A5, A8 | S | A copy gets a new UUID; an import keeps it (T7); the generation is compared and available as a selector; short agent SVID lifetime; evict agents on restore. A copy that inherits the agent's data (its SVID and key) never attests: its agent loads the original's SVID and its workloads receive the original's workload SVIDs (lab-verified), so nothing in the plugin can refuse it | Medium: a copy with the agent's data is the original's node until the agent is evicted |
| T15 | **Privileged containers or low-level overrides** weaken the boundary | A4, A5 | E | `privileged` selector; privileged containers refused by default; `deny_config_keys` (`raw.lxc`, `raw.idmap`, ...) | Low if configured |
| T16 | Challenge visible to LXD viewers (`config_push`), or a proof file readable | A5, A7 | I | Single-use, short-lived values; the proof is a hash; key and file removed after use (best effort) | Low |
| T17 | Compromised or malicious LXD daemon or host | A7 | all | Not preventable: the host is trusted. Separate credentials, correlation of LXD's and SPIRE's audit records by `volatile.uuid`, network restrictions | Accepted |
| T18 | Parsing attacks: LXD JSON, devLXD answers, the proof file, protobuf | A1, A2 | T, D | Size caps; strict, typed decoding; fuzz tests of every parser | Low |
| T19 | Plugin or dependency tampering | A9 | T | `plugin_checksum`; pinned dependencies; SBOM; `govulncheck`; signed checksums and packages | Low |
| T20 | **LXD API drift or missing extensions** cause silent misbehaviour | — | D, T | Required extensions checked on every connection; fail closed; contract tests against fixtures recorded from each supported version | Low |
| T21 | Cluster migration or evacuation during an attestation | A7 | D | Calls forwarded by every member; `Running` checked on both reads; a move between the reads refused (`instance_moved`); a VM's slow files API after evacuation handled as start-up; the agent retries. A files API read of a *container* during its evacuation makes the evacuation fail and leaves its volume behind (an LXD defect, measured on 5.21.8, 6.9 and 6.10.0). Live migration untested | Operational |
| T22 | `config_push` races: the agent reads before the update completes, or the cleanup never completes | — | D | Synchronous `PATCH` with ETag, 412 handled; failed cleanups logged; stale keys removed by the instance's next attestation | Low |
| T23 | Leaks through logs or telemetry | any | I | Nonces, proofs, challenge values and credentials never logged; no identifiers as metric labels | Low |
| T24 | **Nested LXD** (an instance that hosts LXD) misattributed | A5 | S | The node is the innermost instance; each LXD is configured for the instances it really manages | Operational |

:::

## Security requirements

The design's security requirements, which the code and its tests trace by ID:

| ID | Requirement |
| -------- | ------------------------------------------------------------------------------------- |
| SEC-01 | No identity on a claim alone: a proof of co-location is mandatory unless SEC-02 is explicitly overridden. |
| SEC-02 | `proof_mode = "none"` is refused unless acknowledged as insecure, and then warns on every attestation. |
| SEC-03 | Nonces: 32 bytes from a cryptographic random source, single use, compared in constant time; a server nonce lives at most its challenge's `proof_timeout`, at most 2 minutes. |
| SEC-04 | Proof paths and keys are derived from the agent's nonce; the client chooses no name. |
| SEC-05 | No match or several matches, including several instances with the resolved `volatile.uuid`, fail closed. |
| SEC-06 | State, identity and generation are verified again after the proof, before the answer. |
| SEC-07 | TLS 1.3 to LXD, the certificate pinned or verified against a CA, mutual TLS or a bearer token; only the development mode bypasses verification, with a warning. |
| SEC-08 | Credentials come from files with permission checks. |
| SEC-09 | Concurrency cap, token bucket and per-claim cooldown, all before any LXD call; over a limit, refused at once. |
| SEC-10 | Uniform failure towards the agent; detail only in SPIRE Server's log, with a correlation ID. |
| SEC-11 | Every untrusted input bounded (payload 4 KiB, strings 128 bytes, proof 64 bytes, LXD answers 1 MiB); typed decoding; fuzz targets for every parser. |
| SEC-12 | The documents recommend an agent SVID lifetime of one hour or less and explain `reattest`. |
| SEC-13 | Release pipeline: pinned dependencies, `govulncheck`, SBOM, signatures, reproducible build flags. |
| SEC-14 | The documents state that `insecure_bootstrap` voids the protection against T4. |
| SEC-15 | Selectors and ID components come only from LXD's R1 and R2 data; guest-asserted values are never selectors. |
| SEC-16 | The server writes only its own `user.spire.challenge.<id>` key (and removes stale ones), reads only the proof path it derived, and warns when its LXD identity looks like an administrator. |

## Accepted risks

- **T17**: the LXD host and daemon are trusted. An LXD administrator or a compromised host can make any instance look like any other.
- **T9 and T10** remain medium: they depend on how tightly LXD's access control is run, and on LXD's coarse entitlements (`can_access_files` allows writes too; `can_edit` reaches every setting). A narrow challenge broker would reduce T10 (*Design decisions and alternatives*).
- **T14**: a copy or restore that carries the agent's data is the original node until its agent is evicted. The *Operator's Guide* gives the procedure; a short SVID lifetime bounds the window.
