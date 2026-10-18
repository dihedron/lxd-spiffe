# Stress run summary

**All invariants hold.** 0 faults injected, 0 recovery iterations.

## Settings

- `fault_rate` = `0.2`
- `fleet` = `35`
- `iterations` = `0`
- `seed` = `1792317842018219493`
- `session` = `ST-2`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | none | pass: 336 |
| file_pull | none | pass: 336 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| concurrency 01 | config_push | 84 | 2.0 | 54 ms | 66 ms | 83 ms | 83 ms |
| concurrency 01 | file_pull | 84 | 1.1 | 50 ms | 56 ms | 102 ms | 102 ms |
| concurrency 05 | config_push | 84 | 7.1 | 132 ms | 280 ms | 318 ms | 318 ms |
| concurrency 05 | file_pull | 84 | 5.0 | 77 ms | 101 ms | 108 ms | 108 ms |
| concurrency 10 | config_push | 84 | 8.4 | 381 ms | 650 ms | 808 ms | 808 ms |
| concurrency 10 | file_pull | 84 | 6.7 | 151 ms | 261 ms | 323 ms | 323 ms |
| concurrency 20 | config_push | 84 | 9.4 | 939 ms | 1713 ms | 2176 ms | 2176 ms |
| concurrency 20 | file_pull | 84 | 7.0 | 455 ms | 760 ms | 866 ms | 866 ms |

## Orphans (left by crashes, INV-4)

None.

## Violations

None.
