# Configuration reference

This chapter describes every key of the two plugins' `plugin_data` blocks: the server plugin's, in SPIRE Server's configuration, and the agent plugin's, in SPIRE Agent's. For each key it gives its type, default and accepted values, what it is for, how it depends on other keys, its caveats and, where a choice affects security, the threat it bears on and the safe choice. Threat IDs (such as `T10`) refer to the threat register in *Architecture and Design*.

Some conventions:

- **Durations** are Go durations: `500ms`, `10s`, `2m`.
- **Unknown keys are errors**: the plugin refuses the configuration and lists the keys it knows. A typo never silently falls back to a default.
- **Empty or absent means the default**, for every key that has one.
- The plugins check their configuration when SPIRE starts or reloads them, and refuse it as a whole on any error, which stops SPIRE with a message naming the key (`invalid plugin configuration: ...`). The server plugin contacts no LXD server at that point.

## Server plugin

The `plugin_data` block of `NodeAttestor "lxd_iid"` in SPIRE Server's configuration. The sample `/usr/share/doc/lxd-server-plugin/server.conf` holds every key.

### Identity

`agent_path_template`
:   *Go template, default `{{ .PluginName }}/{{ .ServerID }}/{{ .InstanceUUID }}`.* The path of the agent's SPIFFE ID after `spiffe://<trust-domain>/spire/agent/`. It may use only `.PluginName` (`lxd_iid`), `.ServerID` (the `servers` entry's key) and `.InstanceUUID` (the instance's `volatile.uuid`): every value comes from configuration or LXD, never from the guest. Every segment of the rendered path must be a valid SPIFFE ID path segment (1 to 63 letters, digits, `.`, `-` and `_`, not `.` or `..`); the plugin renders the template for each server at startup and refuses one that fails. Keep both `.ServerID` and `.InstanceUUID`: the UUID is unique per LXD server only (an instance imported from a backup keeps it), so the server ID is what keeps IDs from different servers apart (`T7`). Changing the template changes every agent's SPIFFE ID, and the node entries that name agents by ID.

### Proof

`proof_mode`
:   *`file_pull`, `config_push`, `auto` or `none`, default `file_pull`.* How the server proves that the agent runs inside the instance (*Choosing the proof mode*). `file_pull` reads a proof file through the files API and needs instance `can_access_files`; `config_push` writes a challenge key and needs instance `can_edit`; `auto` takes `file_pull` when the agent offers it and `config_push` otherwise, so the SPIRE identity needs the grants of both. The agent must offer the mode, or the attestation is refused (`policy_denied`). `none` issues identities on the claim alone, with no proof at all: anyone who can reach SPIRE Server and guess an instance name gets that instance's identity (`T1`). It is refused unless `i_understand_claim_only_is_insecure = true`, and logs a warning at startup and on every attestation. Never use it outside a test.

`i_understand_claim_only_is_insecure`
:   *Boolean, default `false`.* The acknowledgement `proof_mode = "none"` requires. It has no effect with any other mode.

`proof_timeout`
:   *Duration, default `10s`, from `1s` to `2m`.* How long the server waits for the proof after sending its challenge: the agent's answer and, with `file_pull`, the proof file's read, retried while a VM's `lxd-agent` is starting. It is also the lifetime of the server's nonce. Raise it for VMs whose SPIRE Agent may start before `lxd-agent` (`90s` covers the slowest families the lab measured), and keep the agent's `proof_timeout` at least as long. Too short: VM attestations fail with `vm_agent_unavailable` or `proof_timeout` after a cold boot. Too long: an attestation against a stalled agent holds a slot of `limits.max_inflight` that long.

`proof_dir`
:   *Absolute path, default `/run/spire/lxd`.* The directory in the guest where the agent writes the proof file (`file_pull`). It must equal the agent's `proof_dir`, or the agent refuses the challenge. It must be absolute, clean, not `/`, and short enough that a proof path (`<proof_dir>/proof-<16 hex digits>`) fits 128 bytes. The server also uses it for the isolation check: it refuses an instance with a disk device at, above or below this directory (`T2`). Keep it on a tmpfs such as `/run`, so that no copy, snapshot or backup of the instance carries a proof.

`reattest`
:   *`allow` or `deny`, default `allow`.* Whether an agent that holds a valid node identity may attest again. Agents of this attestor attest again at about half their SVID's lifetime rather than renewing it, so `allow` keeps them going without an operator. `deny` stops an agent whose SVID expired, or that lost its data, until an operator evicts it (`spire-server agent evict`): use it where every re-attestation must be approved, and expect to evict agents after every instance restore (`T13`, `T14`).

### Policy

`policy.allow_privileged`
:   *Boolean, default `false`.* Whether privileged containers (`security.privileged=true`) may attest. Root in a privileged container is root on the host, and so can take over every instance and its identity (`T15`). Keep `false`; where a privileged container must attest, give it entries that do not trust it like the others (the `privileged` selector).

`policy.deny_config_keys`
:   *List of configuration keys, default none.* Instance configuration keys that refuse the attestation when set to a non-empty value, in the instance's expanded configuration (profiles included). List the low-level keys that weaken isolation, such as `raw.lxc`, `raw.idmap`, `raw.qemu` and `raw.apparmor`, unless the project's restrictions already forbid them (`T15`). An empty key is refused.

### LXD servers

`servers`
:   *Map, at least one entry.* One entry per LXD server or cluster. The entry's key is its `server_id`: a SPIFFE ID path segment (1 to 63 letters, digits, `.`, `-` and `_`, not `.` or `..`) that becomes part of every SPIFFE ID the plugin issues for that server's instances and of the `server` selector. Keep it unique across every LXD server SPIRE attests instances of, and never change it (`T7`). A cluster is one entry (*Clusters*). An instance's claim is looked up on every server whose `projects` allow it, and must match exactly one instance across all of them; a server that cannot be reached fails the attestation, since without it the match could not be shown to be the only one.

`servers.*.url`
:   *`https://host[:port]`, required.* The LXD API address. No path, user, query or fragment. For a cluster, a load balancer or virtual address in front of the members, or one member's address if attestation may stop while that member is down. The plugin never follows redirects.

`servers.*.client_cert_file`
:   *Path.* The SPIRE identity's client certificate (PEM), for mutual TLS. Required with `client_key_file`, unless `bearer_token_file` is set; not both. The plugin reloads it when the file changes, so a renewed certificate takes effect without a restart.

`servers.*.client_key_file`
:   *Path.* The client certificate's private key. It must not be readable by group or others (mode `0400` or `0600`); the plugin refuses it otherwise. Whoever holds it acts as SPIRE towards LXD, with the grants of *Preparing LXD* (`T10`).

`servers.*.bearer_token_file`
:   *Path, LXD 6.x only.* A file holding an LXD bearer token for the SPIRE identity, instead of the client certificate. Same permission rule as the key. The token must be a signed JWT as LXD issues it: an unsigned one (`alg` `none`, or no signature) is refused. The plugin presents it and never trusts its content; it rereads the file when it changes, and once more after LXD refuses it, before failing. An entry with a token is refused at attestation time by a server without the `auth_bearer` extension (LXD 5.21).

`servers.*.server_cert_fingerprint`
:   *SHA-256 fingerprint.* The LXD server certificate's fingerprint, 64 hexadecimal digits, with or without a `sha256:` prefix and colons. The plugin accepts only a server presenting that certificate (`T5`). Exactly one of this and `ca_bundle_file` is required, unless `dev_mode`. Take it from the server (`lxc info`), not over the network.

`servers.*.ca_bundle_file`
:   *Path.* A PEM bundle of CA certificates to verify the LXD server's certificate against, for servers whose certificate a CA issued, instead of the fingerprint.

`servers.*.projects`
:   *List of project names, at least one.* The projects whose instances may attest through this server: the project allow-list. An instance in any other project is refused (`scope_denied`), and the uniqueness check of `volatile.uuid` covers these projects. Each must be a valid LXD project name, listed once. The SPIRE identity needs project `can_view` and `can_view_instances` on each.

### Selectors

`selectors.enable`
:   *List of selector names, default `["server", "uuid", "project", "type", "privileged", "profile", "network"]`.* The selectors the plugin issues. Names: `server`, `uuid`, `project`, `type`, `arch`, `privileged`, `profile`, `network`, `member`, `base-image`, `image`, `secureboot`, `generation`, `name`. An empty list issues none but the configured `config` and `user` selectors; an unknown or repeated name is refused. Enable only what your registration entries use; prefer high-trust selectors (*Installing the server plugin*, *Registration entries*). Enabling `network` makes the plugin read the instance's project, for its `features.networks`, when the instance has a NIC on a managed network.

`selectors.config_keys`
:   *List of configuration keys, default none.* Instance configuration keys (full names, not `user.*`) to issue as `config:<key>:<value>` selectors, from the expanded configuration. A key that is not set gives no selector. Keys are printable ASCII without spaces or `:`, up to 256 bytes. These are medium-trust: whoever can edit the instance or its profiles sets them (`T9`).

`selectors.user_keys`
:   *List of keys, default none.* `user.*` keys, without their `user.` prefix, to issue as `user:<key>:<value>` selectors. Same rules as `config_keys`. Any editor of the instance can set `user.*` keys: use them only where LXD access control restricts who edits these instances (`T9`).

### Limits

The limits protect LXD and SPIRE Server from floods of attestations, which anyone who reaches SPIRE Server can start before authenticating (`T11`). They apply before any LXD call; an attestation over a limit is refused at once (`rate_limited`), not queued.

`limits.max_inflight`
:   *Integer, default `16`, from `1` to `1024`.* The most attestations the plugin runs at once.

`limits.rate_per_sec`
:   *Integer, default `20`, from `1` to `10000`.* The sustained attestation rate, a token bucket. After an LXD daemon restart, every container of that server attests again at about the same time: size this for the containers of one LXD server, or expect their attestations to be refused and retried for a while.

`limits.failure_cooldown`
:   *Duration, default `5s`, from `100ms` to `5m`.* After a failed attestation, further attempts with the same claimed project and instance name are refused for this long; each further failure doubles it, up to 5 minutes, and a success resets it. A claim that keeps failing therefore costs LXD little.

`limits.breaker_threshold`
:   *Integer, default `5`, from `1` to `100`.* The circuit breaker of each LXD server opens after this many transport failures in a row (connection errors, timeouts, server errors other than a VM's `lxd-agent` starting, invalid responses). Any answer from LXD, a refusal included, resets the count. While open, attestations through that server fail at once (`lxd_unavailable`) without calling it.

`limits.breaker_open`
:   *Duration, default `5s`, from `1s` to `5m`.* How long the breaker stays open before one trial attestation goes through on a new connection; the trial's success closes it, its failure reopens it for twice as long.

`limits.breaker_max_open`
:   *Duration, default `60s` (or `breaker_open` if longer), from `breaker_open` to `1h`.* The longest the breaker stays open between trials.

### Development

`dev_mode`
:   *Boolean, default `false`.* Lets a server entry have neither `server_cert_fingerprint` nor `ca_bundle_file`, and then does not verify that server's certificate at all, logging a warning at startup and on every connection: anyone on the path to LXD can then impersonate it and make the plugin attest what they want (`T5`). A pin or CA bundle, if set, is still enforced. For test labs only.

## Agent plugin

The `plugin_data` block of `NodeAttestor "lxd_iid"` in SPIRE Agent's configuration. The sample `/usr/share/doc/lxd-agent-plugin/agent.conf` holds every key. The agent plugin refuses to start unless SPIRE Agent runs as root.

### Claim

`claim.project`
:   *Project name, default empty.* The project the agent claims its instance is in. An instance cannot learn its project from inside, so set it: without it, the server looks the instance's name up in every allowed project, and refuses it as ambiguous if two of them have an instance of that name. The claim is only a lookup hint: a wrong project makes the attestation fail, never succeed for another instance.

`claim.instance_name`
:   *Instance name, default empty.* The instance name the agent claims. Empty: the name devLXD reports, or the hostname when devLXD is not available. Set it only where neither gives the LXD instance name (a hostname that differs from the instance name, with devLXD off).

### devLXD

`use_devlxd`
:   *`auto`, `required` or `off`, default `auto`.* Whether the agent uses devLXD (`/dev/lxd/sock`), the instance's local LXD API. Through it the agent learns its instance name, its cloud-init instance ID (which the server checks against LXD's, against a stale or wrong name) and its instance type, and it is how `config_push` reads the challenge. `auto` uses it when it answers and carries on without it otherwise, offering `file_pull` only; `required` fails the attestation without it (`devLXD is required but not available`); `off` never uses it, so `config_push` is not offered.

`devlxd_socket`
:   *Path, default `/dev/lxd/sock`.* The devLXD socket. Change it only for a guest that mounts it elsewhere.

### Proof and timeouts

`proof_dir`
:   *Absolute path, default `/run/spire/lxd`.* Where the agent writes the `file_pull` proof file; it must equal the server's `proof_dir`. The agent creates it (and missing parents) with mode `0700`, owned by root, and refuses a directory with another owner or mode, or a symlink. Keep it on a tmpfs: a proof must never survive into a copy or a backup.

`proof_timeout`
:   *Duration, default `15s`, from `1s` to `2m`, shorter than `attestation_timeout`.* How long the agent keeps the proof after answering the challenge, while the server reads it; it then removes the proof. Keep it at least as long as the server's `proof_timeout`, or the proof can vanish while the server still retries (for VMs, `90s` with the server at `90s`).

`attestation_timeout`
:   *Duration, default `30s`, from `2s` to `5m`.* The longest one attestation may take on the agent, from its first message to the server's verdict, the time the proof stays included. It must cover the server's queue: with many agents behind one NAT address and SPIRE Server's per-address rate limit on, raise it to cover the whole queue (`4m` for 100 agents behind one address). Too short: attestations fail with `attestation_timeout of ...`, and SPIRE Agent retries with a backoff. Too long: an agent stuck on a stalled server waits that long before its retry.
