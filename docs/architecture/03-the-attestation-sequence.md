# The attestation sequence

One attestation runs over SPIRE's node attestation stream between SPIRE Agent and SPIRE Server, which relay the plugins' messages. The sequence below uses `file_pull`; *Proof of co-location* describes both modes in detail.

![One attestation with file_pull](attestation-sequence.pdf){width=100%}

## Step by step

1. **The claim.** The agent plugin collects the claim, each field from the first source that has it: its configuration (`claim.project`, `claim.instance_name`); devLXD's meta-data (the instance name, as `local-hostname`, and the cloud-init instance ID); the hostname (the name only). devLXD does not tell an instance its project, so the project comes from configuration or stays empty. The plugin generates a 32-byte nonce from the operating system's random source and sends an `AttestRequest`: the claim and where its name came from, the nonce, the instance type if devLXD reported it, the proof modes it supports (`file_pull` always, `config_push` only when devLXD answered) and its version.
2. **Validation and limits.** Before any LXD call, the server plugin checks the payload strictly: version 1, at most 4 KiB, a 32-byte nonce, at most two proof modes without duplicates, an instance name and project that follow LXD's own naming rules (at most 63 bytes), a well-formed cloud-init ID. It then applies its limits: a cap on concurrent attestations, a token bucket, and a cooldown for a claim that recently failed. It chooses the proof mode: the configured one, which the agent must offer (`auto` prefers `file_pull`).
3. **Resolution.** For each configured LXD server whose allow-list contains the claimed project (or, with no project claimed, every project of every allow-list), the plugin reads `GET /1.0/instances/<name>?project=<p>`. If the claim carries a cloud-init instance ID, it must equal the instance's `volatile.cloud-init.instance-id`. Exactly one match across all servers and projects is required; none or several refuse the attestation. A server that cannot be asked also refuses it: without its answer, the match could not be shown to be the only one.
4. **Policy.** From that one read, the plugin requires: status `Running`; a project in the allow-list; the type the agent reported, if any; a valid `volatile.uuid`; not a privileged container, unless allowed; none of the denied configuration keys; and the checks of the proof mode (the isolation check and expected owner for `file_pull`, devLXD enabled for `config_push`).
5. **Uniqueness.** The plugin lists, on every configured server and project, the instances with the resolved `volatile.uuid` (`GET /1.0/instances?filter=config.volatile.uuid eq <uuid>`). Exactly one must exist, the resolved instance itself. An instance imported from a backup keeps the original's UUID, on the same server too; this check refuses both twins rather than letting one inherit the other's identity.
6. **Challenge.** With `file_pull`, the server generates a 32-byte nonce and derives the proof path from the agent's nonce (`<proof_dir>/proof-<first 16 hex digits of SHA-256(agent nonce)>`), so that the agent never chooses a path. It sends both. The server nonce is valid for `proof_timeout` only, at most two minutes.
7. **Proof.** The agent checks that the path is its own (inside its `proof_dir`, derived from its own nonce), computes `SHA-256("spire-lxd-v1" 0x00, length-prefixed project, length-prefixed name, server nonce, agent nonce)` with the claim values it sent, and writes the 64 hexadecimal characters to a new file, mode 0600, in a root-owned 0700 directory, refusing symlinks and existing files.
8. **Answer.** The agent replies that the step is done.
9. **Read-back.** The server reads the file through LXD's files API, retrying while a VM's `lxd-agent` starts, and requires a regular file owned by uid 0 with mode 0600, exactly 64 bytes, equal to the proof it computes itself (compared in constant time). It also requires the proof directory to be a root-owned 0700 directory.
10. **Re-verification.** The plugin reads the instance again. It must still be running, with the same name, project, `volatile.uuid`, `volatile.uuid.generation` and location; otherwise the attestation is refused (`instance_moved` when only the location changed).
11. **Identity.** The plugin renders the SPIFFE ID path (`lxd_iid/<server_id>/<volatile.uuid>`), builds the enabled selectors from the first read, logs the outcome with the selectors issued, and returns them to SPIRE Server with `can_reattest` as configured. SPIRE Server issues the agent SVID.

Every refusal, at any step, answers the agent with the same `PermissionDenied: attestation failed`, and logs the reason code and the detail on SPIRE Server under a correlation ID. Nothing in the answer tells an outsider whether an instance exists.

## Timing

The steps that wait are bounded: every LXD call has a deadline; the proof wait is bounded by the server's `proof_timeout`, the whole attestation on the agent by `attestation_timeout`. In the lab, a `file_pull` attestation took about 120 ms at the median on a quiet LXD 6.10.0 server with 100 agents, `config_push` about 150 ms; the slow tail comes from LXD's own latency during restarts and from virtual machines whose `lxd-agent` is starting.

## The wire protocol

The messages the plugins exchange, as defined in the source (`internal/proto/lxd_iid.proto`):

```{.protobuf include="internal/proto/lxd_iid.proto"}
```
