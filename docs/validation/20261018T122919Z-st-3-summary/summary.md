# Stress run summary

**All invariants hold.** 413 faults injected, 702 recovery iterations.

## Settings

- `fault_rate` = `0.2`
- `faults` = `C1,C2,C3,C4,C5,C6,C9,C10`
- `fleet` = `35`
- `iterations` = `500`
- `seed` = `1792326421037194997`
- `session` = `ST-3`
- `workers` = `10`

## Outcomes

| Mode | Fault | Outcomes |
|---|---|---|
| config_push | C1 instance stopped | error: 1, fail at answer: 29, fail at connect: 1, fail at timeout: 2 |
| config_push | C10 proof tampered | fail at answer: 1, fail at compare: 21, fail at connect: 3, fail at policy: 5, fail at timeout: 1 |
| config_push | C3 LXD daemon killed | fail at answer: 19, fail at connect: 3, fail at policy: 1, fail at timeout: 1, pass: 3 |
| config_push | C4 server half killed | fail at killed: 32 |
| config_push | C5 guest half killed | fail at answer: 31, fail at connect: 4, fail at policy: 6 |
| config_push | C6 network partition | fail at policy: 2, fail at timeout: 12, pass: 11 |
| config_push | C9 concurrent writer | fail at connect: 1, fail at policy: 3, fail at timeout: 1, fail at write: 1, pass: 14 |
| config_push | none | error: 1, fail at answer: 28, fail at connect: 68, fail at policy: 79, fail at timeout: 30, pass: 585 |
| file_pull | C1 instance stopped | fail at answer: 24, fail at connect: 4, fail at policy: 2 |
| file_pull | C10 proof tampered | fail at answer: 2, fail at compare: 20, fail at connect: 2, fail at policy: 2 |
| file_pull | C2 instance restarted | fail at connect: 9, fail at policy: 7, fail at read: 10, fail at timeout: 3 |
| file_pull | C3 LXD daemon killed | fail at answer: 22, fail at connect: 2, fail at policy: 3, pass: 1 |
| file_pull | C4 server half killed | fail at killed: 30 |
| file_pull | C5 guest half killed | fail at answer: 26, fail at connect: 1, fail at policy: 4 |
| file_pull | C6 network partition | fail at answer: 1, fail at connect: 3, fail at policy: 4, fail at read: 1, fail at timeout: 10, pass: 11 |
| file_pull | none | error: 3, fail at answer: 15, fail at connect: 67, fail at policy: 93, fail at timeout: 34, pass: 584 |

## Latency of the attestor's steps (passes without faults)

| Load | Mode | Iterations | Per second | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|---|
| chaos | config_push | 1000 | 0.8 | 109 ms | 741 ms | 6365 ms | 12133 ms |
| chaos | file_pull | 1000 | 0.8 | 76 ms | 175 ms | 243 ms | 363 ms |

## Orphans (left by crashes, INV-4)

The end-of-session sweep found 121.

- C3 (LXD daemon killed): 65
- C4 (server half killed): 44
- C6 (network partition): 56

## Violations

None.
