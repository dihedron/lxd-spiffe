# LXD validation probe (`lxd-probe`) — implementation spec

Oct 8, 2026 · @Andrea Funtò

*Status*: draft v0.3, for spec-driven development. `lxd-probe` is step 0 of the attestor spec's implementation order (`lxd-spire-plugins.md`, *Testing → Implementation instructions*): it is built and run before any plugin code that depends on a validation gate. Nothing of it is implemented yet.

| Field | Value |
|---|---|
| Status | Draft v0.3 for spec-driven development (SDD) |
| Purpose | A lab tool that **observes real LXD behaviour** and records it as evidence, so that every `[V:Ln]` validation gate of the attestor spec is closed with data instead of assumptions. |
| Companion of | `lxd-spire-plugins.md` (the "attestor spec") |
| Binary | `lxd-probe` |
| Language | Go, same module as the attestor (`cmd/lxd-probe`) |
| Platforms | Linux on amd64 and arm64 only (PRN-22). `guest` environment: Linux containers and VMs. `server` environment: a Linux operator workstation or the SPIRE server host |
| Dependencies | Allow-list of NFR-03, which includes the Apache-2.0 LXD Go client SDK (`github.com/canonical/lxd/client`, `github.com/canonical/lxd/shared/...`); no CGO; static binary |
| Output | Human-readable text by default; `--json` for machine output; evidence files always JSON |
| Network | `guest` environment uses **no network** (only `/dev/lxd/sock` and local files). `server` environment talks only to the configured LXD endpoint |
| Secrets | Credentials never written to evidence, logs or the command line (*Trust model → Credentials and logging*) |

**Keywords.** MUST, MUST NOT, SHOULD, MAY are used per RFC 2119. Requirement prefixes: PRF (probe functional, guest), PRS (server), PRU (util), PRN (non-functional), PRT (tests), PRE (evidence). Validation gates (`L*`) are those of the attestor spec (*Testing → Validation gates*); references into the attestor spec name its sections.

**Revision history.**

| Version | Changes |
|---|---|
| v0.1 | Initial draft. |
| Oct 8, 2026 | Moved from `lxprobe-spec-v0.1.md` and reorganised into the sections of the attestor spec (Overview, Trust model, Guest environment, Server environment, Util environment, Configuration, Testing) without changing requirements, except: references by section name instead of number; the header names belong to the attestor spec's minimal API contract (L1); the L6 decision rule follows FR-S7 (`server_id` already in the ID) and FR-S10 (generation); uniqueness is FR-S3/SEC-05 (PRS-12); PRF-12 no longer cites the withdrawn L5; committed evidence in `docs/validation/`, fixtures in `test/fixtures/`; the assumption-based fake is limited to lxprobe's own tests (PRT-10); the secrets constraint points to the credential rules; PRU added to the requirement prefixes. |
| Oct 9, 2026 | The probe is renamed `lxd-probe` (binary, `cmd/lxd-probe`); lab config keys become `user.lxd-probe.*`, the guest lab directory `/run/lxd-probe/`, and the bearer token variable `LXD_PROBE_TOKEN`. No other changes. |
| v0.2 (Oct 9, 2026) | Review fixes before implementation. Command line: go-flags and the repository's command structure (PRN-23, PRN-30–PRN-34), logging through `LXD_PROBE_*` variables instead of `-v`/`-vv`, exit code 1, `version` reuses `internal/command/version`, flag tables per environment and per verb. Evidence: expectation table defined (PRE-04, PRE-06), per-environment run directories and `O_EXCL` record numbering (PRE-07), aliases in `aliases.json` (PRE-03, PRE-09), raw plus typed decoding (PRE-08). Safety: `--allow-outside-lab-dir` removed (PRF-20), `--allow-write`/`--lab` apply to guest writes too, PRS-24 compares `volatile.uuid`. `--allow-tls12` is a probe-only exception and Q3 moves to L8 (PRS-03). PRF-13 re-executes the binary through a hidden command. Linux on amd64 and arm64 only, no Windows guests; FR-S17 scope of the probe (PRN-24); separate archives, no deb/rpm (PRN-22). Shared options live in embedded base structs, flags follow the verb, and session options default from `LXD_PROBE_*` variables (PRN-31, PRN-32). PRU-07/PRU-08: the report never matches decision rules; the operator records the decision with `util note --decision`. |
| v0.3 (Oct 9, 2026) | Follows attestor spec v0.4: `internal/lxd` and `internal/devlxd` wrap the LXD Go client SDK (principle 5, PRF-14, PRN-24); observations use the SDK's raw access so that headers and bodies are recorded as sent. The TLS 1.2 question becomes a separate handshake of `server info --probe-tls12` (PRS-03), which replaces `--allow-tls12`. PRT-11 tests the wrapper's own rules. L10 keeps only the coverage check. |

## Overview

This spec defines `lxd-probe`, a lab tool that **observes real LXD behaviour** and records it as evidence, so that every `[V:Ln]` validation gate of the attestor spec is closed with data instead of assumptions.

`lxd-probe <environment> [<object>] <verb> [arguments] [flags]` (*Configuration → Command line → Command structure*)

| Environment | Where it runs | What it can see |
|---|---|---|
| `guest` | inside the instance (container or VM) | devLXD socket, local files, `/proc` |
| `server` | on an operator workstation or the SPIRE server host | the LXD REST API (remote or local socket) |
| `util` | anywhere | offline helpers on evidence files |

### Why this tool exists

The attestor spec contains documented facts `[D]` and unverified assumptions `[V:Ln]` (gates L1–L15). Building the plugins before the gates are closed risks building on wrong assumptions (e.g. which UID LXD reports for files in unprivileged containers; whether `PATCH` honours `If-Match`; how a single `user.*` key is removed). `lxd-probe` is built **first** (step 0 of the attestor roadmap) to remove that uncertainty.

### Principles

