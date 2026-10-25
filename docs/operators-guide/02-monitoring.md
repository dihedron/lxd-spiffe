# Monitoring

The plugins log through SPIRE, so their lines are in SPIRE Server's and SPIRE Agent's logs (the journal, or wherever SPIRE logs), under the node attestor's subsystem. There are no metrics endpoints: the plugin SDK offers plugins none. Derive metrics from the log lines with your log pipeline.

## The attestation log line

Every attestation the server plugin handles ends with one line. Its fields, shown here as `key=value` pairs (SPIRE's log format and field order differ, and SPIRE adds its own fields, such as the subsystem):

```text
level=info msg="attestation succeeded" correlation_id=3f9c... proof_mode=file_pull server_id=lxd-prod
  project=spire-nodes instance_uuid=6b1e... outcome=success reason_code=ok
  selectors="[server:lxd-prod uuid:6b1e... project:spire-nodes type:container privileged:false ...]"
  duration_ms=118 proof_ms=41 inflight=1
level=error msg="attestation refused" correlation_id=a07d... proof_mode=file_pull server_id=lxd-prod
  project=spire-nodes outcome=failure reason_code=proof_timeout
  error="no answer to the challenge within proof_timeout" duration_ms=10012 proof_ms=10001 inflight=3
```

| Field | Meaning |
| ------------------ | ---------------------------------------------------------------- |
| `correlation_id` | Unique per attestation; ties together every line of it |
| `outcome` | `success` or `failure` |
| `reason_code` | `ok`, or why it was refused (*Error reference*) |
| `error` | The refusal's detail: what failed, with LXD's answer where there is one |
| `proof_mode`, `server_id`, `project` | Set as soon as known: a refusal before resolution has no `server_id` |
| `instance_uuid`, `selectors` | Only on success: the audit record of the identity issued |
| `duration_ms` | The whole attestation, as the server saw it |
| `proof_ms` | From the challenge to the proof read back (0 before the challenge) |
| `inflight` | Attestations running at that moment |

## Metrics to derive

| Metric | From | Use |
| ---------------------------------- | -------------------------------------------- | ------------------------------------------ |
| Attestations by `outcome` and `reason_code` | the attestation lines | Rates, refusal ratio |
| Attestation duration | `duration_ms` | p50, p95 and p99 per `proof_mode` |
| Proof duration | `proof_ms` | VM start-up delays, slow LXD file reads |
| Concurrency | `inflight` | Headroom against `limits.max_inflight` |
| Cleanup failures | warnings with `config_push_cleanup_failures_total` | `config_push` only |
| Plugin process exits | SPIRE Server's `plugin process exited` | The alert below |

Never use the instance UUID, name or project as a metric label: they are unbounded.

## Alerts

| Alert | Condition | Meaning and first step |
| ---------------------------------- | ---------------------------------- | ------------------------------------------------------ |
| **Server plugin down** | SPIRE Server logs `plugin process exited` for `lxd_iid`, or attestations fail with `connection refused` or `Nodeattestor(lxd_iid): connection error` | SPIRE does not restart a crashed plugin; every attestation fails until SPIRE Server restarts. Restart it (*Incident procedures*). Supervising SPIRE Server's process does not catch this: it stays up |
| **No successful attestation** | zero `attestation succeeded` for longer than a quarter of `agent_ttl`, while agents exist | Agents will start losing their SVIDs: check the plugin, LXD and the network |
| **Refusal ratio high** | `attestation refused` over, say, 20 % of attestations for 15 minutes | Look at the dominant `reason_code` (*Troubleshooting*) |
| **`lxd_unavailable` or `extension_missing`** | sustained | An LXD server is down, unreachable, refuses SPIRE's identity, or was changed |
| **`proof_mismatch` or `instance_ambiguous`** | any, outside known maintenance | Possible forgery attempt or a duplicated instance: investigate (*Incident procedures*) |
| **`rate_limited`** | sustained | A flood, or `limits` too low for a mass restart |
| **Identity has too many rights** | the warning `the LXD identity has more rights than the attestor needs (SEC-16)` | SPIRE's LXD identity is an administrator: fix its grants |
| **Insecure configuration** | the warnings about `dev_mode` or `proof_mode is "none"` | Never acceptable in production |
| **Cleanup failures** | `config_push cleanup failed` | LXD refused or failed the key's removal; the key goes at the next attestation |

## The agent's side

SPIRE Agent logs `attesting`, with the claim, at the start of each attestation, and on failure `attestation failed` with the error, then SPIRE Agent's own error with the plugin's message prefixed `lxd_iid:`. The server's reason never reaches the agent: a server refusal appears there only as `PermissionDenied` with `attestation failed`. Find the server's line by time and by the claimed project and name.

## LXD's side

LXD's lifecycle events record what the SPIRE identity does: with `file_pull`, file reads; with `config_push`, instance updates touching `user.spire.challenge.*`. Audit events requested by the SPIRE identity that do anything else (`lxc monitor --type=lifecycle`, or LXD's Loki integration): they mean its credential is being used by someone else.
