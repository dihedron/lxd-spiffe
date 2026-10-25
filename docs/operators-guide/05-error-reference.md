# Error reference

Every error the plugins can produce, where it appears, what causes it, and what to do. Messages are quoted as the plugins write them; `<...>` stands for a value filled in at run time, such as a key, a size or an LXD error. Many messages wrap another: `server lxd-prod, project spire-nodes: lxd: the server does not trust this client` is a resolution failure (first table) caused by an LXD client error (*The LXD client*). Read a message from the outside in.

## Reason codes

Every refused attestation logs `attestation refused` on SPIRE Server with one of these `reason_code`s; the agent sees only `PermissionDenied` with `attestation failed`. A successful one logs `attestation succeeded` with `ok`.

| Reason | Meaning | Usual causes | Remedy |
| --------------------- | ------------------------------------ | ------------------------------------ | ------------------------------------ |
| `ok` | The identity was issued | — | — |
| `payload_invalid` | The agent's messages broke the protocol | Incompatible plugin versions; something other than the agent plugin talking; a corrupted stream | Align versions; investigate the sender if it persists |
| `instance_not_found` | No instance matches the claim, or the instance vanished or changed identity during the proof | A hostname that is not the instance name; a wrong `claim.project`; a cloud-init ID that no longer matches (renamed instance); an instance deleted or restored mid-attestation; a claim for another instance | Fix the claim (*Setup Guide*, `claim.*`); otherwise someone claims what is not theirs |
| `instance_ambiguous` | Several instances match the claim, or several have its `volatile.uuid` | The same name in two allowed projects, without `claim.project`; an instance imported from a backup next to the original | Set `claim.project`; remove or re-identify the twin (*Incident procedures*) |
| `not_running` | The instance is not running, at the first read or after the proof | Stopping, stopped, frozen, moving, evacuating | None: the agent retries once the instance runs |
| `scope_denied` | The claimed or actual project is not allowed | The project is not in any server's `projects`; a claim with no project for a project not listed | Add the project, or move the instance |
| `policy_denied` | A policy check refused the instance, or an internal error | A privileged container; a denied config key; a disk at the proof directory; `security.devlxd=false` with `config_push`; a proof mode the agent does not offer; a selector or path problem | The `error` names the check; fix the instance or the policy |
| `proof_timeout` | The proof did not arrive within `proof_timeout` | An agent stuck or killed; a VM's files API answering 404 too long; a slow LXD | Check the agent; raise `proof_timeout` for VMs |
| `proof_mismatch` | The proof was wrong | Different `proof_dir` on the two sides; a proof directory with the wrong owner or mode; an agent not running as root; a forgery attempt | Fix the setup; if the setup is right, investigate (*Incident procedures*) |
| `vm_agent_unavailable` | A VM's `lxd-agent` did not answer within `proof_timeout` | A cold boot with SPIRE Agent started before `lxd-agent`; SELinux on RHEL 9 derivatives; `lxd-agent` disabled | Order SPIRE Agent after `lxd-agent`; the SELinux drop-in; raise `proof_timeout` |
| `lxd_unavailable` | LXD failed or refused a call | The server unreachable; the circuit breaker open; the certificate pin not matching; SPIRE's identity removed, expired or lacking the instance grant (a 403) | Check the server, the pin, the identity and the grant for the instance (*Incident procedures*) |
| `extension_missing` | The LXD server lacks a required API extension | LXD older than 5.21; a bearer token entry on 5.21; another service behind the URL | Upgrade LXD, or fix the entry |
| `rate_limited` | Refused by the plugin's limits, before any LXD call | A claim cooling down after failures; more attestations than `limits.max_inflight` or `limits.rate_per_sec` | Fix the failing claim; raise the limits for mass restarts |
| `precondition_failed` | The `config_push` challenge write kept meeting other writers | Another tool updating the instance's configuration continuously | Find the other writer; the agent retries |
| `instance_moved` | The instance changed cluster member during the attestation | A migration or evacuation | None: the agent retries |
| `project_unreadable` | The instance's project could not be read for the `network` selector | SPIRE's group lacks project `can_view` | Grant project `can_view`, or disable the `network` selector |