1. **Observe, do not assume.** The tool never "fixes" or interprets an unexpected response: it records it verbatim (sanitized) and reports it.
2. **Evidence is the product.** Every command produces a machine-readable record; a run produces an evidence directory that can be committed (after sanitization) and replayed as test fixtures (gate L15).
3. **Lab only.** The tool performs writes (to `user.lxd-probe.*` keys and to `/run/lxd-probe/`) and must refuse to run write verbs without explicit acknowledgement. It is not a production component and is not shipped with the plugins.
4. **Share code with the attestor, not conclusions.** `lxd-probe` uses the same `internal/lxd` client (FR-S17), `internal/policy` (idmap translation, device overlap) and `internal/proof` packages that the plugins will use. A bug found in the probe is a bug found in the future plugin; a fixture recorded by the probe is the plugin's test input.
5. **No new dependencies.** Same module, Go standard library plus the dependencies allowed by NFR-03, which include the LXD Go client SDK the plugins use. No CGO.
6. **Findings feed the spec.** Each gate runbook (*Testing → Gate runbooks*) states which spec text is amended for each possible outcome.

### Evidence model

#### Layout

```
evidence/<run_id>/
  <env>/                # one run directory per environment: guest/, server/, util/ (PRE-07)
    run.json            # run metadata: tool version, git commit, host, LXD endpoint (no credentials), flags
    records/            # one JSON file per command execution: NNNN-<verb>.json
    artifacts/          # raw bodies, file contents (hashes + optional content for small files)
    fixtures/           # sanitized request/response pairs (when --record is used)
    notes.jsonl         # operator notes
    aliases.json        # name → alias map for --redact-names (PRE-09); never committed
```

The guest and server environments usually run on different machines. The operator copies the guest's `evidence/<run_id>/guest/` next to the server's `evidence/<run_id>/server/`; `util report` merges the environments of a run by record time.

Working runs write to `--evidence-dir` (default `./evidence`). Sanitized evidence is committed under `docs/validation/<run_id>/`, the location the attestor spec names, and fixtures under `test/fixtures/<lxd-version>/`.

#### Record schema (PRE)

| ID | Requirement |
|---|---|
| PRE-01 | Every record contains: `schema` (integer), `id`, `time` (UTC RFC3339), `tool_version`, `env`, `verb`, `args` (sanitized), `gate`, `cell`, `outcome` (`ok`, `unexpected`, `error`), `duration_ms`, `observations` (structured key/values), and `raw` (reference to artifact). |
| PRE-02 | The `server` records include the LXD `version` and `api_extensions` hash, so that a record can always be attributed to a server version. |
| PRE-03 | **Sanitization (mandatory, applied at write time, not afterwards):** remove `Authorization` and cookie headers; remove client certificates/keys and any bearer tokens; redact the values of `cloud-init.*`, `user.*` (except `user.lxd-probe.*` and `user.spire.challenge.*`) and `environment.*` config keys; replace MAC addresses and, when `--redact-names` is set, instance and project names with stable aliases (PRE-09). Apart from the alias map of PRE-09, the unsanitized data is never persisted. |
| PRE-04 | `outcome: unexpected` is assigned whenever the status code, a header, or a body shape differs from the tool's built-in expectation table (PRE-06). This is a finding, not an error: the exit code is still 0 unless `--strict` is given (exit 8). |
| PRE-05 | Evidence files are written with mode 0600, directories 0700. |
| PRE-06 | **Expectation table.** Each verb that calls an API has one or more expectations, kept as data in `internal/probe` (not as code paths). An expectation names the call (method and path template) and lists: the accepted status codes; the headers that must be present (for example `ETag` on an instance read, at least one `X-LXD-*` header on a files read); and the body shape (the envelope `type`, `sync`, `async` or `error`, and the `metadata` fields that must be present). With `--expect-denied`, 403 is the accepted status instead. The record names the expectation it was checked against, and each deviation becomes an observation (`expectation.status`, `expectation.header.<name>`, `expectation.body.<field>`). Calls without an expectation are recorded with outcome `ok`. |
| PRE-07 | **Record numbering.** Each environment writes to its own run directory (`evidence/<run_id>/<env>/`). Within it, the record number `NNNN` is allocated by creating `records/NNNN-<verb>.json` with `O_CREAT\|O_EXCL`, starting at one plus the highest existing number and retrying with the next number on `EEXIST`, so concurrent invocations on the same machine never overwrite each other. Fixture and artifact names reuse the record's number. |
| PRE-08 | **Raw and typed.** Every `server` response is stored raw (sanitized artifact) **and** decoded with the typed structs of `internal/lxd`. A decode failure is not a command error: it is recorded as an observation (`decode.error`) with outcome `unexpected`, and the raw body remains the evidence (gate L15). |
| PRE-09 | **Aliases.** `--redact-names` keeps its name → alias map in `aliases.json` (mode 0600) in the environment's run directory, so that a name has the same alias in every record of the run. Aliases are not stable across runs. `aliases.json` is the only file that holds unsanitized names: `util sanitize` leaves it out of the sanitized copy, and `util sanitize --check` fails if it is present. |

#### Fixtures

| ID | Requirement |
|---|---|
| PRE-10 | With `--record DIR`, every HTTP exchange of the `server` environment is stored as `NNNN.request.json` and `NNNN.response.json` (status, selected headers, body) after sanitization, indexed by method + path + query, with the LXD version in the file name. |
| PRE-11 | Fixtures include the error cases 403, 404, 412 and the `operation` envelopes (sync/async/error), which the lab runbooks provoke on purpose. |
| PRE-12 | A fixture set can be replayed by the fake LXD API of the test suite (PRT-10) with no network. |

### Repository layout and build

```
cmd/lxd-probe            main, init (logging, .env, profiling), command/ (go-flags command tree)
internal/lxd             wrapper around the LXD Go client SDK (FR-S17): the same package the plugin uses
internal/devlxd          devLXD client on the SDK's ConnectDevLXD (guest)
internal/policy          idmap translation (attestor spec *UID translation*), device overlap, owner rules
internal/proof           nonce, path derivation, challenge handling
internal/evidence        record writer, sanitizer, report renderer
internal/probe           command implementations (guest, server, util)
test/fixtures            sanitized real responses (output of lab runs, gate L15)
test/fakelxd             in-process fake LXD that replays fixtures
```

Build: same module, toolchain and goreleaser configuration as the plugins (attestor spec *Overview → Repository layout and build*), as separate archives for its own platforms (PRN-22); `lxd-probe` is never part of the plugin packages (*Overview → Principles*, item 3).

