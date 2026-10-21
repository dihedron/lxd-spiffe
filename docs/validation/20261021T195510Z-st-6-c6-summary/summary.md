# Evacuation reliability summary

**No harness error.** 20 cycles, 10 blocked.

## Settings

- `cluster` = `c6`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c6n3`
- `harness` = `bb3d94a-dirty`
- `session` = `ST-6`
- `version` = `6.9`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 6.9 | files | container | 10 of 10 | 0 of 0 | 20 | 10 | 10 | 100.0 % (10 failed of 10) |
| 6.9 | files | virtual-machine | 0 of 0 | 0 of 0 | 30 | 0 | 0 | n/a |
| 6.9 | idle | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 6.9 | idle | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 6.9 | files | 10 | 10 | 10 | 10 | 16741 ms | 17424 ms | 24054 ms | 24524 ms |
| 6.9 | idle | 10 | 0 | 0 | 0 | 115967 ms | 117147 ms | 116838 ms | 119583 ms |

## Busy datasets and their holders (STR-73)

- cycle 1, c6n3, default/containers/e-c1: c6n3: 98955 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 55378 (first seen at 1792612837)
- cycle 3, c6n3, default/containers/e-c1: c6n3: 93682 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 91772 (first seen at 1792613472)
- cycle 5, c6n3, default/containers/e-c1: c6n3: 96945 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 54031 (first seen at 1792614091)
- cycle 7, c6n3, default/containers/e-c1: c6n3: 97077 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 67863 (first seen at 1792614711)
- cycle 9, c6n3, default/containers/e-c1: c6n3: 101281 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 54809 (first seen at 1792615344)
- cycle 11, c6n3, default/containers/e-c1: c6n3: 93411 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 76152 (first seen at 1792615971)
- cycle 13, c6n3, default/containers/e-c1: c6n3: 96519 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 52799 (first seen at 1792616596)
- cycle 15, c6n3, default/containers/e-c1: c6n3: 94276 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 50858 (first seen at 1792617254)
- cycle 17, c6n3, default/containers/e-c1: c6n3: 98872 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 54003 (first seen at 1792617904)
- cycle 19, c6n3, default/containers/e-c1: c6n3: 94824 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 51889 (first seen at 1792618569)

## Harness errors

None.
