# Validation

The design rests on statements about LXD's behaviour that its documentation does not settle: what the files API reports as a file's owner, whether a `PATCH` honours `If-Match`, what an import does to `volatile.uuid`, who can open devLXD. Every such statement was established in a lab of real LXD servers before the code depending on it was written, and the plugins were then tested end to end, under stress and in a soak, in the same lab. The evidence of every run is kept in the repository (`docs/validation/`).

## The lab

The lab runs on one Linux host, with libvirt virtual machines: a standalone LXD 5.21 server, a standalone LXD 6.x server, a three-member cluster of each, and a SPIRE Server. The LXD servers run the snap, with ZFS storage, and baseline instances of every kind the plugins support: unprivileged, privileged, isolated and raw-idmap containers, and Ubuntu, Debian, Alpine, Rocky Linux 9 and AlmaLinux 9 virtual machines, in a regular and a restricted project. The versions tested so far: LXD 5.21.8, 6.9 and 6.10.0, with SPIRE 1.15.5.

## Validation gates

Seventeen questions about LXD (gates L1 to L17; L5 was withdrawn with the mode it concerned) were answered by recorded experiments before the dependent code was written. Among their findings:

- LXD reports a file's owner as seen inside the instance, for every kind of instance: `file_pull` accepts uid 0 only (L1).
- A `PATCH` of a `user.*` key is applied to a running instance at once and visible through devLXD within a millisecond; `PATCH` honours `If-Match`; idle instances' ETags do not change by themselves (L2, L14).
- devLXD gives an instance its name but not its project; only root can open it (L3, L4).
- `volatile.uuid` is unique per server only: an import keeps it, even on the same server, which led to the `server_id` in the SPIFFE ID and the uniqueness check (L6).
- A VM's files API needs `lxd-agent`, which takes 16 to 72 seconds after a cold boot depending on the OS; RHEL 9 derivatives with SELinux enforcing need a drop-in for it to start at all (L7).
- Every shared disk covering the proof directory let another instance forge a proof, and the isolation check refused every case (L12).
- Every cluster member forwards every call the plugin makes, without redirects; `location` is accurate within a second of a move (L11).
- LXD refuses every grant in a project the group cannot view; a group holds at most 99 permissions, and an identity's grants over several groups all count (L16, L17).
- Bearer identities work for the remote API on 6.x, with the permissions of a TLS identity; a new token revokes the previous one at once (L13).

The fixtures that these sessions recorded, every call of the plugin with its errors, are replayed by the unit and integration tests.

## End-to-end tests

With real SPIRE Agents in the instances and the plugins in SPIRE Server, on every server of the lab:

- **E2E-1, E2E-2**: unprivileged and privileged containers and Ubuntu VMs attest with `file_pull` and with `config_push` (and `auto`), with the expected SPIFFE ID and selectors; the challenge key is gone after a `config_push` attestation.
- **E2E-3**: an instance outside the allowed projects, a claim naming another instance, and an identity without `can_access_files` are refused, the agent seeing only the uniform error.
- **E2E-4**: an agent restarted without its data attests again with the same SPIFFE ID; a copy made without the agent's data gets a different SPIFFE ID; a copy that keeps the agent's data never attests and is issued the original's workload SVIDs (recorded, the basis of `T14`). With the agent's data in tmpfs, on a container and a VM, an agent restart does not attest, a copy (a VM from a stateless snapshot) attests as another instance and gets none of the original's workload SVIDs, and a reboot attests again as the same instance, after about 10 s in a container and 42 s in a VM.
- **E2E-5**: on a cluster, an instance on the second member attests through the first.

E2E-1 to E2E-4 passed on LXD 5.21.8 and 6.10.0 (and earlier on 6.9), the tmpfs variant of E2E-4 on both, E2E-5 on a 6.10.0 cluster.

## Stress, chaos and soak

- **Load**: 100 instances per server, attestations at 1 to 20 at once and in bursts of 20 per second; `file_pull` p95 between 54 and 617 ms, `config_push` between 66 ms and 3.0 s (LXD serializes an instance's writes).
- **Chaos**: thousands of attestations with injected faults: the LXD daemon killed, network partitions, members down, instances stopped, moved and restored mid-attestation, concurrent writers, tampered proofs. On standalone servers (5.21.8 and 6.9) and on clusters (5.21.8, 6.9 and 6.10.0), the security invariants held throughout: no identity issued without a valid proof, none for the wrong instance, and none for an instance that changed during its attestation.
- **Evacuations**: 60 migrations per instance type and version, with and without attestations running; the finding on containers read through the files API during their evacuation (*LXD integration*, `T21`) holds on 5.21.8, 6.9 and 6.10.0.
- **Soak**: 100 real SPIRE Agents with 5-minute SVIDs for 60 minutes, under faults every 90 seconds (SPIRE Server restarted, the server plugin killed, partitions, agents wiped and stopped): about 2,500 attestations, invariants held, no challenge key left behind. It also showed that SPIRE does not restart a killed plugin process (the *Operator's Guide* says how to detect it), and that SPIRE Server's per-address rate limit makes agents behind one NAT address queue after a mass restart.

## What remains untested

Storage drivers other than ZFS; live migration of instances; Windows guests (unsupported); more than 100 agents per server; the server's sustained rate above about 12 attestations per second, which the lab's own harness could not exceed.