| ID | Requirement |
|---|---|
| PRN-20 | Packages `internal/lxd`, `internal/policy` and `internal/proof` must not import anything from `internal/probe` or `internal/evidence`; the dependency direction is probe → shared packages. |
| PRN-21 | `lxd-probe` has no persistent state outside the evidence directory. |
| PRN-22 | Static binary for linux/amd64 and linux/arm64 only, like the plugins; no macOS or Windows builds, and no Windows guests. Size target: ≤ 15 MB. Released as its own goreleaser archives (`lxd-probe_<version>_<os>_<arch>`); no deb or rpm packages. |
| PRN-23 | The command line is parsed with `github.com/jessevdk/go-flags` and follows the structure of the repository's other binaries (*Configuration → Command line → Command structure*); no other command line library is used. |
| PRN-24 | The probe uses `internal/lxd` (FR-S17 of the attestor spec) with every rule except the reload of the client certificate when its files change (a probe invocation is short-lived). To observe rather than interpret, the probe's calls go through the SDK's raw access (`RawQuery`, or `DoHTTP` where the full response with all headers is needed, PRS-30) on the wrapper's connection, so pinning, project and limits still apply; the typed `shared/api` structs are then used for decoding (PRE-08). One probe-only deviation is per call and never changes the plugin's defaults: the files API limit follows `--max-bytes` (PRS-31). |

### Non-goals

- Not an attestor, not a SPIRE plugin, no SPIRE dependency.
- Not a load or performance benchmark tool (latency is recorded, not optimized).
- Not a security testing/exploitation tool. Negative privilege tests are performed by the operator with `lxc` and recorded via `lxd-probe util note` (*Trust model → Manual tests*).
- Not a substitute for the T-series threat tests of the attestor spec.

## Trust model

`lxd-probe` runs against lab LXD servers only (*Overview → Principles*, item 3). It holds an LXD credential and can write to instances and to guest files, so its safety rules are its trust model: what it may modify, how it treats credentials, and which tests stay manual.

### Writes

| ID | Requirement |
|---|---|
| PRN-01 | Every verb that modifies an LXD server or the guest filesystem is classified `write`; it refuses to run without `--allow-write` and `--lab`. |
| PRN-02 | Writes are limited to: config keys `user.lxd-probe.*` (and `user.spire.challenge.*` with `--allow-spire-keys`); guest paths under `/run/lxd-probe/` (and `/run/spire/lxd/` with `--allow-spire-paths`). Any other target is rejected before a request is sent. |
| PRN-03 | The tool never performs state-changing instance operations (start, stop, freeze, restore, delete, snapshot, copy, move, publish). Experiments requiring them (gate L6) are performed by the operator with `lxc`, and observed with `server instance watch` and `util note`. |
| PRN-04 | With `--dry-run`, a write verb prints the request it would send (or, in the guest, the file operation) and exits 0 without acting. Read-only verbs ignore it. |

### Credentials and logging

| ID | Requirement |
|---|---|
| PRN-10 | Credentials are accepted only from files or environment, never from command-line values; they are never written to logs, records or evidence. |
| PRN-11 | Log output at every level (`LXD_PROBE_LOG_LEVEL`, *Configuration → Command line → Command structure*) uses the same sanitizer as evidence. |
| PRN-12 | The binary refuses to start with `--auth bearer` if the token file is group- or world-readable. |

### Manual tests

Negative privilege tests (for example, "a client with only `can_view` receives 403 on file pull") are performed by the operator with `lxc` and recorded using `util note`; `lxd-probe` provides the matrix scripts as documentation in *Configuration → Operational guidance → Lab setup*, not as automated attacks.

## Guest environment

### Preflight

| ID | Requirement |
|---|---|
| PRF-01 | `guest preflight` records: effective uid/gid, `/proc/self/uid_map` and `gid_map`, kernel version, presence and mode/owner of `/dev/lxd/sock`, whether a `lxd-agent` process exists (VM), mount type and options of `/run`, instance type guess (container/VM) from `/proc` and `systemd-detect-virt` **if present** (absence is recorded, not an error). Supports gates L1, L4, L7. |
| PRF-02 | Preflight is read-only and needs no privileges beyond those of the invoking user. |

### devLXD

| ID | Requirement |
|---|---|
| PRF-10 | `guest devlxd get PATH` performs `GET` on the devLXD socket and records status, headers, body (sanitized), latency. Paths used by runbooks: `/`, `/1.0`, `/1.0/config`, `/1.0/config/<key>`, `/1.0/meta-data`, `/1.0/devices`, `/1.0/events` (when available). |
| PRF-11 | `guest devlxd dump` walks all known read paths (PRF-10 list) and stores one record per path. Unknown or failing paths are recorded with their status, not skipped. |
| PRF-12 | `guest devlxd watch KEY [--for DUR]` polls `/1.0/config/KEY` (and uses the events endpoint when it exists) and records timestamps of each change, to measure propagation delay after a host-side `config set` (gate L2). |
| PRF-13 | `guest devlxd access --as-uid N[,N...]` performs the preflight `GET /1.0` as each uid and records allowed/denied (gate L4). It requires root in the guest. For each uid, the probe re-executes its own binary (`/proc/self/exe`) with `SysProcAttr.Credential` set to that uid (and its primary gid, or the same number when the uid has no passwd entry), running the hidden command `guest devlxd access-child`. The child performs the call and writes its result as JSON on stdout; the parent writes the record. Failure to switch uid or to start the child is recorded as an observation. |
| PRF-14 | The devLXD client is `internal/devlxd`, built on the SDK's `ConnectDevLXD`. Calls go through its raw access (`RawQuery`) so that every path of PRF-10 is reachable, including those the SDK has no typed method for, and the response is recorded as received. |

### Local files (proof path rehearsal)

