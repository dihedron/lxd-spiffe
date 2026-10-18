# Stress run summary

**All invariants hold.** 0 faults injected, 0 recovery iterations.

## Settings

- `fault_rate` = `0.2`
- `fleet` = `100`
- `iterations` = `500`
- `seed` = `1792341044810647472`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | none | pass: 214 |
| file_pull | none | pass: 214 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| fleet check | config_push | 214 | 8.0 | 450 ms | 690 ms | 745 ms | 753 ms |
| fleet check | file_pull | 214 | 4.8 | 211 ms | 561 ms | 2462 ms | 2507 ms |

## Orphans (left by crashes, INV-4)

The end-of-session sweep found 0.

None.

## Violations

None.
