# Evacuation reliability summary

**No harness error.** 10 cycles, 0 blocked.

## Settings

- `arms` = `config`
- `cluster` = `c6`
- `containers` = `3`
- `cycles` = `10`
- `evacuee` = `c6n3`
- `harness` = `7e7deba`
- `session` = `ST-6`
- `sparse_period` = `60`
- `version` = `6.9`
- `vms` = `3`

## Migrations

| LXD | Arm | Type | Evacuate failed | Restore failed | Not attempted | Volumes left | Busy datasets | Failure rate |
|---|---|---|---|---|---|---|---|---|
| 6.9 | config | container | 2 of 4 | 2 of 2 | 26 | 0 | 0 | 66.7 % (4 failed of 6) |
| 6.9 | config | virtual-machine | 0 of 0 | 0 of 0 | 30 | 0 | 0 | n/a |

## Cycles

| LXD | Arm | Cycles | Blocked | Evacuate errors | Restore errors | Evacuate p50 | Evacuate max | Restore p50 | Restore max |
|---|---|---|---|---|---|---|---|---|---|
| 6.9 | config | 10 | 0 | 10 | 0 | 1703 ms | 15808 ms | 922 ms | 72162 ms |

## Busy datasets and their holders (STR-73)

None.

## Harness errors

None.