| ID | Requirement |
|---|---|
| PRF-20 | `guest file put PATH --content-file F [--mode 0600]` writes a file under `/run/lxd-probe/` (or under `/run/spire/lxd/` with `--allow-spire-paths`, PRN-02) using `O_CREAT\|O_EXCL`, mode as requested, and records the resulting `stat` (uid, gid, mode, size, mtime, inode). |
| PRF-21 | `guest file stat PATH` records `lstat` (so symlinks are visible) and `stat`. |
| PRF-22 | `guest file rm PATH` removes a file created by `lxd-probe` only (checks a sidecar marker or the lab directory). |
| PRF-23 | `guest file layout` creates the set of "hostile layouts" used by L1: a regular file, a symlink to a file, a directory, a file with mode 0666, a file owned by another uid (when root). Used with `server file stat` (*Server environment → Files API*) to learn what the files API reports for each. Created only under `/run/lxd-probe/layout/`. |

### idmap

| ID | Requirement |
|---|---|
| PRF-30 | `guest idmap show` prints and records the parsed `/proc/self/uid_map` and `gid_map`. |

## Server environment

### Connection and capability discovery

| ID | Requirement |
|---|---|
| PRS-01 | `server info` calls `GET /1.0` and records: `api_extensions` (full list), server version, `auth` state, `environment` (kernel, driver versions, `server_clustered`, `server_name`), and the TLS version and cipher negotiated. Highlights which extensions required by attestor spec *Server-side plugin → LXD integration → API usage and required extensions* are present or absent. Supports L8. |
| PRS-02 | `server whoami` calls `GET /1.0/auth/identities/current` when the extension exists and records the identity type, groups and effective permissions as reported. Supports L9, L13. |
| PRS-03 | TLS: API calls use the client of FR-S17 (pinned fingerprint, TLS 1.3 minimum). To learn which TLS versions a server accepts (Q3, gate L8), `server info --probe-tls12` additionally performs a bare TLS handshake (`crypto/tls`, no HTTP request) limited to TLS 1.2 against the endpoint and records whether it succeeds, with the version and cipher negotiated by each handshake. A server that accepts TLS 1.2 is recorded as a finding; no API call is ever made over TLS 1.2. |
| PRS-04 | The client does not follow redirects and records any 3xx as an unexpected response. |

### Instance inspection

| ID | Requirement |
|---|---|
| PRS-10 | `server instance show NAME` calls `GET /1.0/instances/NAME?project=P` (recursion 1 and 0, both stored) and records the **raw ETag header**, `config` (volatile keys included), `devices`, `expanded_devices`, `location`, `status`, `type`, `architecture`. Supports L1, L3, L6, L9. |
| PRS-11 | `server instance watch NAME [--for DUR]` polls the instance and prints/records a change log of `volatile.uuid`, `volatile.uuid.generation`, `volatile.idmap.*`, `location`, `status`, and ETag. Used during copy/move/restore experiments (L6, L11). |
| PRS-12 | `server instance list` records name, type, status, location for the project (used to confirm the uniqueness checks of FR-S3 and SEC-05). |

### Config writes (gate L2, L14)

All verbs in this subsection require `--allow-write --lab`, and only touch keys `user.lxd-probe.*` (or `user.spire.challenge.*` when `--allow-spire-keys` is given, to rehearse the real key layout).

| ID | Requirement |
|---|---|
| PRS-20 | `server config get NAME KEY` reads the key from the instance object and prints it. |
| PRS-21 | `server config set NAME KEY VALUE --method patch\|put --if-match auto\|none\|stale [--wait]` writes the key. `--method patch` sends `PATCH` with only the key; `--method put` reads the instance, modifies, and sends `PUT` of the whole object. `--if-match auto` uses the ETag read just before; `none` sends no header; `stale` deliberately sends an outdated ETag to provoke 412. `--wait` waits on the returned operation (`/1.0/operations/ID/wait`). The command records: request (sanitized), status, headers, operation metadata, duration, and whether a restart was triggered (instance `status` before/after). |
| PRS-22 | `server config unset NAME KEY --method patch-empty\|patch-null\|put` tries the candidate ways of removing a single `user.*` key and records which actually removes the key (as opposed to leaving an empty value). Gate L14. |
| PRS-23 | `server config race NAME KEY --writers N` launches N concurrent writers with `--if-match auto` on the same key and records the sequence of 200/412 outcomes, the number of retries, and the final value. Verifies the FR-S14 retry logic empirically and the rate of spurious 412 responses (for example when volatile keys change under the writer). |
| PRS-24 | Every write verb prints, before acting, the exact request line, and, when `--expect-instance-uuid` is supplied, reads the instance first and refuses to run (exit 5) if its `volatile.uuid` differs from the flag (guard against the wrong target). |

### Files API (gate L1, L7, L12)

| ID | Requirement |
|---|---|
| PRS-30 | `server file stat NAME PATH` performs `GET /1.0/instances/NAME/files?path=PATH` (with and without body reading as appropriate) and records **all** response headers, in particular the `X-LXD-uid`, `X-LXD-gid`, `X-LXD-mode`, `X-LXD-type` family, with their raw values. No header name is assumed: the verb dumps every `X-LXD-*` header found (resolves the "header names unverified" note of the attestor spec). |
| PRS-31 | `server file get NAME PATH [--max-bytes N]` reads the content with a limit reader and records size, hash, and latency. The `--max-bytes` default is 1 MiB. |
| PRS-32 | `server file head NAME PATH` tries `HEAD` and records whether metadata is obtainable without the body. |
| PRS-33 | `server file dir NAME PATH` lists a directory and records what the API returns for a directory, symlink target, or special file. |
| PRS-34 | Behaviour for a missing file (404 body), a permission-denied file, an oversize file (`--max-bytes` exceeded) and a stopped or frozen instance is recorded verbatim. |

### Permissions and identities (gates L9, L13)

| ID | Requirement |
|---|---|
| PRS-40 | `server permissions NAME` runs a fixed matrix of **read-only** calls for the presented identity (`GET` instance, `GET` files, `GET` events websocket handshake, `GET` operations) and records allowed/denied per call. The operator runs it once per probe identity (read, files, edit, full) to build the entitlement matrix. |
| PRS-41 | `server bearer check` (when `--auth bearer`) records whether the token authenticates against the remote API, its reported lifetime/expiry if exposed, and the first failing call after expiry (`--wait-expiry` option). Gate L13. |

### Rehearsal of the real proofs (stage 3)

