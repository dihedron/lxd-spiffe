# Proof of co-location

Resolving the claim tells the server which instance the agent *says* it is. The proof of co-location shows that the party on the attestation stream can act inside that instance, now. Because the agent cannot send anything LXD-verifiable, the server always uses its own LXD credential to reach into the instance; the two modes differ in which direction the data travels and which LXD entitlement it needs. Both bind the evidence to a fresh server nonce and to the agent's nonce, so a proof is useless for any other attestation.

## `file_pull` (the default)

The agent writes, the server reads.

1. The server picks a 32-byte nonce and the proof path `<proof_dir>/proof-<id>`, where `<id>` is the first 16 hexadecimal digits of SHA-256 of the agent's nonce: the path is derived, never chosen by the client.
2. The agent computes the proof, SHA-256 over a domain separator (`spire-lxd-v1` and a zero byte), the length-prefixed project and instance name it claimed, the server nonce and its own nonce, and writes its 64 lowercase hexadecimal characters to the proof file: created exclusively (`O_CREAT|O_EXCL|O_NOFOLLOW`), mode 0600, synced, in a directory the agent creates with mode 0700 and checks again through the handle it creates the file through, so that a directory swapped in between is refused. It removes the file when the attestation ends.
3. The server reads the file through `GET /1.0/instances/<name>/files?path=<proof_path>`. It requires LXD's headers to say a regular file (`X-LXD-type: file`; a symlink is reported as such and not followed), owner uid 0 and mode 0600, a body of exactly 64 bytes, and the proof it computes itself, compared in constant time. It also reads the directory and requires a root-owned 0700 directory.

**Why it proves presence.** Only code that can write inside the instance's filesystem can create the file, and a root-owned file in a root-only directory under `/run` is evidence of *privileged* presence: an unprivileged user in the instance cannot create it. LXD reports file owners as seen inside the instance, for VMs and for privileged, unprivileged, isolated and raw-idmap containers alike (measured on 5.21.8 and 6.9), so the one accepted owner is uid 0. Accepting either of two values is never allowed; should an LXD version report host-side owners, the expected owner becomes the host uid that the container's uid 0 maps to, computed from `volatile.idmap.current`, and the plugin fails closed on a map it cannot translate unambiguously.

**The isolation check.** If another instance could write into this instance's proof directory, it could forge the proof. Before the challenge, the server inspects the instance's expanded devices and refuses an instance with any non-root `disk` device whose path is at, above or below the proof directory. The lab showed that every such shared disk let another instance write a proof that the files API reported as uid 0 and mode 0600, and that the check refused every such case. Keeping the proof directory on a tmpfs (`/run`) also keeps proofs out of snapshots, copies and backups.

**Virtual machines.** In a VM, the files API is served by `lxd-agent` inside the guest. While it starts, LXD answers 500 ("LXD VM agent is not currently running") and, briefly after, 404. The server treats both as "not ready yet" and retries, with a backoff from 250 ms to 2 s, within `proof_timeout`, then reports `vm_agent_unavailable` or `proof_timeout`. Any other error fails at once.

**Entitlement.** Instance `can_access_files`, which also allows pushing and pulling any file in the instance. The plugin only reads, and only the path it derived.

## `config_push`

The server writes, the agent reads.

1. The server writes `user.spire.challenge.<id>` = the hexadecimal server nonce into the instance's configuration with a `PATCH` carrying the instance's ETag in `If-Match`, and sends the key's name. The same `PATCH` removes the instance's stale challenge keys, those left by attestations whose cleanup could not run, except the keys of attestations this server process is still running for the instance.
2. The agent checks that the key is the one derived from its own nonce, reads it through devLXD (`GET /1.0/config/<key>`), checks that the value is 64 lowercase hexadecimal characters, and sends it back.
3. The server compares it with its nonce in constant time, then removes the key with a second `PATCH`, also on failure and timeout.

**Why it proves presence.** devLXD returns only the calling instance's own data, so another instance cannot read the value; and only root inside the instance can use devLXD (containers answer other users with `401`, a VM's socket is mode 0600, owned by root), so the guest-side gate equals `file_pull`'s, as long as LXD keeps this behaviour, which the lab checks for every LXD version. A `PATCH` of a `user.*` key on a running instance is applied without a restart and is visible through devLXD within about a millisecond of LXD's answer.

**ETags and retries.** If another writer changed the instance between the server's read and its write, LXD answers `412 Precondition Failed`. The server then reads the instance again, runs every policy check again on the new record, aborts if its identity, generation, project, name or location changed, and retries with the same challenge value, with an exponential backoff (50 ms to 1 s, with jitter), at most 10 times and never beyond `proof_timeout` (`precondition_failed`). In the lab, eight writers racing on one instance needed up to five retries, and an idle instance's ETag never changed by itself.

**Costs.** The value is visible for a few seconds to everyone who can view the instance's configuration; it is single-use and useless after the attestation. A key whose cleanup could not run (LXD unreachable, the server killed) stays until the instance's next `config_push` attestation removes it. **Entitlement**: instance `can_edit`, a broad grant: whoever holds it can change any setting of the instance, devices and low-level settings included, which can reach the host unless the project is restricted.

## Mode negotiation

The server's `proof_mode` decides: `file_pull`, `config_push`, or `auto`, which takes the first of `file_pull` and `config_push` that the agent offers. The server never downgrades silently: an agent that does not offer the required mode is refused. `none`, a claim without any proof, is refused at configuration time unless explicitly acknowledged as insecure, and then logs a warning on every attestation.

## The privilege cost of each mode

The proof's privilege cost on the LXD side is the design's main security trade-off:

| Mode | SPIRE identity's grants | If the identity is stolen | Guest-side gate |
| ---------- | -------------------------------- | ------------------------------------------ | ------------------------ |
| `file_pull` | project `can_view`, `can_view_instances`; instance `can_access_files` | Read and **write** any file in the granted instances; no host-level change by itself | Root writes a file in a 0700 directory |
| `config_push` | project `can_view`, `can_view_instances`; instance `can_edit` | Change any setting of the granted instances, devices and `raw.*` included: **host-level impact** in a project that is not restricted | Root reads devLXD |
| claim only | project `can_view`, `can_view_instances` | Read-only | None (refused by default) |

`can_access_files` and `can_edit` are separate: `can_edit` does not read files. Hence the recommendations: a dedicated identity limited to dedicated projects; restricted projects for `config_push`; `file_pull` where possible.
