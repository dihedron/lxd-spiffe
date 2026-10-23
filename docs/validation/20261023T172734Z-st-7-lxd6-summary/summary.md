# Soak with real agents

SPIRE 1.15.5, plugins 0.0.0-SNAPSHOT-e2f13e2, lxd6 LXD 6.9

Settings: map[agent_ttl:5m0s attestation_timeout:4m0s duration:1h0m0s fault_every:1m30s fleet:100 seed:1792776246329213163 server:lxd6]

## Attestations

| mode | succeeded | duration p50 / p95 / p99 / max (ms) | proof p50 / p95 / p99 / max (ms) |
|---|---|---|---|
| file_pull | 1120 | 161 / 181 / 207 / 303 | 43 / 49 / 54 / 113 |
| config_push | 1341 | 205 / 252 / 8357 / 17829 | 83 / 108 / 3662 / 10068 |

Throughput: 0.68 attestations per second. Refused, by reason: map[lxd_unavailable:19 project_unreadable:1 rate_limited:1]

## Invariants

INV-1, INV-4 and INV-7 held. Challenge keys left: 0.

## Findings

- the server plugin killed at 17:40:56: NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted
- the server plugin killed at 17:45:30: NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted
- the server plugin killed at 18:15:23: NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted
- 1 attestations were refused as rate_limited by the plugin's own limits

## Faults

| fault | target | start | end | proof mode | note |
|---|---|---|---|---|---|
| spire-restart |  | 17:30:35 | 17:30:36 | file_pull |  |
| stop | s-044 | 17:32:37 | 17:33:02 | file_pull |  |
| stop | s-099 | 17:35:14 | 17:35:49 | file_pull |  |
| spire-restart |  | 17:37:06 | 17:37:13 | file_pull |  |
| stop | s-084 | 17:38:48 | 17:39:06 | file_pull |  |
| plugin-kill |  | 17:40:56 | 17:43:31 | file_pull | NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted |
| plugin-kill |  | 17:45:30 | 17:48:05 | file_pull | NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted |
| stop | s-089 | 17:49:31 | 17:49:59 | file_pull |  |
| spire-restart |  | 17:51:54 | 17:51:54 | file_pull |  |
| wipe | s-039 | 17:53:44 | 17:53:44 | file_pull |  |
| mode-switch |  | 17:55:07 | 17:55:08 | config_push | proof_mode config_push |
| daemon |  | 17:55:46 | 17:56:47 | config_push | the fleet's containers run again |
| partition |  | 17:58:32 | 17:58:42 | config_push | 10 s |
| partition |  | 18:00:12 | 18:00:39 | config_push | 22 s |
| writer | s-038 | 18:01:36 | 18:01:56 | config_push |  |
| stop | s-026 | 18:03:16 | 18:03:33 | config_push |  |
| stop | s-031 | 18:04:27 | 18:05:08 | config_push |  |
| daemon |  | 18:07:25 | 18:08:23 | config_push | the fleet's containers run again |
| spire-restart |  | 18:10:39 | 18:10:46 | config_push |  |
| spire-restart |  | 18:11:38 | 18:11:40 | config_push |  |
| stop | s-097 | 18:13:41 | 18:14:11 | config_push |  |
| plugin-kill |  | 18:15:23 | 18:17:58 | config_push | NOT recovered by SPIRE: the plugin process did not come back in 150 s; SPIRE Server restarted |
| wipe | s-008 | 18:20:04 | 18:20:05 | config_push |  |
| wipe | s-073 | 18:22:19 | 18:22:20 | config_push |  |

## Memory (MiB)

| at | SPIRE Server | server plugin | LXD | agents attested |
|---|---|---|---|---|
| 17:27:36 | 113 | 25 | 333 | 100 |
| 17:28:38 | 110 | 25 | 333 | 100 |
| 17:29:40 | 112 | 25 | 331 | 100 |
| 17:30:43 | 96 | 23 | 333 | 100 |
| 17:31:45 | 107 | 24 | 332 | 100 |
| 17:33:03 | 110 | 24 | 331 | 100 |
| 17:34:05 | 109 | 25 | 333 | 100 |
| 17:35:07 | 109 | 25 | 334 | 100 |
| 17:36:10 | 110 | 25 | 334 | 100 |
| 17:37:14 | 87 | 23 | 335 | 100 |
| 17:38:16 | 107 | 24 | 335 | 100 |
| 17:39:18 | 109 | 24 | 333 | 100 |
| 17:40:20 | 111 | 25 | 330 | 100 |
| 17:43:32 | 88 | 21 | 336 | 100 |
| 17:44:34 | 106 | 24 | 332 | 100 |
| 17:48:06 | 87 | 21 | 333 | 100 |
| 17:49:09 | 106 | 24 | 334 | 100 |
| 17:50:11 | 109 | 24 | 332 | 100 |
| 17:51:13 | 111 | 24 | 333 | 100 |
| 17:52:16 | 102 | 24 | 334 | 100 |
| 17:53:18 | 107 | 24 | 331 | 100 |
| 17:54:21 | 109 | 24 | 334 | 100 |
| 17:55:25 | 100 | 24 | 336 | 100 |
| 17:56:48 | 110 | 24 | 305 | 100 |
| 17:57:50 | 110 | 25 | 313 | 100 |
| 17:58:54 | 111 | 25 | 315 | 100 |
| 17:59:56 | 111 | 25 | 323 | 100 |
| 18:01:01 | 111 | 25 | 328 | 100 |
| 18:02:02 | 111 | 25 | 319 | 100 |
| 18:03:05 | 110 | 25 | 326 | 100 |
| 18:04:05 | 110 | 25 | 334 | 100 |
| 18:05:10 | 111 | 25 | 340 | 100 |
| 18:06:12 | 111 | 25 | 342 | 100 |
| 18:07:14 | 111 | 25 | 351 | 100 |
| 18:08:24 | 108 | 25 | 338 | 100 |
| 18:09:26 | 110 | 26 | 345 | 100 |
| 18:10:29 | 112 | 25 | 348 | 100 |
| 18:11:33 | 109 | 24 | 358 | 100 |
| 18:12:37 | 108 | 24 | 364 | 100 |
| 18:13:39 | 109 | 24 | 368 | 100 |
| 18:14:42 | 110 | 25 | 377 | 100 |
| 18:18:00 | 87 | 23 | 390 | 100 |
| 18:19:02 | 106 | 24 | 396 | 100 |
| 18:20:04 | 109 | 24 | 385 | 100 |
| 18:21:07 | 110 | 25 | 394 | 100 |
| 18:22:09 | 109 | 25 | 397 | 100 |
| 18:22:36 | 109 | 25 | 402 | 100 |
| 18:23:38 | 109 | 25 | 408 | 100 |
| 18:24:40 | 109 | 25 | 411 | 100 |
| 18:25:43 | 108 | 25 | 416 | 100 |
| 18:26:45 | 109 | 25 | 420 | 100 |