| ID | Requirement |
|---|---|
| PRS-50 | `server rehearse file-pull` implements, using `internal/proof`, the server side of the `file_pull` flow of attestor spec *Trust model → Proof of co-location → Mode `file_pull`* against a lab instance, **together with** a cooperating `guest file put` in the instance, and prints a step-by-step trace (instance resolve, owner rule, nonce-derived path, read, compare). |
| PRS-51 | `server rehearse config-push` does the same for `config_push` (attestor spec *Proof of co-location → Mode `config_push`*), including the devLXD read in the guest through `guest devlxd watch`. |
| PRS-52 | Rehearsals produce a pass/fail with the failing step, but never alter the attestor logic: they call the same packages and print their errors. |

## Util environment

| ID | Requirement |
|---|---|
| PRU-01 | `util diff A.json B.json` compares two evidence records ignoring volatile fields (timestamps, durations, ETag values when asked) and prints differences. Used to compare LXD 5.21 and 6.x outputs. |
| PRU-02 | `util idmap translate --uid-map FILE-or-string --volatile-idmap JSON --uid N` runs the translation algorithm of attestor spec *Proof of co-location → UID translation* (exactly one matching entry; use `volatile.idmap.current`; fail closed) and prints the host uid or the reason for refusal. The same code is used by the plugin. |
| PRU-03 | `util idmap check INSTANCE_RECORD GUEST_PREFLIGHT_RECORD` cross-checks `volatile.idmap.base/current` of the LXD record against the guest's `/proc/self/uid_map` and reports a match or mismatch (gate L1). |
| PRU-04 | `util overlap RECORD [--proof-dir PATH]` runs the device-overlap check of the attestor spec (*Proof of co-location → Mode `file_pull`*, proof-path device rules) on the recorded `expanded_devices` and reports shared or bind-mounted paths over the proof directory (gate L12). |
| PRU-05 | `util sanitize DIR` applies the sanitization rules of *Overview → Evidence model → Record schema* (PRE-03) to an evidence directory; `util sanitize --check DIR` fails (exit 6) if forbidden material is found. |
| PRU-06 | `util fixtures verify DIR` decodes every recorded response in `DIR` with the typed structs of `internal/lxd` and reports decode errors, unknown fields (informational) and missing required fields (gate L15). |
| PRU-07 | `util note --gate Ln --cell C "text"` appends an operator observation (for example manual negative privilege tests with `lxc`) to the evidence of the run. With `--decision`, the note records the operator's decision for the gate instead (which outcome of the runbook's decision rules applies, and why). Free text is sanitized like everything else. |
| PRU-08 | `util report RUN_DIR...` merges the environments of one or more runs and renders a Markdown report per gate: question, cells covered, observations with their record ids, the gate's decision rules as text (*Testing → Gate runbooks*), the operator's decisions recorded with `util note --decision`, and open items (gates without a decision, unreached endpoints). It never matches observations to rules by itself. |

## Configuration

### Command line

#### Command structure

`lxd-probe` is built like the repository's other binaries (`cmd/lxd-agent-plugin`, `cmd/lxd-server-plugin`) and follows the `<object> <verb>` convention of the project guidelines (`CLAUDE.md`, *Command line*), with the environment as the outermost group:

```
lxd-probe <environment> [<object>] <verb> [arguments] [flags]
lxd-probe server config set NAME KEY VALUE --method patch ...
lxd-probe guest file put PATH --content-file F
lxd-probe server info                       # verbs without an object
lxd-probe version [--verbose]
```

| ID | Requirement |
|---|---|
| PRN-30 | `cmd/lxd-probe` has the same files as the plugin binaries: `main.go` (parses `command.Commands` with `flags.NewParser(&options, flags.Default)`, after `godotenv.Load()`), `init.go` (the shared `log/slog` set-up, `.env` loading through `metadata.DotEnvVarName`, and CPU/memory profiling, with `cleanup()` deferred in `main`) and `command/commands.go` (the root `Commands` struct). |
| PRN-31 | The root `Commands` struct has one go-flags command per environment (`guest`, `server`, `util`) plus `version`, which reuses `internal/command/version` unchanged. Objects and verbs are nested go-flags commands; each leaf is a struct in `internal/probe/...` that implements `Execute(args []string) error`. Options shared by several commands are declared once in base structs that have no `Execute` method (`GlobalOptions`; `GuestCommand` and `ServerCommand`, which embed `GlobalOptions`) and are embedded anonymously in the leaf commands, which go-flags scans as their own options; a leaf adds only its verb flags. Positional arguments use go-flags `positional-args` with `required` where the verb needs them. Every command and option has a `description`. |
| PRN-32 | Because every option belongs to a leaf command (PRN-31), flags are written after the verb: `lxd-probe server config set NAME KEY VALUE --endpoint ... --method patch`. The global and environment options that describe the session (evidence directory, run id, gate, cell, endpoint, pin, client certificate and key, authentication, token file, project) also read a default from an `LXD_PROBE_*` environment variable through the go-flags `env` tag (*Global flags*, *Server-environment flags*), so a lab session sets them once, in the shell or in the `.env` file. The safety flags (`--allow-write`, `--lab`, `--allow-spire-keys`, `--allow-spire-paths`) have no environment variable: they are acknowledged on each command line. |
| PRN-33 | Logging and profiling are configured only through the environment, as in the other binaries, with the prefix derived from the binary name: `LXD_PROBE_LOG_LEVEL`, `LXD_PROBE_LOG_STREAM`, `LXD_PROBE_CPU_PROFILE`, `LXD_PROBE_MEM_PROFILE`. There is no verbosity flag. Logs go to stderr by default, so they never mix with `--json` output on stdout. |
| PRN-34 | `main` maps errors to the exit codes of *Exit codes*: `flags.ErrHelp` exits 0; any other go-flags parse error exits 2; a verb returns a typed error carrying its exit code; any other error exits 1. |

#### Global flags

