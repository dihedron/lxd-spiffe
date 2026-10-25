# Evacuation reliability summary

**No harness error.** 30 cycles, 10 blocked.

## Settings

- `arms` = `idle,files,files-vm`
- `cluster` = `c6`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c6n3`
- `harness` = `d4bfd88`
- `session` = `ST-6`
- `sparse_period` = `60`
- `version` = `6.10.0`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 6.10.0 | files | container | 10 of 10 | 0 of 0 | 20 | 10 | 10 | 100.0 % (10 failed of 10) |
| 6.10.0 | files | virtual-machine | 0 of 0 | 0 of 0 | 30 | 0 | 0 | n/a |
| 6.10.0 | files-vm | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 6.10.0 | files-vm | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 6.10.0 | idle | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 6.10.0 | idle | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 6.10.0 | files | 10 | 10 | 10 | 10 | 16812 ms | 20422 ms | 24173 ms | 36032 ms |
| 6.10.0 | files-vm | 10 | 0 | 0 | 0 | 127983 ms | 138360 ms | 126553 ms | 142740 ms |
| 6.10.0 | idle | 10 | 0 | 0 | 0 | 121241 ms | 128556 ms | 120614 ms | 128349 ms |

## Busy datasets and their holders (STR-73)

- cycle 1, c6n3, default/containers/e-c1: c6n3: 185006 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 181353 (first seen at 1792869253)
- cycle 4, c6n3, default/containers/e-c1: c6n3: 159512 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 121125 (first seen at 1792870227)
- cycle 7, c6n3, default/containers/e-c1: c6n3: 181452 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 179812 (first seen at 1792871177)
- cycle 10, c6n3, default/containers/e-c1: c6n3: 178278 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 147125 (first seen at 1792872095)
- cycle 13, c6n3, default/containers/e-c1: c6n3: 182489 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 138065 (first seen at 1792873024)
- cycle 16, c6n3, default/containers/e-c1: c6n3: 181096 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 149231 (first seen at 1792873952)
- cycle 19, c6n3, default/containers/e-c1: c6n3: 181077 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 136625 (first seen at 1792874868)
- cycle 22, c6n3, default/containers/e-c1: c6n3: 182199 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 137726 (first seen at 1792875764)
- cycle 25, c6n3, default/containers/e-c1: c6n3: 181445 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 136266 (first seen at 1792876684)
- cycle 28, c6n3, default/containers/e-c1: c6n3: 182859 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 139348 (first seen at 1792877602)

## Harness errors

None.
