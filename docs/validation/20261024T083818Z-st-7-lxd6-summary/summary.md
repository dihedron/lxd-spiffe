# Soak with real agents

SPIRE 1.15.5, plugins 0.0.0-SNAPSHOT-4245fee, lxd6 LXD 6.10.0

Settings: map[agent_ttl:5m0s attestation_timeout:4m0s duration:1h0m0s fault_every:1m30s fleet:100 seed:1792830892307896081 server:lxd6]

## Attestations

| mode | succeeded | duration p50 / p95 / p99 / max (ms) | proof p50 / p95 / p99 / max (ms) |
|---|---|---|---|
| file_pull | 1121 | 120 / 136 / 10343 / 17460 | 42 / 49 / 138 / 210 |
| config_push | 1346 | 147 / 203 / 12516 / 17982 | 70 / 98 / 477 / 9657 |

Throughput: 0.68 attestations per second. Refused, by reason: map[lxd_unavailable:16 proof_timeout:1 rate_limited:4]

## Invariants

INV-1, INV-4 and INV-7 held. Challenge keys left: 0.

## Findings

- the server plugin killed at 08:46:38: NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted
- the server plugin killed at 08:51:18: NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted
- 4 attestations were refused as rate_limited by the plugin's own limits

## Faults

| fault | target | start | end | proof mode | note |
|---|---|---|---|---|---|
| partition |  | 08:41:20 | 08:41:41 | file_pull | 18 s |
| stop | s-015 | 08:43:37 | 08:44:15 | file_pull |  |
| partition |  | 08:45:24 | 08:45:41 | file_pull | 13 s |
| plugin-kill |  | 08:46:38 | 08:49:12 | file_pull | NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted |
| plugin-kill |  | 08:51:18 | 08:53:53 | file_pull | NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted |
| stop | s-069 | 08:55:32 | 08:56:07 | file_pull |  |
| wipe | s-081 | 08:57:36 | 08:57:36 | file_pull |  |
| wipe | s-022 | 08:59:30 | 08:59:31 | file_pull |  |
| stop | s-093 | 09:01:09 | 09:01:45 | file_pull |  |
| partition |  | 09:03:19 | 09:03:35 | file_pull | 14 s |
| spire-restart |  | 09:05:34 | 09:05:35 | file_pull |  |
| mode-switch |  | 09:05:50 | 09:05:51 | config_push | proof_mode config_push |
| writer | s-009 | 09:06:27 | 09:06:48 | config_push |  |
| partition |  | 09:08:42 | 09:09:04 | config_push | 21 s |
| stop | s-050 | 09:11:15 | 09:11:37 | config_push |  |
| daemon |  | 09:13:09 | 09:14:06 | config_push | the fleet's containers run again |
| partition |  | 09:16:00 | 09:16:11 | config_push | 10 s |
| wipe | s-022 | 09:17:50 | 09:17:50 | config_push |  |
| stop | s-080 | 09:19:21 | 09:20:02 | config_push |  |
| wipe | s-074 | 09:21:09 | 09:21:10 | config_push |  |
| writer | s-019 | 09:22:49 | 09:23:09 | config_push |  |
| wipe | s-036 | 09:25:23 | 09:25:23 | config_push |  |
| partition |  | 09:26:43 | 09:27:04 | config_push | 21 s |
| wipe | s-018 | 09:29:08 | 09:29:09 | config_push |  |
| spire-restart |  | 09:30:41 | 09:30:42 | config_push |  |
| partition |  | 09:32:58 | 09:33:14 | config_push | 12 s |

## Memory (MiB)

| at | SPIRE Server | server plugin | LXD | agents attested |
|---|---|---|---|---|
| 08:38:20 | 109 | 24 | 322 | 100 |
| 08:39:22 | 109 | 24 | 323 | 100 |
| 08:40:24 | 110 | 25 | 324 | 100 |
| 08:41:42 | 110 | 25 | 330 | 100 |
| 08:42:44 | 110 | 25 | 317 | 100 |
| 08:44:17 | 111 | 25 | 319 | 100 |
| 08:45:19 | 111 | 25 | 322 | 100 |
| 08:46:22 | 110 | 25 | 317 | 100 |
| 08:49:14 | 87 | 21 | 319 | 100 |
| 08:50:16 | 108 | 24 | 317 | 100 |
| 08:51:18 | 109 | 24 | 318 | 100 |
| 08:53:54 | 86 | 21 | 317 | 100 |
| 08:54:56 | 106 | 24 | 319 | 100 |
| 08:56:09 | 108 | 24 | 319 | 100 |
| 08:57:10 | 109 | 24 | 322 | 100 |
| 08:58:13 | 110 | 24 | 320 | 100 |
| 08:59:15 | 109 | 24 | 318 | 100 |
| 09:00:17 | 110 | 24 | 318 | 100 |
| 09:01:46 | 110 | 25 | 321 | 100 |
| 09:02:48 | 109 | 25 | 319 | 100 |
| 09:03:52 | 110 | 25 | 320 | 100 |
| 09:04:54 | 110 | 25 | 320 | 100 |
| 09:05:57 | 96 | 23 | 324 | 100 |
| 09:06:59 | 106 | 24 | 335 | 100 |
| 09:08:01 | 109 | 24 | 324 | 100 |
| 09:09:05 | 109 | 25 | 329 | 100 |
| 09:10:07 | 109 | 25 | 333 | 100 |
| 09:11:09 | 110 | 25 | 339 | 100 |
| 09:12:13 | 110 | 25 | 347 | 100 |
| 09:14:07 | 110 | 25 | 355 | 100 |
| 09:15:09 | 111 | 25 | 343 | 100 |
| 09:16:12 | 111 | 25 | 347 | 100 |
| 09:17:14 | 110 | 25 | 358 | 100 |
| 09:18:17 | 110 | 25 | 357 | 100 |
| 09:19:19 | 110 | 25 | 365 | 100 |
| 09:20:24 | 110 | 25 | 366 | 100 |
| 09:21:26 | 111 | 25 | 369 | 100 |
| 09:22:28 | 111 | 25 | 379 | 100 |
| 09:23:30 | 111 | 25 | 373 | 100 |
| 09:24:32 | 111 | 25 | 381 | 100 |
| 09:25:35 | 110 | 25 | 388 | 100 |
| 09:26:37 | 110 | 25 | 391 | 100 |
| 09:27:41 | 111 | 25 | 398 | 100 |
| 09:28:43 | 111 | 25 | 405 | 100 |
| 09:29:45 | 111 | 25 | 409 | 100 |
| 09:30:48 | 95 | 23 | 412 | 100 |
| 09:31:50 | 107 | 24 | 416 | 100 |
| 09:32:52 | 109 | 24 | 420 | 100 |
| 09:33:20 | 110 | 25 | 426 | 100 |
| 09:34:22 | 110 | 25 | 417 | 100 |
| 09:35:24 | 110 | 25 | 419 | 100 |
| 09:36:25 | 109 | 25 | 427 | 100 |
| 09:37:27 | 110 | 25 | 418 | 100 |
