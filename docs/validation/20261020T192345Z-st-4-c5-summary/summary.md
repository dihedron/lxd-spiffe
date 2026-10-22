# Stress run summary

**All invariants hold.** 115 faults injected, 185 recovery iterations.

## Settings

- `cluster` = `c5`
- `fault_rate` = `0.25`
- `faults` = `C3,C6,C7a,C7b,C8a,C9,C10`
- `fleet` = `9`
- `iterations` = `200`
- `seed` = `1792524204701214188`
- `session` = `ST-4`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | C10 proof tampered | fail at compare: 6 |
| config_push | C3 LXD daemon killed | fail at answer: 3, fail at policy: 1, fail at resolve: 1, pass: 8 |
| config_push | C6 network partition | fail at answer: 2, fail at timeout: 2, pass: 4 |
| config_push | C7a forwarding member down | fail at policy: 1, pass: 12 |
| config_push | C7b holding member down | fail at policy: 1, fail at resolve: 2, fail at timeout: 6 |
| config_push | C8a instance moved | fail at answer: 4, fail at policy: 2, fail at recheck: 2 |
| config_push | C9 concurrent writer | pass: 8 |
| config_push | none | fail at answer: 1, fail at cleanup: 2, fail at write: 1, pass: 131 |
| file_pull | C10 proof tampered | fail at compare: 13 |
| file_pull | C3 LXD daemon killed | fail at resolve: 1, pass: 6 |
| file_pull | C6 network partition | fail at policy: 2, pass: 2 |
| file_pull | C7a forwarding member down | fail at answer: 1, pass: 6 |
| file_pull | C7b holding member down | fail at answer: 6, fail at resolve: 2 |
| file_pull | C8a instance moved | fail at answer: 1, fail at recheck: 9, fail at resolve: 1 |
| file_pull | none | fail at answer: 2, pass: 148 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| cluster chaos | config_push | 200 | 0.1 | 228 ms | 428 ms | 734 ms | 734 ms |
| cluster chaos | file_pull | 200 | 0.1 | 107 ms | 193 ms | 383 ms | 383 ms |

## Orphans (left by crashes, INV-4)

The end-of-session sweep found 15.

- C3 (LXD daemon killed): 3
- C6 (network partition): 4
- C7a (forwarding member down): 2
- C7b (holding member down): 8

## Violations

None.
