# Stress run summary

**All invariants hold.** 98 faults injected, 146 recovery iterations.

## Settings

- `cluster` = `c6`
- `fault_rate` = `0.25`
- `faults` = `C3,C6,C7a,C7b,C8a,C9,C10`
- `fleet` = `9`
- `iterations` = `200`
- `seed` = `1792515598277485519`
- `session` = `ST-4`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | C10 proof tampered | fail at compare: 7 |
| config_push | C3 LXD daemon killed | fail at answer: 3, fail at resolve: 1, pass: 3 |
| config_push | C6 network partition | fail at answer: 1, fail at timeout: 4, pass: 5 |
| config_push | C7a forwarding member down | fail at answer: 1, pass: 10 |
| config_push | C7b holding member down | fail at resolve: 1, fail at timeout: 3 |
| config_push | C8a instance moved | fail at answer: 1, fail at recheck: 5 |
| config_push | C9 concurrent writer | pass: 5 |
| config_push | none | fail at answer: 2, fail at policy: 2, fail at resolve: 2, fail at write: 2, pass: 142 |
| file_pull | C10 proof tampered | fail at compare: 4 |
| file_pull | C3 LXD daemon killed | fail at answer: 5, pass: 9 |
| file_pull | C6 network partition | fail at answer: 4, pass: 4 |
| file_pull | C7a forwarding member down | fail at resolve: 2, pass: 7 |
| file_pull | C7b holding member down | fail at answer: 6, fail at policy: 1, fail at resolve: 2 |
| file_pull | C8a instance moved | fail at recheck: 4 |
| file_pull | none | fail at answer: 2, fail at file: 1, fail at policy: 4, fail at resolve: 2, pass: 143 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| cluster chaos | config_push | 200 | 0.1 | 256 ms | 405 ms | 550 ms | 550 ms |
| cluster chaos | file_pull | 200 | 0.1 | 149 ms | 211 ms | 5223 ms | 5223 ms |

## Orphans (left by crashes, INV-4)

The end-of-session sweep found 15.

- C3 (LXD daemon killed): 1
- C6 (network partition): 8
- C7a (forwarding member down): 6
- C7b (holding member down): 4

## Violations

None.
