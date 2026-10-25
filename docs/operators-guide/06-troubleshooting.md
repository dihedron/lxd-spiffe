# Troubleshooting

Start from the symptom, find the server's `attestation refused` line for the attestation (by time, and by the agent's claimed project and name), and read its `reason_code` and `error`. The *Error reference* explains every message.

## An agent does not attest

1. **Does the agent reach SPIRE Server?** SPIRE Agent's log shows connection errors otherwise. Check the address, the port, the firewall and the trust bundle.
2. **Did the agent plugin start?** `the agent plugin must run as uid 0`: SPIRE Agent runs as another user. `invalid plugin configuration`: a key or value is wrong; the message names it. A checksum mismatch: `plugin_checksum` is not the installed binary's.
3. **Did the attestation reach the server plugin?** Look for `attestation refused` or `attestation succeeded` at that time. Nothing at all: the server plugin crashed (*Incident procedures*), or SPIRE Server refused the attestation before the plugin (its own rate limit, the agent's TLS).
4. **The server refused it: read `reason_code`.**

| `reason_code` | First things to check |
| ---------------------- | ------------------------------------------------------------------------------- |
| `scope_denied` | The agent's `claim.project` and the instance's project are in the server's `projects` |
| `instance_not_found` | The claimed name is the LXD instance name (not a different hostname); the project; on a cluster, that the instance exists; with a cloud-init ID claimed, that the instance was not renamed since boot |
| `instance_ambiguous` | Two allowed projects with an instance of that name (set `claim.project`), or two instances with one `volatile.uuid` (an import: *Incident procedures*) |
| `not_running` | The instance is stopping, frozen or moving |
| `policy_denied` | The `error`: a privileged container, a denied config key, a disk at the proof directory, `security.devlxd` off with `config_push`, a proof mode the agent does not offer |
| `proof_timeout` | The agent's `proof_dir` and the server's match; the agent is not stuck; for VMs, `lxd-agent` is up; raise `proof_timeout` |
| `vm_agent_unavailable` | `lxd-agent` in the VM: started, ordered before SPIRE Agent, SELinux drop-in on RHEL 9 derivatives |
| `proof_mismatch` | The proof directory: root-owned, mode 0700, not a symlink, not on shared storage; the same `proof_dir` on both sides; SPIRE Agent as root |
| `lxd_forbidden` | The instance grant (`can_access_files` or `can_edit`) for this instance; SPIRE's identity, its credential and its groups |
| `lxd_unavailable` | The server's reachability and pin (*Incident procedures*) |
| `extension_missing` | The LXD version behind the URL (5.21 or later); `auth_bearer` for a token entry |
| `rate_limited` | A claim failing repeatedly (it cools down), or `limits` exceeded by a burst |
| `precondition_failed` | Another tool writing the instance's configuration continuously |
| `instance_moved` | A migration or evacuation during the attestation; it retries |
| `project_unreadable` | Project `can_view` for SPIRE's group on the instance's project |
| `payload_invalid` | Agent and server of incompatible versions, or something else than the agent plugin talking |

## Attestations are slow

- `proof_ms` close to `duration_ms`: the time goes into the proof. For VMs, `lxd-agent` starting; for `config_push`, LXD serializing writes to the instance.
- `duration_ms` high but `proof_ms` low: the LXD lookups. Many allowed projects without `claim.project` cost one read per project; a slow LXD server or a cluster member forwarding to a busy one.
- `inflight` near `limits.max_inflight`: attestations queue on LXD; look at LXD's load.
- The agent fails with `attestation_timeout of ...` while the server shows nothing slow: the time went before the plugin, in SPIRE Server's per-address rate limit queue (many agents behind one NAT address).

## Attestations fail after an LXD upgrade

- `extension_missing`: the new version lacks an extension the plugin requires: report it.
- `proof_mismatch` with an unexpected owner: LXD changed how it reports file owners. Report it with the `error`.
- Any new refusal: run the *Setup Guide*'s *End-to-end check* against the upgraded server.

## Many attestations at once

After an LXD daemon crash, a `snap refresh` or a SPIRE Server outage longer than the SVIDs' lifetime, every affected agent attests at about the same time. Expect `rate_limited` from the plugin's limits and queuing in SPIRE Server's per-address rate limit; agents retry with a backoff and converge. If it takes too long, raise `limits.rate_per_sec`, `limits.max_inflight` and the agents' `attestation_timeout` (*Operational guidance*).

## Agents attest too often

Agents of this attestor attest again at about half their SVID's lifetime. With a short `agent_ttl` and many agents, that is the steady attestation rate (*Operating model*). A single agent attesting much more often is failing: look for its refusals.
