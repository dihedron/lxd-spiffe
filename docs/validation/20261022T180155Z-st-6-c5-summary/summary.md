# Evacuation reliability summary

**No harness error.** 10 cycles, 0 blocked.

## Settings

- `arms` = `config`
- `cluster` = `c5`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c5n3`
- `harness` = `7e7deba`
- `session` = `ST-6`
- `sparse_period` = `60`
- `version` = `5.21.8`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 5.21.8 | config | container | 3 of 6 | 3 of 3 | 24 | 0 | 0 | 66.7 % (6 failed of 9) |
| 5.21.8 | config | virtual-machine | 0 of 0 | 0 of 0 | 30 | 0 | 0 | n/a |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 5.21.8 | config | 10 | 0 | 10 | 0 | 4030 ms | 5752 ms | 480 ms | 528 ms |

## Busy datasets and their holders (STR-73)

None.

## Harness errors

None.