## The server plugin's refusals

The `error` of `attestation refused`, by the step that refused. The reason code is the one logged with it.

### Before the lookup

| Message | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `receiving the payload: <error>` | `payload_invalid` | The stream ended before the agent's first message | Look at the agent's log |
| `invalid message: <detail>` (see *The protocol messages*) | `payload_invalid` | The agent's request broke the protocol | Align versions |
| `the claim is cooling down after a failure` | `rate_limited` | The same project and name failed recently; the cooldown doubles with each failure, up to 5 minutes | Fix the claim's first failure, then wait |
| (no error; `inflight` logged) | `rate_limited` | `limits.max_inflight` or `limits.rate_per_sec` reached | Raise them if legitimate |
| `the agent offers <modes>, the server requires <mode>` | `policy_denied` | The agent does not offer the configured `proof_mode`; an agent offers `config_push` only when devLXD answers | Enable devLXD in the instance, or use `file_pull` or `auto` |

### Finding the instance

| Message | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `the claimed project is in no server's allow-list` | `scope_denied` | `claim.project` names a project no server entry allows | Fix the claim or add the project |
| `server <id>, project <project>: <error>` | `lxd_unavailable` | Reading the instance failed for another reason than "not found" | The wrapped error (*The LXD client*) |
| `server <id>: <error>` | `lxd_unavailable` or `extension_missing` | The server's check failed: untrusted identity, missing extensions, unreachable | The wrapped error; `refusing to attest through an LXD server` gives it once |
| `the circuit breaker is open after LXD failures` | `lxd_unavailable` | The server failed `limits.breaker_threshold` times in a row; no call is made until the next trial | Fix the server; the breaker closes after a successful trial |
| `no instance matches the claim` | `instance_not_found` | No instance of that name in the allowed projects, or its cloud-init ID differs from the claim | Check the name and project; a renamed instance needs SPIRE Agent restarted to send its new cloud-init ID |
| `<n> instances match the claim` | `instance_ambiguous` | The name exists in several allowed projects or servers | Set `claim.project`; give servers distinct projects |

### Policy checks

These come as `attestation denied (<code>): <detail>`; `after a 412: ` precedes the detail when the check ran again after a `config_push` write conflict.

| Detail | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `status <status>` | `not_running` | The instance is not `Running` | The agent retries when it runs |
| `project <project> is not allowed` | `scope_denied` | The instance's project is not in the server entry's `projects` | Add it, or move the instance |
| `unknown type hint <type>` | `policy_denied` | The agent reported an instance type the server does not know | Align versions |
| `type <type>, the agent says <type>` | `policy_denied` | devLXD's instance type differs from LXD's record | Investigate: a claim for another instance |
| `volatile.uuid missing or not a UUID` | `policy_denied` | The instance record lacks a valid `volatile.uuid` | Report it with the instance's `lxc config show` |
| `privileged container` | `policy_denied` | `security.privileged=true` and `policy.allow_privileged = false` | Make the container unprivileged, or allow it deliberately |
| `denied config key <key> is set` | `policy_denied` | A key of `policy.deny_config_keys` is set, possibly through a profile | Remove the key from the instance or its profiles |
| `devices overlap the proof directory: <devices>` | `policy_denied` | A disk device at, above or below `proof_dir` (`file_pull`) | Remove or move the disk: another instance could forge the proof through it |
| `security.devlxd is off` | `policy_denied` | `config_push` needs devLXD in the instance | Set `security.devlxd=true`, or use `file_pull` |
| `unknown proof mode <mode>` | `policy_denied` | An internal inconsistency | Report it |
| `raw.idmap is set and owners are host-side` | `policy_denied` | Only for an LXD version that reports host-side file owners (none tested does) | Remove `raw.idmap` |
| `uid_map line <line>: <error>` | `policy_denied` | A malformed `volatile.idmap.current` (host-side owners only) | Report it with the instance's configuration |

