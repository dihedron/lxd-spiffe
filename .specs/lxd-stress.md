# Stress, soak and chaos tests of the LXD attestor — spec

Oct 18, 2026 · @Andrea Funtò

*Status*: draft v0.1, for spec-driven development. Phase A runs on today's lab with `lxd-probe`'s rehearsals; Phase B needs the plugins (attestor spec, delivery step (e)) and the lab's `plugins` scenario.

| Field | Value |
|---|---|
| Status | Draft v0.1: Phase A to build and run now; Phase B specified, runnable after step (e) |
| Purpose | Show that attestation stays **correct** and **recovers** under sustained load and injected faults: hundreds of iterations, with nodes, instances, LXD and the attestor's own processes stopped halfway |
| Companion of | `lxd-spire-plugins.md` (the "attestor spec": NFR-01, NFR-02, NFR-04, SEC-09, FR-S10, FR-S13, FR-S14), `lxd-probe.md` (rehearsals, PRS-50–PRS-53), `lxd-test-environment.md` (the lab) |
| Harness | Go tests in `test/lab/acceptance` behind the `lab` build tag, like the gate sessions; no dependency added to `go.mod` |

**Keywords.** MUST, MUST NOT, SHOULD, MAY per RFC 2119. Requirement prefixes: STR (stress harness), INV (invariants checked after every iteration), C (fault catalogue).

**Revision history.**

| Version | Changes |
|---|---|
| v0.1 (Oct 18, 2026) | Initial draft: Phase A (rehearsals under load, soak and chaos), Phase B (real attestation and workloads under chaos). |

## Overview

A gate session asks one question once. This spec asks the attestor's LXD-facing flow to work **hundreds of times in a row**, many at once, while the harness breaks things on purpose, and checks after every iteration that nothing unsafe happened and that the system comes back.

Two phases share the harness, the invariants and the report:

- **Phase A (now)** drives the attestor's real packages and real LXD calls through `lxd-probe server rehearse` and `guest rehearse` (probe spec PRS-50–PRS-53) against a fleet of lab instances. It stresses everything on the LXD side of attestation: the instance read, `policy.Check`, the files API with the VM start-up retry, the conditional `PATCH` and its cleanup, the FR-S10 re-read.
- **Phase B (after step (e))** replaces the rehearsals with SPIRE: a SPIRE Server with the server plugin, a SPIRE Agent with the agent plugin in every fleet instance, registration entries on `lxd_instance` selectors, and simulated workloads that fetch SVIDs. The same faults, plus the attestor's own processes killed halfway.

### What "stable" means here

Every iteration ends in one of: **pass**, **fail** (a step failed, with its step name), or **harness error** (the harness itself could not run it). Stability is four properties, checked as invariants (*Invariants*):

1. **Safety**: no pass that should not be one. A rehearsal or attestation passes only if the instance it resolved still holds the proof, unchanged, at the end.
2. **Liveness**: every iteration ends within a bound; nothing hangs.
3. **Recovery**: once a fault is cleared, iterations pass again within a bound.
4. **Cleanliness**: no challenge key or proof file outlives its iteration, except where an injected crash prevented the cleanup, and those are counted and accounted for.

## Phase A — rehearsals under load and chaos

### Fleet