| Flag | Meaning |
|---|---|
| `--evidence-dir DIR` | root of the evidence tree (default `./evidence`); environment `LXD_PROBE_EVIDENCE_DIR` |
| `--run-id ID` | run identifier (default: UTC timestamp + 4 random hex); environment `LXD_PROBE_RUN_ID` |
| `--gate Ln` | tags every record with the gate it supports; environment `LXD_PROBE_GATE` |
| `--cell NAME` | tags every record with the lab-matrix cell (*Configuration → Operational guidance → Lab matrix*), e.g. `lxd6-unpriv-ubuntu`; environment `LXD_PROBE_CELL` |
| `--json` | print the result as JSON on stdout |
| `--timeout DUR` | per-command timeout (default 30s) |
| `--record DIR` | additionally store raw sanitized request/response pairs as fixture candidates (*Overview → Evidence model → Fixtures*) |
| `--strict` | exit 8 when at least one record of the invocation has outcome `unexpected` (PRE-04) |
| `--redact-names` | replace instance and project names with aliases (PRE-09) |
| `--allow-write` | required by every verb that modifies anything, in the guest or on a server (PRN-01) |
| `--lab` | explicit acknowledgement "this is a lab system"; required together with `--allow-write` |
| `--dry-run` | write verbs print what they would do and exit (PRN-04) |

#### Guest-environment flags

| Flag | Meaning |
|---|---|
| `--socket PATH` | devLXD socket (default `/dev/lxd/sock`) |
| `--allow-spire-paths` | also allow writes under `/run/spire/lxd/`, to rehearse the real proof path (PRN-02) |

#### Server-environment flags

| Flag | Meaning |
|---|---|
| `--endpoint URL` | `https://host:8443` or `unix:///var/snap/lxd/common/lxd/unix.socket`; environment `LXD_PROBE_ENDPOINT` |
| `--pin SHA256` | server certificate fingerprint to pin (FR-S17); mandatory for https; environment `LXD_PROBE_PIN` |
| `--client-cert FILE` / `--client-key FILE` | TLS identity; environment `LXD_PROBE_CLIENT_CERT` / `LXD_PROBE_CLIENT_KEY` |
| `--auth tls\|bearer` | authentication mode (default `tls`; environment `LXD_PROBE_AUTH`); `bearer` reads the token from `--token-file` or the `LXD_PROBE_TOKEN` environment variable (never from an argument) |
| `--token-file FILE` | file holding the bearer token; refused if group- or world-readable (PRN-12); environment `LXD_PROBE_TOKEN_FILE` |
| `--project NAME` | LXD project (always sent explicitly; default `default`); environment `LXD_PROBE_PROJECT` |
| `--allow-spire-keys` | also allow writes to `user.spire.challenge.*`, to rehearse the real key layout (PRN-02) |
| `--expect-denied` | a 403 is the expected result: recorded as an observation with outcome `ok`, exit 0 instead of 4 (PRE-06) |

#### Verb flags

Each verb also accepts the global flags and the flags of its environment. Positional arguments are in capitals.

| Verb | Flag | Type, default | Meaning |
|---|---|---|---|
| `server info` | `--probe-tls12` | bool | also try a TLS 1.2 handshake and record the result (PRS-03) |
| `guest devlxd watch KEY` | `--for` | duration, 60s | how long to watch (PRF-12) |
| `guest devlxd access` | `--as-uid` | list of uids, required | uids to try (PRF-13) |
| `guest file put PATH` | `--content-file` | path, required | content to write; `-` reads stdin |
| | `--mode` | octal, 0600 | file mode |
| `server instance watch NAME` | `--for` | duration, 10m | how long to watch (PRS-11) |
| | `--interval` | duration, 1s | poll interval |
| `server config set NAME KEY VALUE` | `--method` | `patch\|put`, `patch` | PRS-21 |
| | `--if-match` | `auto\|none\|stale`, `auto` | PRS-21 |
| | `--wait` | bool | wait for the operation |
| | `--expect-instance-uuid` | UUID | PRS-24 |
| `server config unset NAME KEY` | `--method` | `patch-empty\|patch-null\|put`, required | PRS-22 |
| | `--expect-instance-uuid` | UUID | PRS-24 |
| `server config race NAME KEY` | `--writers` | integer, 8 | concurrent writers (PRS-23) |
| | `--retries` | integer, 3 | retry budget per writer on 412 |
| | `--expect-instance-uuid` | UUID | PRS-24 |
| `server file get NAME PATH` | `--max-bytes` | size, 1 MiB | read limit (PRS-31) |
| `server bearer check` | `--wait-expiry` | bool | keep calling until the token expires and record the first failure (PRS-41) |
| | `--max-wait` | duration, 1h | upper bound for `--wait-expiry` |
| `util idmap translate` | `--uid-map` | path or inline map, required | PRU-02 |
| | `--volatile-idmap` | path or JSON, required | PRU-02 |
| | `--uid` | integer, required | PRU-02 |
| `util overlap RECORD` | `--proof-dir` | path, `/run/spire/lxd` | PRU-04 |
| `util sanitize DIR` | `--check` | bool | check only (PRU-05) |
| | `--out` | path | write the sanitized copy here (default: in place) |
| `util diff A B` | `--ignore-etag` | bool | ignore ETag values (PRU-01) |
| `util note TEXT` | `--decision` | bool | record a gate decision (PRU-07) |

#### Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (observations recorded; may contain `unexpected` outcomes) |
| 1 | Internal error (any error not listed below) |
| 2 | Usage error |
| 3 | Connection or TLS failure (including pin mismatch) |
| 4 | Authentication or authorization failure (recorded as observation when expected by the runbook, with `--expect-denied`) |
| 5 | Refused by safety rules (missing `--allow-write`/`--lab`, out-of-scope target) |
| 6 | Sanitization check failed |
| 7 | Timeout |
| 8 | `--strict` and at least one `unexpected` outcome |

### Operational guidance

#### Lab matrix

##### Cells

| Axis | Values |
|---|---|
| LXD server | 5.21 LTS standalone; latest 6.x standalone; a 3-node cluster; a second standalone (backup import, L6) |
| Instance | unprivileged container; privileged container; container with `security.idmap.isolated=true`; container with `raw.idmap`; Alpine VM; Ubuntu VM; Debian VM |
| Probe identity | read-only; files; edit; full; (bearer, when available) |
| Project | `default`; a restricted project |

The full cross-product is not required. `util report` lists the cells covered per gate and flags gates closed with less than one real cell per relevant axis value.

##### Naming

`--cell` uses `lxd<major>-<instance>-<image>[-<identity>]`, for example `lxd6-unpriv-ubuntu-files`.