### Uniqueness

| Message | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `server <id>, project <project>: listing by uuid: <error>` | `lxd_unavailable` | The filtered instance list failed on a server; every server must answer | The wrapped error |
| `instances with the resolved uuid: [<server>/<project>/<name> ...]` | `instance_ambiguous` | Several instances share the UUID (an import keeps it), or SPIRE's group lacks project `can_view_instances` so the list misses the instance itself | Remove the twin; grant `can_view_instances` |

### The proof

| Message | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `reading a nonce: <error>` | `policy_denied` | The system's random source failed | Check SPIRE Server's host |
| `invalid proof input: agent nonce of <n> bytes` | `payload_invalid` | A nonce of the wrong length | Align versions |
| `sending the challenge: <error>` | `proof_timeout` | The stream to the agent broke | The agent's log |
| `no answer to the challenge within proof_timeout` | `proof_timeout` | The agent did not answer in time | The agent's log; raise `proof_timeout` |
| `receiving the answer: <error>` | `proof_timeout` | The stream broke while waiting | The agent's log, often an agent-side error |
| `the agent did not complete the file_pull step` | `proof_mismatch` | The agent answered with another mode or without completing | Align versions |
| `no proof file: <error>` | `proof_mismatch` | The files API found no file at the proof path in a container | Same `proof_dir` on both sides; the agent wrote it (its log) |
| `the proof file was not readable within proof_timeout: <error>` | `vm_agent_unavailable` or `proof_timeout` | A VM's `lxd-agent` not answering (500), or the file missing (404), until the deadline | Order SPIRE Agent after `lxd-agent`; raise `proof_timeout` |
| `reading the proof file: <error>` | `lxd_unavailable` | The files API failed otherwise, typically 403: no `can_access_files` on the instance | Grant `can_access_files` |
| `the proof is a <type>, not a regular file` | `proof_mismatch` | A directory or symlink at the proof path | Investigate: the agent never creates one |
| `the proof has owner <uid> and mode <mode>, not 0 and 0600` | `proof_mismatch` | The file is not root's, or not 0600 | SPIRE Agent must run as root; investigate otherwise |
| `the proof's content is not the expected proof` | `proof_mismatch` | Wrong content: another attestation's file, a forgery, a claim that differs from the instance | Investigate (*Incident procedures*) |
| `reading the proof directory: <error>` | `proof_mismatch` | The proof directory could not be read | Check it in the instance |
| `the proof directory is a <type> with owner <uid> and mode <mode>` | `proof_mismatch` | The proof directory is not a root-owned 0700 directory | Remove it; the agent recreates it correctly |
| `the agent's echo is not the challenge value` | `proof_mismatch` | `config_push`: the value read back differs | Investigate |
| `writing the challenge key: <error>` | `lxd_unavailable` | `config_push`: the `PATCH` failed, typically 403: no `can_edit` | Grant `can_edit` |
| `retries cut by proof_timeout: <error>` | `precondition_failed` | Write conflicts until the deadline | Find the other writer |
| `the instance changed during 10 retries` | `precondition_failed` | Ten write conflicts in a row | Find the other writer |
| `reading the instance after a 412: <error>` | `lxd_unavailable` | The re-read after a conflict failed | The wrapped error |

### After the proof

| Message | Reason | Cause | Remedy |
| ---------------------------------- | --------------------- | -------------------------------- | -------------------------------- |
| `the instance is gone after the proof` | `instance_not_found` | Deleted during the attestation | None |
| `reading the instance again: <error>` | `lxd_unavailable` | The second read failed | The wrapped error |
| `the instance is <status> after the proof` | `not_running` | Stopped during the attestation | The agent retries |
| `the instance's identity changed during the proof` (or `after a 412`) | `instance_not_found` | Name, project, UUID or generation changed: renamed, restored, replaced | Investigate if unexpected |
| `the instance moved from <member> to <member> during the proof` | `instance_moved` | Migrated or evacuated | The agent retries |
| `reading project <project>: <error>` | `project_unreadable` | The project read for the `network` selector failed | Grant project `can_view` |
| `selectors: the network selector needs the project that holds the instance's networks` | `policy_denied` | An internal inconsistency | Report it |
| `agent_path_template: <error>`, `agent_path_template renders an invalid SPIFFE ID path segment: <error>` | `policy_denied` | The template failed for this instance | Fix `agent_path_template` |

