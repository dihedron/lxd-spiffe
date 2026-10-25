# Observability

The plugins log through SPIRE's logger, so their lines appear in SPIRE Server's and SPIRE Agent's logs, in SPIRE's format, under the node attestor's subsystem. The SPIRE plugin SDK offers plugins no metrics interface; the plugins' metrics are fields of their log lines, from which a log pipeline derives counters and histograms.

## The server plugin's log

Every attestation ends with **one line**:

- `attestation succeeded` (info), with `outcome` `success`, `reason_code` `ok`, the `selectors` issued, `instance_uuid`, and the attestation's `duration_ms` and `proof_ms`;
- or `attestation refused` (error), with `outcome` `failure`, a `reason_code`, an `error` giving the detail, `duration_ms` and `proof_ms`.

Both carry a `correlation_id`, unique per attestation, and as soon as they are known the `proof_mode`, `server_id`, `project` and `inflight` (attestations running at that moment). The success line is the audit record of the identity issued: it names the instance by UUID and lists every selector.

The reason codes: `ok`, `payload_invalid`, `instance_not_found`, `instance_ambiguous`, `not_running`, `scope_denied`, `policy_denied`, `proof_timeout`, `proof_mismatch`, `vm_agent_unavailable`, `lxd_unavailable`, `extension_missing`, `rate_limited`, `precondition_failed`, `instance_moved`, `project_unreadable`. The *Operator's Guide* explains each.

Other lines:

- warnings at startup for a configuration that weakens security (`dev_mode`, `proof_mode = "none"`), and on every claim-only attestation;
- a warning when the plugin's LXD identity looks like an administrator, at its first connection to each server;
- an error, once until it changes, when a server is refused (missing extensions, untrusted identity, unreachable);
- a warning for every selector left out by the value rule;
- with `config_push`, a warning for every failed cleanup of a challenge key (field `config_push_cleanup_failures_total`).

## Metrics derived from the log

| Metric | From |
| ------------------------------ | ------------------------------------------------------ |
| Attestations by outcome and reason | `outcome` and `reason_code` of the attestation lines |
| Attestation latency | `duration_ms` (the whole attestation) |
| Proof latency | `proof_ms` (challenge to proof read back) |
| Concurrency | `inflight` |
| Claim-only mode active | the warning of every claim-only attestation |
| `config_push` cleanup failures | the warnings with `config_push_cleanup_failures_total` |

No metric uses names or UUIDs as labels: the cardinality stays bounded.

## What is never logged

Nonces, proofs, challenge values and credentials never appear in any log, on either side. The agent logs its claim (project, name, where the name came from) at info level.

## The agent plugin's log

The agent plugin logs `attesting` with its claim, where the name came from and the instance type devLXD reported, and `attestation failed` with the error when an attestation fails; SPIRE Agent then logs the error the plugin returned, prefixed `lxd_iid:`. The server's refusal reaches the agent only as `PermissionDenied: attestation failed`, by design: the reason is in SPIRE Server's log, found by time and claim.