| ID | Requirement |
|---|---|
| STR-01 | The session creates a fleet of `LAB_STRESS_FLEET` (default 50) Alpine containers per standalone server, `s-001`… in project `default`, from the image the lab already uses, with `lxd-probe` installed as in the baseline (`LAB_GUEST_PROBE`) and the lab identities' grants (`files`: `can_access_files`; `edit`: `can_edit`; project `can_view` first). The baseline instances (containers and VMs) join the fleet, so that every instance class is stressed. |
| STR-02 | The fleet is deleted at the end of the session, whatever happened, and at its start if a previous session left one (a teardown that runs on both ends, as G-L12's). |
| STR-03 | With a cluster running (`LAB_CLUSTER`), the session MAY also create a smaller fleet on it (default 9, three per member) for the cluster faults (C7, C8). |

### Load profile

| ID | Requirement |
|---|---|
| STR-10 | **Stepped load**: for each mode, rehearsals of every fleet instance at concurrency 1, 5, 10 and 20 (workers), each step attesting every instance at least once. It measures latency and throughput without faults. |
| STR-11 | **Soak**: `LAB_STRESS_ITERATIONS` (default 500) rehearsals per mode per server, instances drawn at random, at concurrency `LAB_STRESS_WORKERS` (default 10), with faults injected (*Fault catalogue*) at rate `LAB_STRESS_FAULT_RATE` (default 0.2 of the iterations). The random generator's seed is recorded; the same seed replays the same sequence of instances and faults. |
| STR-12 | **Burst**: the attestor's rate limit (SEC-09: 20/s, 16 in flight) as offered load for 60 s, to see what LXD does at the plugin's own limits. |

### Fault catalogue

Each fault has an **injection point** (when, relative to the iteration's steps), an **effect**, the **expected outcome** (the iterations it hits must end so) and a **clean-up** that the injector always performs, even if the harness dies: faults that cut access are reverted by a timer armed on the VM itself (`systemd-run --on-active`), not by the harness.

| ID | Fault | Injection point | Expected outcome of the hit iteration | Clean-up |
|---|---|---|---|---|
| C1 | Instance stopped | after the challenge, before the read | fail at `read` or `recheck` (never pass) | instance started again |
| C2 | Instance restarted | after the guest's answer, before the read | container: fail at `read` (the proof was on tmpfs); VM: `read` retries through the agent's start-up, then fails (file gone) | none (it restarts itself) |
| C3 | LXD daemon restarted (`snap restart lxd`) on the server | anywhere | fail at the step in flight, or pass if the restart fell between steps and `recheck` saw the instance unchanged | wait for the daemon |
| C4 | Server half killed (`SIGKILL`) | `config_push`: after `write`, before `cleanup`; `file_pull`: after `challenge` | harness records the kill; the challenge key (or proof file) is **left behind** | counted; removed by the session's sweep (STR-30) |
| C5 | Guest half killed, or never answers | after the challenge | fail at `answer` within `--timeout`; `config_push` still runs `cleanup` | none |
| C6 | Network partition between the lab host and an LXD server (`iptables` drop of 8443 on the VM) for 5–20 s | anywhere | fail at the step in flight within `--timeout`, never pass across the partition unless `recheck` succeeded after it | rule removed by the VM's own timer |
| C7 | Cluster member stopped (one that holds the instance, or one that only forwards) | anywhere | fail or pass as forwarding allows; never pass with a changed instance | member started again |
| C8 | Instance moved or its member evacuated (cluster) | between `challenge` and `recheck` | fail at `recheck` (`location` changed, FR-S10); a VM may fail at `read` first | instance moved back, member restored |
| C9 | Concurrent writer on the instance's config (another `PATCH`) | between the first read and `write` (`config_push`) | fail at `write` with 412 (the rehearsal does one `PATCH`; FR-S14's retries are the plugin's) | the writer's key removed |
| C10 | Proof tampered or swapped (`--tamper`, or a proof for another instance written at the path) | the guest's step | fail at `compare` | proof file removed |

The injector picks the fault and its moment from the seeded generator; each record of a hit iteration names the fault (`--cell <instance>-<fault>`), so the report groups outcomes by fault.

### Invariants (checked after every iteration and at the end)

| ID | Invariant |
|---|---|
| INV-1 | **No false pass.** An iteration hit by C1, C2, C4–C6 (when they cut the step in flight), C8 or C10 MUST NOT pass; one hit by C3, C6 or C7 passes only if its `recheck` step ran after the fault and found the instance unchanged. Any other pass of a hit iteration is a violation. |
| INV-2 | **Bounded time.** Every iteration ends within `--timeout` + 10 s, the margin for the harness's own processes; a hung iteration is a violation and is killed. |
| INV-3 | **Recovery.** After each fault's clean-up, the next iteration on the same instance (and, for C3, C6, C7, on the same server) passes within 60 s, retrying at most 5 times. |
| INV-4 | **No leftovers.** After an iteration that ran to its end, no `user.spire.challenge.*` key is set on its instance and no proof file is left in its proof directory (the guest's `file rm` runs as part of the iteration). Leftovers after C4 are counted as **orphans**, not violations: they measure what a crash leaves behind. |
| INV-5 | **Sanitized evidence.** The committed evidence passes `util sanitize --check`. |
| INV-6 | **No drift.** Over the soak, the LXD daemon's resident memory and the number of its operations do not grow without bound: the session samples them every 60 s (`/proc/<pid>/status`, `GET /1.0/operations`), and a growth of the last sample over the first above 50 % is a finding, not a violation. |

| ID | Requirement |
|---|---|
| STR-30 | **Orphan sweep.** At the end of the soak, the session lists every `user.spire.challenge.*` key on the fleet and every file under `/run/spire/lxd/` of the fleet instances, removes them, and reports their number per fault. Expected: orphans only after C4. Their number feeds an attestor spec decision: a sweep of stale challenge keys by the plugin, or a documented operator procedure. |
| STR-31 | **Pass criteria.** A Phase A session passes when INV-1 to INV-5 hold. Latency and throughput are measured, not pass criteria: they are compared with NFR-01 (p95 ≤ 4 s `file_pull`, ≤ 6 s `config_push`) and NFR-02 (≥ 20/s) and reported as findings when outside. |

### Measures and report

| ID | Requirement |
|---|---|
| STR-40 | **Step timing.** The server rehearsal records each step's duration as `step.<name>.ms` (probe spec amendment, PRS-54), so that the attestor's own latency is the sum of its LXD-facing steps, without the time the guest half takes through `lab.sh guest` (SSH and `lxc exec`), which the real agent does not pay. |
| STR-41 | **Summary.** Each run writes `summary.md` and `summary.json`: the seed and settings; per mode, step and fault, the counts of pass, fail (by failing step) and harness errors; latency percentiles (p50, p95, p99, max) of the attestor's steps, per step and in total, per concurrency level; throughput; the invariant results with every violation's iteration; orphans per fault; the drift samples. |
| STR-42 | **Evidence size.** Per-iteration records stay in the working run (`LAB_EVIDENCE_DIR`); a run collected to `docs/validation/<run_id>-stress/` keeps the summary, the records of failed iterations hit by no fault, of invariant violations, and of a sample of 20 passes and 20 failures per fault, sanitized. |
| STR-43 | The records of stress sessions name gate `S`, a group of the report like `R` (evidence only, no decision expected). |

### Sessions (Phase A)

| ID | Session | Checks |
|---|---|---|
| ST-1 | Fleet | STR-01, STR-02: the fleet is built, every instance answers one rehearsal per mode, and is deleted at the end |
| ST-2 | Stepped load | STR-10 on both servers, both modes; no faults; INV-2, INV-4, INV-5 |
| ST-3 | Soak with chaos | STR-11 with faults C1–C6, C9, C10 on the standalone servers; all invariants; the sweep (STR-30) |
| ST-4 | Cluster chaos | With a cluster up: STR-11 on its fleet with C3, C6, C7, C8 added; all invariants |
| ST-5 | Burst | STR-12; INV-2, INV-4 |

## Phase B — real attestation and workloads under chaos

Runnable once the plugins attest (attestor spec delivery step (e)) and the lab's `plugins` scenario provides a SPIRE Server (LAB-4). Specified now so that the plugins are built to pass it.

| ID | Requirement |
|---|---|
| STR-50 | **Fleet of agents.** Every fleet instance runs a SPIRE Agent with the agent plugin (`file_pull`, and `config_push` on a second fleet), joined to the lab's SPIRE Server with the server plugin and the lab's least-privilege identity. `spire-server agent list` MUST show every instance once, with the SPIFFE ID `spiffe://<td>/spire/agent/lxd_instance/<server_id>/<volatile.uuid>` and the selectors the attestor spec defines. |
| STR-51 | **Registration and workloads.** Registration entries on `lxd_instance` selectors, for example `project:default`, `privileged:false`, `profile:<p>`, `network:<project>/<net>`, `user:<key>:<value>`; with the `unix` workload attestor, a simulated workload in every instance (a small fetcher under a dedicated uid) fetches its X.509-SVID in a loop. Each workload MUST get exactly the SVIDs its node's selectors and its own uid allow: an "unprivileged only" entry never reaches a privileged container; a profile change moves an instance in or out of an entry after re-attestation. |
| STR-52 | **Churn and chaos.** On top of the Phase A faults: SPIRE Server killed during attestations; the server plugin process killed halfway (SPIRE restarts it); an agent killed and restarted, with and without its data; agents' SVIDs expiring (short TTL) so that re-attestation (`reattest = "allow"`) runs continuously; an instance copied (gate L6: the copy gets another SPIFFE ID or is refused, FR-S18); an instance moved within a cluster (FR-S10: `instance_moved`). |
| STR-53 | **Invariants of Phase B.** INV-1 becomes: no agent receives an identity, and no workload an SVID, that its instance's current state does not justify (a stopped, moved, copied or changed instance is refused; a workload never gets an SVID of another instance's entries). INV-2–INV-6 hold as in Phase A, with the challenge keys left by a killed server plugin counted as orphans (STR-30). Added: **INV-7**, every agent that should be attested is attested again within 2 minutes of the last fault's clean-up; **INV-8**, every workload's SVID is renewed before expiry outside fault windows. |

## Harness

| ID | Requirement |
|---|---|
| STR-60 | A concurrent driver in `test/lab/acceptance`: a worker pool of `LAB_STRESS_WORKERS` goroutines, each running one iteration at a time through the existing rehearse helper (server half piped, guest half through `lab.sh guest`), and one injector goroutine that schedules faults from the seeded generator and performs their clean-up. Results go to a JSON Lines file per run, one line per iteration (instance, mode, fault, outcome, failing step, step timings, start and end time), from which the summary is computed. |
| STR-61 | Faults are performed through `lab.sh ssh` on the VMs (`lxc stop`, `snap restart lxd`, `iptables` with a `systemd-run --on-active` revert, `lxc move`, `lxc cluster evacuate`/`restore`) and by signalling the harness's own child processes (C4, C5). Every fault that cuts access arms its revert on the VM before cutting. |
| STR-62 | Settings come from the environment with defaults: `LAB_STRESS_FLEET`, `LAB_STRESS_ITERATIONS`, `LAB_STRESS_WORKERS`, `LAB_STRESS_FAULT_RATE`, `LAB_STRESS_SEED` (default: from the clock, recorded), `LAB_STRESS_FAULTS` (default: the session's set). |
| STR-63 | A stress session never runs as part of a plain `lab.sh test`: it runs only when selected (`-run 'TestST'`), since it takes tens of minutes. |

## Lab resources

The fleet's containers are small (Alpine, about 30 MB of memory each): 50 per server fit in the standalone servers' 12 GiB. The cluster faults need one cluster at a time (3 × 4 GiB), as G-L16 did when both clusters exceeded the host's free memory.

## Open questions

| # | Question | Where it is answered |
|---|---|---|
| Q1 | How many challenge keys does a crashed server leave, and should the plugin sweep stale ones (on start-up, or before writing a new one on the same instance)? | ST-3 (C4) → attestor spec decision |
| Q2 | Do NFR-01 and NFR-02 hold, now that the steps can be timed one by one? | ST-2, ST-5 → attestor spec (NFR-01, NFR-02 lose "provisional") |
| Q3 | Does LXD queue, throttle or fail under 20 attestations per second on one server, and on a cluster? | ST-5, ST-4 |
