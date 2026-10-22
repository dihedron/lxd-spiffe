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

The same on 6.9 and on 5.21.8. A sampler, run once a second on every member during each step, recorded the processes in another mount namespace than LXD's that mount an instance's dataset. In the cycles with reads it found, for every container, `/snap/lxd/current/sbin/lxd forkfile -- 3 4 5 <pid>`, the helper of the files API, first seen at the same second for the three containers, shortly after the reads began. It never found a `forkfile` in the idle cycles or in the cycles that read only the VMs. The other process it found, `virtiofsd` for each VM's config share, was there in every cycle, those that succeeded included.

## Cause

Not established. What the evidence shows: a `forkfile` process in the container's mount namespace is present while the files API is being read, and the unmount of the dataset fails while it is. We have not looked at when `forkfile` exits, or whether one request is enough.

## Impact

An operator evacuating a member for maintenance, while anything reads the files API of its containers (the SPIRE plugin's `file_pull` attestation does, `lxc file pull` does too), gets a member stuck in evacuated state and a volume to remove by hand; on 5.21.8 instances can remain running on it. Containers only: VMs under the same reads did not fail.

## Not tested

- Sparse reads: ours were back to back, which is the worst case. We do not know whether a single read during an evacuation is enough.
- `lxc file pull` and `lxc file push` instead of the raw API, and writes through the files API.
- Storage drivers other than ZFS, and LXD versions other than the two above.
- Live migration (`migration.stateful`).

## Workaround

Do not read or write the files API of the instances of a member while it is evacuated. After a failed evacuation, remove the leftover volume and its database row on the source member (not tested here: the lab was reset instead).
