# Stress run summary

**All invariants hold.** 0 faults injected, 0 recovery iterations.

## Settings

- `fault_rate` = `0.2`
- `fleet` = `35`
- `iterations` = `0`
- `seed` = `1792317572490599275`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | none | pass: 84 |
| file_pull | none | pass: 84 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| fleet check | config_push | 84 | 8.3 | 407 ms | 650 ms | 788 ms | 788 ms |
| fleet check | file_pull | 84 | 5.9 | 164 ms | 393 ms | 434 ms | 434 ms |

## Orphans (left by crashes, INV-4)

None.

## Violations

None.
