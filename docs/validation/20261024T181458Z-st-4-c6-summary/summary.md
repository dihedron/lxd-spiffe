# Stress run summary

**All invariants hold.** 103 faults injected, 166 recovery iterations.

## Settings

- `cluster` = `c6`
- `fault_rate` = `0.25`
- `faults` = `C3,C6,C7a,C7b,C8a,C9,C10`
- `fleet` = `9`
- `iterations` = `200`
- `seed` = `1792865674712993441`
- `session` = `ST-4`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | C10 proof tampered | fail at compare: 9 |
| config_push | C3 LXD daemon killed | fail at answer: 3, fail at resolve: 1, pass: 3 |
| config_push | C6 network partition | fail at answer: 6, fail at resolve: 1, fail at timeout: 2, pass: 4 |
| config_push | C7a forwarding member down | fail at answer: 1, pass: 7 |
| config_push | C7b holding member down | fail at timeout: 6 |
| config_push | C8a instance moved | fail at answer: 1, fail at recheck: 5, fail at resolve: 1 |
| config_push | C9 concurrent writer | pass: 6 |
| config_push | none | fail at answer: 1, fail at policy: 2, fail at resolve: 3, fail at timeout: 1, fail at write: 2, pass: 135 |
| file_pull | C10 proof tampered | fail at compare: 9 |
| file_pull | C3 LXD daemon killed | fail at answer: 3, pass: 7 |
| file_pull | C6 network partition | fail at answer: 1, pass: 6 |
| file_pull | C7a forwarding member down | fail at resolve: 1, pass: 8 |
| file_pull | C7b holding member down | fail at answer: 2, fail at resolve: 1 |
| file_pull | C8a instance moved | fail at answer: 1, fail at recheck: 8 |
| file_pull | none | fail at read: 1, fail at resolve: 3, pass: 149 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| cluster chaos | config_push | 200 | 0.1 | 513 ms | 1682 ms | 2563 ms | 2563 ms |
| cluster chaos | file_pull | 200 | 0.1 | 262 ms | 809 ms | 6616 ms | 6616 ms |

## Orphans (left by crashes, INV-4)

The end-of-session sweep found 22.

- C3 (LXD daemon killed): 5
- C6 (network partition): 7
- C7a (forwarding member down): 4
- C7b (holding member down): 8

## Violations

None.