#### Lab setup (indicative; syntax must be checked per LXD version)

```
# containers
lxc launch ubuntu:24.04 c-unpriv
lxc launch ubuntu:24.04 c-priv -c security.privileged=true
lxc launch ubuntu:24.04 c-iso  -c security.idmap.isolated=true
lxc launch ubuntu:24.04 c-raw  -c raw.idmap="both 1000 1000"
# virtual machines
lxc launch ubuntu:24.04 v-ubuntu --vm
lxc launch images:debian/12 v-debian --vm
lxc launch images:alpine/3.20 v-alpine --vm
# identities (TLS), entitlements via the LXD authorization model (OpenFGA or built-in groups, per version)
# create four identities: read (can_view), files (can_view + can_access_files), edit (can_edit), full (admin)
# restricted project
lxc project create restricted -c restricted=true
# manual negative tests: run the same lxd-probe server commands with each identity and record with:
lxd-probe util note --gate L9 --cell lxd6-unpriv-ubuntu-read "file pull returned 403"
```

## Testing

### Tests of the tool

| ID | Requirement |
|---|---|
| PRT-01 | Table-driven tests for `internal/policy` idmap translation: single entry, no entry, two matching entries, host-uid beyond range, `.next` ignored, malformed input. Plus a property test: translation followed by inverse mapping returns the original uid. |
| PRT-02 | Sanitizer tests: known secrets (tokens, cert PEM, MAC addresses, `cloud-init.*`) must never appear in output; fuzz test over random JSON. |
| PRT-03 | Safety-rule tests: each write verb refuses without flags; out-of-scope key or path is refused before any request is made. |
| PRT-10 | A `fakelxd` HTTP server (`test/fakelxd`) replays fixtures. Until real fixtures exist (stage 0), lxd-probe's own tests may use an **assumption-based** fake, clearly marked as such in test output; real fixtures supersede it. The plugins' tests never use the assumption-based fake: they replay real fixtures only (attestor spec *Testing → Tests*). |
| PRT-11 | Tests of the `internal/lxd` wrapper's own rules (FR-S17) against the fake: pin match and mismatch, redirect refused, response size limits, explicit project, `If-Match` from the captured `ETag`, `RawQuery` decoding, error mapping to internal kinds. What the SDK implements (TLS 1.3 minimum, envelope parsing, unknown fields) is covered by replaying fixtures (PRT-10), not re-tested. The `--probe-tls12` handshake is tested against an `httptest` TLS server limited to TLS 1.2 and one limited to TLS 1.3. |
| PRT-12 | ETag/412 tests against the fake: stale ETag, concurrent writers, retry limits. |
| PRT-13 | Golden-file tests of the evidence schema and of the report renderer. |

### Gate runbooks

Each runbook gives the question, the matrix cells, the commands, the evidence, and the **decision rule**: which amendment of the attestor spec follows from each outcome. Commands are indicative and use shorthand (`$S` = `lxd-probe server`, `$G` = `lxd-probe guest`; the connection flags come from the `LXD_PROBE_*` environment, PRN-32).

#### L1 — Files API metadata and owner representation

**Question:** What does the files API return for owner, mode and type, in containers (unprivileged, privileged, `security.idmap.isolated`, `raw.idmap`) and VMs? Which header names are used? Does LXD report the in-container uid or the host-shifted uid? Do symlinks and directories show up distinguishably?

**Cells:** unpriv-container, priv-container, isolated-container, raw-idmap-container, VM (Ubuntu, Debian, Alpine).

**Procedure:**
1. In the guest: `$G preflight`, `$G idmap show`, `$G file layout`, `$G file put /run/lxd-probe/proof --mode 0600`.
2. On the server: `$S instance show NAME`, `$S file stat NAME /run/lxd-probe/proof`, and the same for each layout item.
3. Offline: `lxd-probe util idmap check <instance record> <preflight record>`, `lxd-probe util idmap translate ...`.

**Decision rules:**
- Header names found → update attestor spec *Server-side plugin → LXD integration → Minimal API contract* with the exact names and check that the SDK's `GetInstanceFile` reads the same headers; remove `[V:L1]` tag.
- Owner reported as in-container uid (0) → attestor spec *Mode `file_pull`* step 6 uses the simple rule; the *UID translation* becomes a fallback for LXD versions that differ.
- Owner reported as host uid → attestor spec *UID translation* is normative; record the keys used and the matching test vectors.
- idmap keys cross-check mismatches `/proc/self/uid_map` → `volatile.idmap.current` is not usable; choose another source or restrict `file_pull` to privileged containers and VMs.
- Symlink/directory type not distinguishable → FR-S5 and attestor spec *Mode `file_pull`* must additionally require a type check by another call (or reject).

#### L2 — Live config update and propagation

**Question:** Is `PATCH` of `user.*` on a running instance accepted without restart, how fast is it visible through devLXD, is an event emitted?

**Commands:** `$G devlxd watch user.lxd-probe.t1 --for 60s` while `$S config set NAME user.lxd-probe.t1 v1 --method patch --wait`.

**Decision:** propagation delay p95 sets the default `proof_timeout` floor; absence of events fixes polling as normative in attestor spec *Mode `config_push`*; restart triggered → `config_push` is rejected as a mode.

#### L3 — devLXD metadata

**Command:** `$G devlxd dump`. **Decision:** identifies which fields (instance name/project) the guest can obtain on its own; updates the claim tables of attestor spec *Trust model → What claims can be made reliably* and the "what the agent may send" list.

#### L4 — Who can open `/dev/lxd/sock`

**Commands:** `$G preflight`, `$G devlxd access --as-uid 0,1000,65534` in an unprivileged container, a privileged container, and a VM. **Decision:** if non-root users can open the socket, `config_push` challenge data is readable by any local process → FR/SEC for `config_push` states that the challenge is not secret and that proof strength relies on the nonce binding only; add the corresponding threat entry. If only root can, record it as a control.

#### L5 — Withdrawn

Kept for reference stability. No runbook.

#### L6 — Identity stability (`volatile.uuid` and friends)

**Question:** Which of `volatile.uuid`, `volatile.uuid.generation`, `volatile.cloud-init.instance-id` change on: copy, move, rename, snapshot restore, backup export/import elsewhere, instance recovery?

