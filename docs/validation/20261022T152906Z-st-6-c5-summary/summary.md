# Evacuation reliability summary

**No harness error.** 10 cycles, 0 blocked.

## Settings

- `arms` = `files-sparse`
- `cluster` = `c5`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c5n3`
- `harness` = `e25bdf7`
- `session` = `ST-6`
- `sparse_period` = `60`
- `version` = `5.21.8`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 5.21.8 | files-sparse | container | 2 of 29 | 3 of 27 | 1 | 2 | 2 | 8.9 % (5 failed of 56) |
| 5.21.8 | files-sparse | virtual-machine | 0 of 24 | 0 of 24 | 6 | 0 | 0 | below 6.2 % (none failed of 48) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 5.21.8 | files-sparse | 10 | 0 | 2 | 0 | 113388 ms | 114973 ms | 114046 ms | 134083 ms |

## Busy datasets and their holders (STR-73)

- cycle 3, c5n3, default/containers/e-c3: no holder found
- cycle 6, c5n3, default/containers/e-c2: no holder found

## Harness errors

None.