## The server plugin's log messages

| Message | Level | Meaning | Remedy |
| ------------------------------------- | ------- | ---------------------------------------------- | ---------------------------------- |
| `attestation succeeded` | info | An identity was issued; the line lists the selectors | — |
| `attestation refused` | error | An attestation was refused; see `reason_code` and `error` | This chapter |
| `refusing to attest through an LXD server` | error | A server failed its check (logged once until the error changes); `error` says why | *Incident procedures* |
| `the LXD identity has more rights than the attestor needs (SEC-16)` | warn | `finding` is `the identity is not fine-grained: it has every right on the server` or `the identity has the server's admin entitlement` | Use a fine-grained identity with the grants of *Setup Guide*, *Preparing LXD* |
| `selector omitted` | warn | A selector value broke the value rule; `why` is `not valid UTF-8`, `control character` or `<n> bytes, over 256` | Fix the value at its source, or stop relying on that selector |
| `config_push cleanup failed` | warn | The challenge key could not be removed; field `config_push_cleanup_failures_total` | None needed: the next attestation removes it |
| `proof_mode "none": issuing an identity on a claim only, with no proof of co-location (SEC-02)` | warn | Every claim-only attestation | Never in production |
| `proof_mode is "none": identities are issued on a claim alone, without proof of co-location (SEC-02)` | warn | At startup with `proof_mode = "none"` | Never in production |
| `dev_mode is on: LXD servers without a pin or CA bundle are not verified (SEC-07)` | warn | At startup with `dev_mode = true` | Never in production |
| `lxd: dev_mode: the LXD server certificate is not verified` | warn | On every connection to an unverified server | Set a pin or CA bundle |

Towards SPIRE, the server plugin answers every refusal with the gRPC status `PermissionDenied` and the text `attestation failed`, and an attestation that arrives before it was configured with `FailedPrecondition` and `not configured`.

## The server plugin's configuration errors

SPIRE Server refuses to start, or to reload the plugin, with `invalid plugin configuration: <message>`. The configuration as a whole is refused; fix the key named and start again.

