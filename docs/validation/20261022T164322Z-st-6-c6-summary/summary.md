# Evacuation reliability summary

**No harness error.** 10 cycles, 1 blocked.

## Settings

- `arms` = `files-sparse`
- `cluster` = `c6`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c6n3`
- `harness` = `4e1e295`
- `session` = `ST-6`
- `sparse_period` = `60`
- `version` = `6.9`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 6.9 | files-sparse | container | 1 of 29 | 0 of 28 | 1 | 1 | 1 | 1.8 % (1 failed of 57) |
| 6.9 | files-sparse | virtual-machine | 0 of 27 | 0 of 27 | 3 | 0 | 0 | below 5.6 % (none failed of 54) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 6.9 | files-sparse | 10 | 1 | 1 | 1 | 114569 ms | 115825 ms | 115084 ms | 116767 ms |

## Busy datasets and their holders (STR-73)

- cycle 0, c6n3, default/containers/e-c2: no holder found

## Harness errors

None.