**Procedure:** start `$S instance watch NAME --for 10m`; perform the operations with `lxc` and note each with `util note`. Use `util diff` between instance records before/after.

**Decision:** confirms or amends the [V:L6] statements of attestor spec *Claims about identity* and *Claims about provenance and configuration*; a duplicate `volatile.uuid` across servers confirms that the SPIFFE ID must include `server_id` (FR-S7) and that the ambiguity rule of T7 applies; rollback-detection (`generation`) rules in FR-S10 are confirmed or adapted.

#### L7 — VMs and `lxd-agent`

**Cells:** Ubuntu, Debian, Alpine VMs (the probe does not run in Windows guests, PRN-22). **Commands:** `$G preflight` at boot (via a systemd unit or cloud-init runcmd, repeated to time the agent), `$S file stat` at increasing delays after start. **Decision:** the agent-start-order documentation for operators; which image families are supported; Windows guests stay untested and unsupported.

#### L8 — Versions and extensions

**Command:** `$S info --probe-tls12` on each LXD server, which also learns whether TLS 1.2 is accepted (Q3). **Decision:** minimum LXD version list; the table of required extensions in attestor spec *API usage and required extensions* is confirmed (`projects`, `instance_generation_id`, ...); each additional extension found necessary is added.

#### L9 — Entitlements

**Procedure:** for each identity (read / files / edit / full): `$S whoami`, `$S permissions NAME`; plus the manual negative tests with `lxc` recorded as notes; repeat inside a **restricted project**.

**Decision:** minimal entitlement set per mode for attestor spec *Proof of co-location → Privilege cost of each mode*; whether the identity can see its own privileges (SEC-16 warning feasible or not).

#### L10 — Client coverage

Non-lab. The licence question is closed in attestor spec v0.4 (the SDK's `client` and `shared` packages are Apache-2.0). Evidence: the pinned SDK version (`go.mod`) recorded with `util note`; `lxd-probe` is the first consumer of the FR-S17 client, and `util report` lists the endpoints reached and unreached. **Decision:** confirm that every call of attestor spec *API usage and required extensions* is covered, by an SDK method or by `RawQuery`, and list the calls that need `RawQuery`.

#### L11 — Clusters

**Cells:** a 3-node cluster, instance on node 2 while the client talks to node 1. **Commands:** `$S instance show`, `$S file stat`, `$S config set ...` through each node; `instance watch` during `lxc move` and evacuation. **Decision:** whether forwarding is transparent; the accuracy of `location`; rules for FR-S5 during migration.

#### L12 — Shared devices and nested LXD

**Procedure:** build instances A and B sharing a disk device or bind mount over `/run/lxd-probe`; from B write a file at the path A would use. **Commands:** `$S instance show` on both, `util overlap`. **Decision:** the overlap check is complete enough or must additionally forbid listed device types; nested LXD inside a container is out of scope unless shown to defeat the check.

#### L13 — Bearer identities and short-lived tokens

**Commands:** `$S bearer check --auth bearer --wait-expiry`. **Decision:** whether the plugin can run without long-lived client certificates; the expiry behaviour determines the rotation requirements.

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

#### L15 — Fixtures

**Procedure:** run the commands above with `--record test/fixtures/<lxd-version>/`; add the provocations of 403 (identity without permission), 404 (unknown instance/file), 412 (stale ETag). **Commands:** `util sanitize`, `util sanitize --check`, `util fixtures verify`. **Decision:** the committed fixtures become the contract of the fake LXD API (PRT-10); any decode failure results in a client struct fix.

### Delivery plan (with lab checkpoints)

| Stage | Scope | Exit criterion |
|---|---|---|
| 0 | Skeleton, command tree, `internal/lxd` client with pin/TLS, evidence writer and sanitizer, `server info`, `server whoami`, `server instance show`, `--record`, `guest preflight`, `guest devlxd get/dump` | First real fixtures from LXD 5.21 and 6.x committed; PRT-02/PRT-11 green |
| 1 | `server file stat/get/head/dir`, `server config get/set/unset/race`, `guest file put/stat/rm/layout`, `guest idmap show`, `guest devlxd watch/access`, `util idmap`, `util diff`, `util sanitize` | Evidence for L1, L2, L4, L14. **Spec-amendment checkpoint:** attestor spec revised (v0.4) before stage 2 |
| 2 | `server instance watch`, `server permissions`, `server bearer check`, `util overlap`, `util fixtures verify`, `util report`; tooling for L6, L7, L8, L9, L11, L12, L13 | All gates have either evidence or a recorded "cannot be tested here" reason |
| 3 | `server rehearse file-pull`, `server rehearse config-push` | One end-to-end rehearsal per mode passes in the lab |

Each stage ends with: run the relevant runbooks, commit sanitized evidence to `docs/validation/`, write the findings list, and amend the attestor spec before starting plugin code that depends on the findings.

### Acceptance criteria

1. Every gate L1–L15 (except L5) has a record in the committed evidence tree (`docs/validation/`), or a documented reason it was not testable.
2. `lxd-probe util report` generates a per-gate Markdown report without manual editing.
3. `lxd-probe util sanitize --check` passes on the whole evidence tree.
4. The fake LXD API in the attestor test suite works solely from real fixtures.
5. No credential, token or certificate key exists in any evidence file (verified by PRT-02 plus a repository scan).
6. A findings document lists, for each gate, the spec text amended.

### Open questions ledger (to be closed by the lab, then moved into the attestor spec)

| # | Question | Gate |
|---|---|---|
| Q1 | Maximum file size accepted/returned by the files API; behaviour with files larger than `--max-bytes` | L1 |
| Q2 | Behaviour of files and config calls on a frozen or stopped instance | L1, L7 |
| Q3 | TLS versions and ciphers accepted by LXD 5.21 and 6.x on the remote API | L8 |
| Q4 | Rate limits or request throttling on the API | L14 |
| Q5 | Is the ETag of the instance affected by volatile keys changing in the background | L14 |
| Q6 | Does a config event reach devLXD (`/1.0/events`) and in which form | L2 |
| Q7 | Is the project name obtainable from inside the instance | L3 |
| Q8 | Does `volatile.uuid` survive `lxc copy` to another server | L6 |