| Message | Cause |
| ---------------------------------------------- | ------------------------------------------------------------ |
| `unknown keys <keys> (known: <keys>)` | A misspelt or unsupported key |
| `<key>: <value> is less than the minimum <min>`, `<key>: <value> exceeds the maximum <max>`, `<key>: time: invalid duration ...` | A duration out of range or malformed |
| `SPIRE passed no trust domain` | SPIRE Server's configuration lacks `trust_domain` |
| `proof_mode must be file_pull, config_push, auto or none` | An unknown proof mode |
| `proof_mode "none" issues identities on a claim alone: it needs i_understand_claim_only_is_insecure = true (SEC-02)` | `none` without the acknowledgement |
| `proof_dir must be an absolute, clean path`, `proof_dir must not be /`, `proof_dir is too long: proof paths must fit 128 bytes` | An invalid `proof_dir` |
| `reattest must be allow or deny` | An unknown value |
| `agent_path_template: <error>` | The template does not parse |
| `server <id>: agent_path_template renders an invalid SPIFFE ID path segment: <error>` | The rendered path has an invalid segment for this server |
| `policy.deny_config_keys: an empty key` | An empty string in the list |
| `selectors: selectors.enable: unknown selector <name>`, `selectors.enable: <name> listed twice` | An unknown or repeated selector name |
| `selectors.config_keys: <key> is a user key: list it in user_keys without its prefix` | A `user.*` key in `config_keys` |
| `selectors.user_keys: <key>: give the key without its user. prefix` | A key with the `user.` prefix in `user_keys` |
| `<list>: key of <n> bytes`, `<list>: key <key> with character <c>`, `<list>: <key> listed twice` | An empty, too long, repeated key, or one with a space, a `:` or a non-ASCII character |
| `limits.<key> must be between 1 and <max>` | A limit out of range |
| `servers: at least one LXD server is needed (FR-S11)` | No `servers` entry |
| `server id <id>: must be 1 to 63 characters (it is the server_id of the SPIFFE ID)`, or `must not be "." or ".."`, or `may hold letters, digits, '.', '-' and '_' only` | An invalid `servers` key |
| `server <id>: url: must be https://host[:port]`, or `must have no user, path, query or fragment` | An invalid `url` |
| `server <id>: a client certificate or bearer_token_file, not both (FR-S19)` | Both credentials given |
| `server <id>: client_cert_file and client_key_file go together` | One without the other |
| `server <id>: a credential is required: client_cert_file and client_key_file, or bearer_token_file` | No credential |
| `server <id>: client_cert_file: <error>`, `client_key_file: <error>` | A missing or unreadable file |
| `server <id>: client_key_file is readable by group or others (mode <mode>, SEC-08)` | Fix with `chmod 0400` |
| `server <id>: secret file: <error>`, `secret file <path> is readable by group or others (mode <mode>)` | The token file is missing, unreadable or too open |
| `server <id>: the bearer token is not a JWT: <n> parts`, `the bearer token is not a JWT: header: <error>`, `the bearer token is not a JWT: claims: <error>` | The token file does not hold an LXD token |
| `server <id>: the bearer token is unsigned (alg none): LXD never issues such a token`, `the bearer token has no signature: LXD never issues such a token` | A forged or truncated token |
| `server <id>: server_cert_fingerprint or ca_bundle_file, not both (SEC-07)` | Both verification methods given |
| `server <id>: server_cert_fingerprint or ca_bundle_file is required (SEC-07)` | Neither, without `dev_mode` |
| `server <id>: projects: at least one project`, `projects: <error>`, `projects: <project> is listed twice` | An empty, invalid or repeated project |
| `server <id>: lxd: pin must be a SHA-256 fingerprint (64 hex digits)` | A malformed fingerprint |
| `server <id>: lxd: CA bundle: <error>`, `lxd: the CA bundle holds no PEM certificate` | An unreadable or empty CA bundle |
| `server <id>: lxd: a pin or a CA bundle, not both`, `lxd: a pin or a CA bundle is required for https` | The client's own check of the same rules |
| `server <id>: loading client certificate: <error>` | The certificate and key do not parse or do not match |
| `server <id>: lxd: client certificate and key go together`, `lxd: project is required` | Internal checks of the client |
| `server <id>: lxd: endpoint: <error>`, `lxd: endpoint <url>: scheme must be https or unix`, `lxd: endpoint <url>: a TLS handshake needs https` | An unusable URL |
| `server <id>: lxd: the SDK connection cannot carry a context` | An internal error of the LXD SDK integration: report it |

## The LXD client

The errors of the client the server plugin uses for LXD. They appear wrapped in the refusals above (usually as `lxd_unavailable`), and in `refusing to attest through an LXD server`.

