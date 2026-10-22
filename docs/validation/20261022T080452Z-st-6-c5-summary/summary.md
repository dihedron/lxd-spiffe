# Evacuation reliability summary

**No harness error.** 30 cycles, 0 blocked.

## Settings

- `arms` = `idle,files,files-vm`
- `cluster` = `c5`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c5n3`
- `harness` = `f3b413f`
- `session` = `ST-6`
- `version` = `5.21.8`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 5.21.8 | files | container | 10 of 10 | 0 of 0 | 20 | 10 | 10 | 100.0 % (10 failed of 10) |
| 5.21.8 | files | virtual-machine | 0 of 0 | 0 of 0 | 30 | 0 | 0 | n/a |
| 5.21.8 | files-vm | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 5.21.8 | files-vm | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 5.21.8 | idle | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 5.21.8 | idle | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 5.21.8 | files | 10 | 0 | 10 | 0 | 15234 ms | 15492 ms | 564 ms | 619 ms |
| 5.21.8 | files-vm | 10 | 0 | 0 | 0 | 118283 ms | 118994 ms | 118759 ms | 119533 ms |
| 5.21.8 | idle | 10 | 0 | 0 | 0 | 113872 ms | 115399 ms | 114803 ms | 116170 ms |

## Busy datasets and their holders (STR-73)

- cycle 1, c5n3, default/containers/e-c1: c5n3: 177075 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 162657 (first seen at 1792656607)
- cycle 4, c5n3, default/containers/e-c1: c5n3: 172980 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 169304 (first seen at 1792657945)
- cycle 7, c5n3, default/containers/e-c1: c5n3: 172059 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 157316 (first seen at 1792659292)
- cycle 10, c5n3, default/containers/e-c1: c5n3: 169859 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 164209 (first seen at 1792660635)
- cycle 13, c5n3, default/containers/e-c1: c5n3: 172012 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 128990 (first seen at 1792661991)
- cycle 16, c5n3, default/containers/e-c1: c5n3: 173406 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 145088 (first seen at 1792663334)
- cycle 19, c5n3, default/containers/e-c1: c5n3: 172628 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 129850 (first seen at 1792664679)
- cycle 22, c5n3, default/containers/e-c1: c5n3: 172122 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 155589 (first seen at 1792666025)
- cycle 25, c5n3, default/containers/e-c1: c5n3: 167804 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 126654 (first seen at 1792667381)
- cycle 28, c5n3, default/containers/e-c1: c5n3: 174367 /snap/lxd/current/sbin/lxd forkfile -- 3 4 5 168167 (first seen at 1792668712)

## Harness errors

None.
