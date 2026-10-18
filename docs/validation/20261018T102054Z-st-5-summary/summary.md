# Stress run summary

**All invariants hold.** 0 faults injected, 0 recovery iterations.

## Settings

- `fault_rate` = `0.2`
- `fleet` = `35`
- `iterations` = `0`
- `offered` = `20/s for 60 s, 16 in flight`
- `seed` = `1792318711947789959`
- `session` = `ST-5`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | none | pass: 363 |
| file_pull | none | pass: 364 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| burst 20/s | config_push | 363 | 6.0 | 234 ms | 635 ms | 877 ms | 992 ms |
| burst 20/s | file_pull | 364 | 6.0 | 135 ms | 307 ms | 461 ms | 535 ms |

## Orphans (left by crashes, INV-4)

None.

## Violations

None.