| Message | Cause | Remedy |
| ---------------------------------------------- | ---------------------------------------- | ---------------------------------- |
| `lxd: the server does not trust this client` | LXD does not know SPIRE's certificate or token: the identity was deleted, or the token expired or was replaced | Recreate the identity or renew the token |
| `lxd: the server lacks API extensions <list>` | The server is older than 5.21, or lacks `auth_bearer` for a token entry | Upgrade LXD, or use a client certificate |
| `server certificate fingerprint <fingerprint> does not match the pin` | The server presented another certificate: rotated, another server behind the address, or an attacker in between | Verify the new certificate on the server itself, then update `server_cert_fingerprint` |
| `server presented no certificate` | Not an LXD server, or a TLS-terminating proxy | Point `url` at LXD |
| `client certificate: <error>` | The client certificate could not be reloaded after its files changed | Fix the files; write them atomically |
| `redirect to <location> refused` | The URL answers with a redirect | Use LXD's own address |
| `response body over <n> bytes` | An answer larger than the client accepts (1 MiB, files 4 KiB) | Not an LXD answer, or a proof file that is not one: investigate |
| `decoding envelope: <error>`, `unknown envelope type <type>`, `decoding metadata: <error>`, `error response without an error envelope` | The answer is not LXD's format | Check what answers at `url` |
| `instance read: name, project or type not as requested` | LXD answered for another instance | Report it |
| `instance list: an instance without a name` | A malformed instance list | Report it |
| `project read: name not as requested` | LXD answered for another project | Report it |
| `lxd: not a canonical UUID` | An invalid UUID in the uniqueness check | Report it |
| `lxd: encoding the config` | The challenge write could not be encoded | Report it |
| `operation <id>` | LXD answered the `PATCH` with a background operation, which the plugin does not expect | Report it with the LXD version |
| `operation <id>: <status>: <error>` | A background operation failed | LXD's error |
| `files API: header <name> is not a decimal id`, `files API: header X-LXD-mode is not 4 octal digits`, `files API: header X-LXD-type is not file, directory or symlink` | The files API's metadata is malformed | Report it with the LXD version |
| `lxd: building request: <error>` | An internal error | Report it |

Errors with an HTTP status carry LXD's own text, and connection failures and timeouts the network's, such as `connection refused`: a 403 means a missing grant, a 404 a missing instance or file.

## The agent plugin

The agent plugin logs `attestation failed` with the error, and returns it to SPIRE Agent, prefixed `lxd_iid:`, with the gRPC status `DeadlineExceeded` for a timeout and `Internal` otherwise; before its configuration, `FailedPrecondition` with `not configured`. SPIRE Agent retries with a backoff. Its log line `attesting` records each attempt's claim.

### Configuration

SPIRE Agent refuses to start with these.

| Message | Cause |
| ---------------------------------------------- | ------------------------------------------------------------ |
| `the agent plugin must run as uid 0 (file_pull writes a root-owned proof), not <uid>` | SPIRE Agent runs as another user: run it as root |
| `invalid plugin configuration: unknown keys <keys> (known: <keys>)` | A misspelt or unsupported key |
| `invalid plugin configuration: claim.project: invalid claim: <detail>`, `claim.instance_name: invalid claim: <detail>` | An invalid name (see *Claims* below) |
| `invalid plugin configuration: use_devlxd must be auto, required or off` | An unknown value |
| `invalid plugin configuration: proof_dir must be an absolute, clean path` (and the other `proof_dir` errors above) | An invalid `proof_dir` |
| `invalid plugin configuration: proof_timeout (<d>) must be shorter than attestation_timeout (<d>)` | Raise `attestation_timeout` or lower `proof_timeout` |
| `invalid plugin configuration: <key>: <value> is less than the minimum <min>`, `... exceeds the maximum <max>` | A duration out of range |

### During an attestation

