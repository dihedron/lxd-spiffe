# Evacuation reliability summary

**No harness error.** 10 cycles, 0 blocked.

## Settings

- `arms` = `files-vm`
- `cluster` = `c6`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c6n3`
- `harness` = `799d6cf`
- `session` = `ST-6`
- `version` = `6.9`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 6.9 | files-vm | container | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |
| 6.9 | files-vm | virtual-machine | 0 of 30 | 0 of 30 | 0 | 0 | 0 | below 5.0 % (none failed of 60) |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 6.9 | files-vm | 10 | 0 | 0 | 0 | 117199 ms | 118601 ms | 116686 ms | 118299 ms |

## Busy datasets and their holders (STR-73)

None.

## Harness errors

None.
