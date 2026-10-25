# Choosing the proof mode

The server plugin proves that the agent runs inside the instance it looked up with one of two modes, set by `proof_mode` on the server:

- **`file_pull`** (the default): the server sends a fresh nonce and a path; the agent writes a hash of both nonces and its claim into a root-owned file in `/run/spire/lxd`, a directory only root can enter; the server reads the file back through LXD's files API and checks its owner, mode and content.
- **`config_push`**: the server writes a one-time value into the instance's configuration (`user.spire.challenge.<id>`); the agent reads it through devLXD (`/dev/lxd/sock`) and sends it back; the server compares it and removes the key.

`auto` takes `file_pull` when the agent offers it, which agents always do, and `config_push` otherwise; it exists for agents that cannot write the proof. Both modes need root in the guest, and both need `lxd-agent` in virtual machines.

## What the lab measured

The project's lab measured both modes on LXD 5.21.8 and 6.9 (6.10.0 repeated the cluster and evacuation results), on standalone servers and three-member clusters, with ZFS storage:

| | `file_pull` | `config_push` |
| ---------------------------- | ------------------------------- | ------------------------------- |
| LXD grants on the instances | `can_access_files` | `can_edit` (broad: any setting of the instance) |
| In the guest | root; `/run/spire/lxd` | root; `security.devlxd=true` |
| Latency, 100 instances, no faults, p50 / p95 | 211 / 561 ms | 450 / 690 ms |
| Burst of 20 per second, p95 / max | 307 / 535 ms | 635 / 992 ms |
| Under injected faults, standalone, p95 / max | 175 / 363 ms | 741 ms / 12.1 s |
| Under injected faults, clusters, p95 | 193 to 211 ms | 405 to 428 ms |
| Security invariants under faults | all held | all held |
| Left behind by a failed attestation | nothing | a stale challenge key, removed by the instance's next attestation |
| Evacuating a cluster member with containers | fails every time under continuous attestations, in 3 of 20 cycles with one per container per minute, and leaves a volume that needs a manual repair | fails every time under continuous attestations, quickly; nothing left behind; an instance can be left stopped; retry |
| Evacuating a cluster member with VMs | never failed (0 of 60 migrations per version) | not measured |

The latencies are those of the server's side of an attestation, LXD calls included; `config_push` makes two writes per attestation, which LXD serializes per instance.

## When to use which

1. **Standalone servers, and clusters whose members you do not evacuate while agents attest: `file_pull`.** It needs a narrower grant, answers in about half the time and has the tighter tail.
2. **Cluster members with containers that you evacuate for maintenance**: with `file_pull`, evacuate only once the containers' agents are not attesting, or expect the failure described in *Clusters* and its manual repair, which edits LXD's database. With `config_push`, an evacuation can fail but can simply be repeated, at the cost of `can_edit`, about twice the latency, and stale keys. Choose by what you can accept: a database repair, or a broad grant.
3. **Members with VMs only: `file_pull`.** Its reads never failed an evacuation of a VM.
4. The numbers come from small labs on ZFS. How often an evacuation meets an attestation grows with the number of containers on the member and how often they attest: one attestation per container per minute failed 3 evacuation cycles in 20, which is a measurement, not a rate for your fleet.

## What attestation is not compatible with

- **Evacuating a member whose containers attest with `file_pull`**: the evacuation can fail and leave a volume that LXD cannot delete; on LXD 6.9 the member stays evacuated until the repair (*Operator's Guide*, *Incident procedures*).
- **Stopping or moving an instance while `config_push` writes to it**: LXD answers `Instance is busy running a "update" operation`, and an evacuation can leave the instance stopped; retry, then start it.
- **`snap restart lxd`**: it stops and restarts every instance on the server, and every agent attests again at once. Plan it as maintenance.