| Message | Cause | Remedy |
| ---------------------------------------------- | ---------------------------------------- | ---------------------------------- |
| `devLXD is required: <error>`, `devLXD is required but not available` | `use_devlxd = "required"` and `/dev/lxd/sock` does not answer | Enable `security.devlxd`, or set `use_devlxd = "auto"` |
| `reading devLXD: <error>`, `reading devLXD meta-data: <error>` | devLXD answered with an error | The instance's `lxd-agent` (VMs); devLXD settings |
| `devLXD reports instance type <type>` | An instance type the agent does not know | Align versions |
| `reading the hostname: <error>` | No name from configuration or devLXD, and no hostname | Set `claim.instance_name` |
| `sending the claim: <error>`, `waiting for the challenge: <error>`, `sending the challenge response: <error>`, `waiting for the server to read the proof: <error>` | The stream to SPIRE Server broke, or the server refused (`PermissionDenied`: see the server's log) | The server's `attestation refused` line |
| `attestation_timeout of <d>: context deadline exceeded` | The whole attestation took longer than `attestation_timeout` | SPIRE Server's queue (many agents behind one address); raise `attestation_timeout` |
| `proof_timeout of <d>: context deadline exceeded` | The server did not finish reading the proof within the agent's `proof_timeout` | Keep the agent's `proof_timeout` at least the server's |
| `the challenge: invalid message: <detail>` | The server's challenge broke the protocol | Align versions |
| `the server asks for <mode>, which this agent does not offer` | The server required `config_push` but devLXD did not answer | Enable devLXD, or use `file_pull` |
| `unknown proof mode <mode>` | A mode the agent does not know | Align versions |
| `the server sent a second challenge` | A protocol violation | Align versions; investigate the server |
| `the challenge's proof path <path> is not <path>: check that proof_dir is the same on the server` | The server's `proof_dir` differs from the agent's | Use the same `proof_dir` on both |
| `writing the proof: <error>` | The proof could not be written; see below | Fix the proof directory |
| `proof directory <dir> has mode <mode>, want drwx------`, `proof directory <dir> is owned by uid <uid>, want 0`, `proof directory <dir>: no owner information` | The proof directory exists with the wrong mode or owner, or is a symlink | Remove it; the agent recreates it |
| `proof directory <dir> changed while it was opened` | The directory was replaced during the attestation | Investigate: something in the instance races the agent |
| `proof file name <name> is not a plain name` | An internal check of the derived file name | Report it |
| `the challenge's key <key> is not <key>, the one of this agent's nonce` | `config_push`: the key is not derived from this agent's nonce | Investigate the server |
| `reading the challenge key through devLXD: <error>` | devLXD did not return the key | devLXD; the server's write |
| `the challenge key holds <n> bytes, not 64 lowercase hex characters` | The key's value is not a challenge | Investigate |
| `invalid proof input: <detail>` (`agent nonce of`, `server nonce of`, `project of`, `instance name of <n> bytes`, `proof directory`) | Inputs of the proof out of bounds | Align versions |
| `reading a nonce: <error>` | The random source failed | Check the instance |

### Claims

The claim's fields are checked against LXD's naming rules, on the agent and again on the server: `invalid claim: <detail>`.

| Detail | Cause |
| ---------------------------------------------- | ------------------------------------------------------------ |
| `instance name of <n> bytes`, `instance name starting or ending with a hyphen`, `instance name with character <c>`, `instance name that is a number` | Not a valid LXD instance name (1 to 63 letters, digits and hyphens) |
| `project name of <n> bytes`, `reserved project name`, `project name with character <c>` | Not a valid LXD project name |
| `cloud-init instance id of <n> bytes`, `cloud-init instance id is not a UUID`, `cloud-init instance id is not a lowercase UUID` | devLXD's instance ID is malformed |
| `meta-data with a NUL byte`, `meta-data line without a separator`, `meta-data key <key> repeated` | devLXD's meta-data is malformed |

## The protocol messages

`invalid message: <detail>` on either side means a message broke the plugins' protocol; with matching versions, this does not happen. The details: `message of <n> bytes`, `unknown fields`, `version <n>`, `no claim`, `unknown fields in the claim`, `agent nonce of <n> bytes`, `instance type <n>`, `plugin version of <n> bytes`, `<n> proof modes`, `claim source <n>`, `proof modes <modes>`, `no timeout`, `server nonce of <n> bytes`, `proof path of <n> bytes, not absolute or not clean`, `file_pull challenge with a proof key`, `config_push challenge with a server nonce`, `config_push challenge with a proof path`, `proof key is not a challenge key`, `proof mode <n>`, `file_pull response with an echo`, `config_push echo of <n> bytes or not lowercase hex`, `config_push echo without done`.

## Configuration tooling

`parsing SPIRE configuration: <error>`, `no plugin_data for <type> <name>` and `printing plugin_data: <error>` come from the helper that extracts a plugin's `plugin_data` from a whole SPIRE configuration file, used by the project's tests and lab: a configuration that does not parse, or lacks the `NodeAttestor "lxd_iid"` block.
