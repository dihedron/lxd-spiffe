# Cluster evacuation fails on a container whose files API was read, and leaves its volume behind

`lxc cluster evacuate` fails on a container when the files API of that container (`GET /1.0/instances/{name}/files`) is being read during the evacuation. LXD moves the container, then cannot delete its volume on the source member: the dataset is busy, held by an `lxd forkfile` process. The member stays evacuated, the volume stays on it, and `lxc cluster restore` of that member then fails until the volume is removed by hand.

## Environment

- LXD 5.21.8 (snap, `5.21/stable`, rev 40958) and LXD 6.9 (snap, `latest/stable`, rev 40916), three-member clusters on Ubuntu 24.04 hosts, ZFS pool `default`.
- Guests: `images:alpine/3.24` containers (3) and `images:alpine/3.24/cloud` VMs (3, 1 vCPU, 384 MiB), all running on the member being evacuated.
- Measured by ST-6 of the lxd-spiffe stress spec: `docs/validation/20261021T195510Z-st-6-c6-summary` (6.9) and `docs/validation/20261022T080452Z-st-6-c5-summary` (5.21.8).

## Steps to reproduce

On a cluster of three members, with containers `e-c1`, `e-c2`, `e-c3` and VMs `e-v1`, `e-v2`, `e-v3` running on member `M`, from another member `E`:

```sh
# one reader per container, started before the evacuation: one request at a time
for n in e-c1 e-c2 e-c3; do
  ( while true; do
      curl -sf -o /dev/null --unix-socket /var/snap/lxd/common/lxd/unix.socket \
        "http://lxd/1.0/instances/$n/files?path=/etc/hostname" || sleep 1
    done ) &
done
lxc cluster evacuate M --yes        # --force on 5.21
```

The request goes to `E`'s local socket, which forwards it to `M`.

## Expected

The containers move to other members, their volumes are deleted on `M`, and the evacuation succeeds, as it does without the readers.

## Actual

The evacuation fails after about 15 s on the first container it handles, on both versions:

```
Error: Failed migrating instance "e-c1" in project "default": Failed deleting instance on source member: Error deleting storage volume: Failed running: zfs destroy -r default/containers/e-c1: exit status 1 (cannot unmount '/var/snap/lxd/common/lxd/storage-pools/default/containers/e-c1': pool or dataset is busy) (cluster member remains evacuated)
```

(5.21.8 words it "Failed to run" and has no "(cluster member remains evacuated)".) LXD stops there. The volume of `e-c1` stays on `M`, with its database row. `lxc cluster restore M` then fails, on 6.9 three times out of three tries in a row, with "already exists on migration target storage", and the member stays evacuated.

## Evidence

Ten evacuate and restore cycles per arm, on each version:

| Arm | Containers | VMs |
|---|---|---|
| idle (no reads) | 0 of 60 migrations failed | 0 of 60 failed |
| files API read for every instance | 10 of 10 evacuations failed (one container each) | never tried: LXD stops at the first failure |
| files API read for the VMs only | 0 of 60 failed (idle) | 0 of 60 failed |

The same on 6.9 and on 5.21.8 for these three arms.

A fourth arm read every instance's file once every 60 s (each reader starting at a random moment), 10 cycles on 5.21.8 (`docs/validation/20261022T152906Z-st-6-c5-summary`): the evacuation failed in 2 of the 10 cycles, on a busy container dataset after 17 and 21 s, with a volume left behind each time; the other 8 cycles (evacuation and restore) were clean, and the VMs, read throughout, never failed (0 of 24 evacuations and 0 of 24 restores). A trial run of the same arm (2 cycles, not kept) had a restore, not an evacuation, fail the same way. In the two failures of the 10 cycles the sampler found no process holding the busy dataset, though it found `forkfile` on other containers in one of them, so `forkfile` is not shown to be the holder when reads are sparse.

The same arm on 6.9 (`docs/validation/20261022T164322Z-st-6-c6-summary`, 10 cycles): cycle 0's evacuation failed after 19 s on a busy container dataset and left a volume behind, and its restore then failed all three tries (blocked: the cluster was reset); the other 9 cycles were clean, and the VMs, read throughout, never failed (0 of 27 evacuations, 0 of 27 restores). Again the sampler found no holder for the failing dataset, while in the 9 clean cycles it found `forkfile` on all three containers. Across both versions 3 of 20 sparse cycles failed. A sampler, run once a second on every member during each step, recorded the processes in another mount namespace than LXD's that mount an instance's dataset. In the cycles with reads it found, for every container, `/snap/lxd/current/sbin/lxd forkfile -- 3 4 5 <pid>`, the helper of the files API, first seen at the same second for the three containers, shortly after the reads began. It never found a `forkfile` in the idle cycles or in the cycles that read only the VMs. The other process it found, `virtiofsd` for each VM's config share, was there in every cycle, those that succeeded included.

## Cause

Not established. What the evidence shows: a `forkfile` process in the container's mount namespace is present while the files API is being read, and the unmount of the dataset fails while it is. The process does not end with the request: after a single read of one idle container (`GET …/files?path=/etc/hostname`, answered 200 in 35 ms) one `lxd forkfile` stayed for 15.5 s and then exited (sampled every 0.5 s, LXD 5.21.8). If LXD unmounts the dataset while such a process is alive, the unmount would fail, and reads more often than every 15 s keep the process alive: that fits the continuous reads above. It is not shown for the sparse reads below: no holder was sampled in their three failures, and `forkfile` was sampled on all three containers in every clean cycle on 6.9, so its presence alone does not make an evacuation fail. Measured on 5.21.8 only.

## Impact

An operator evacuating a member for maintenance, while anything reads the files API of its containers (the SPIRE plugin's `file_pull` attestation does, `lxc file pull` does too), gets a member stuck in evacuated state and a volume to remove by hand; on 5.21.8 instances can remain running on it. Containers only: VMs under the same reads did not fail.

## Not tested

- Sparse reads other than one request per instance every 60 s; what holds the dataset in the sparse failures, where the sampler found nothing.
- The 15.5 s on 6.9, and what ends the process.
- `lxc file pull` and `lxc file push` instead of the raw API, and writes through the files API.
- Storage drivers other than ZFS, and LXD versions other than the two above.
- Live migration (`migration.stateful`).

## Workaround

Do not read or write the files API of the instances of a member while it is evacuated. After a failed evacuation, remove the leftover volume and its database row on the source member (not tested here: the lab was reset instead).
