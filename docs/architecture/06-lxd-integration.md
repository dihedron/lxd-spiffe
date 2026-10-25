# LXD integration

## The client

The server plugin talks to LXD only through its own client, a thin wrapper around the LXD Go client SDK that adds the project's rules:

- **TLS 1.3 only** (LXD refuses TLS 1.2 anyway), with mutual TLS or a bearer token.
- **Server verification** by the SHA-256 fingerprint of the certificate the server presents (LXD's certificates are self-signed by default; standard chain verification is replaced only in this pinned mode), or by chain verification against a configured CA bundle. Neither is skipped outside an explicit development mode, which warns on every connection.
- **Credentials from files** with permission checks: a key or token file readable by group or others is refused. The client certificate and the token are reloaded when their files change.
- **No redirects**: a 3xx answer is an error. **A deadline on every call.** **An explicit project on every call.**
- **Bounded responses**: JSON bodies at most 1 MiB, files at most 4 KiB.
- **Strict decoding** into the SDK's typed structures, with the fields the plugin consumes validated; decisions are never read from untyped data. The files API's headers (`X-LXD-uid`, `X-LXD-gid`, `X-LXD-mode`, `X-LXD-type`) are parsed strictly: a missing or malformed header is an error, and the mode keeps every bit.
- **Errors by status code**, not by message text, except LXD's 500 for a VM whose `lxd-agent` is not running. LXD's error texts go to the logs only.

Only the Apache-2.0 licensed `client` and `shared` packages of the LXD repository are used; a CI check enforces a dependency allow-list.

## The calls

| Purpose | Call | When |
| ------------------- | ------------------------------------------ | ------------------------------------------ |
| Capability check | `GET /1.0` | First use of a server and after every reconnection: API extensions, trust, version |
| Own identity | `GET /1.0/auth/identities/current` | After the capability check: warns if the identity looks like an administrator |
| Resolve the instance | `GET /1.0/instances/<name>?project=<p>` | Every attestation, per allowed project; again after the proof |
| Uniqueness | `GET /1.0/instances?project=<p>&filter=config.volatile.uuid eq <uuid>` | Every attestation, per allowed project of every server |
| Read the proof | `GET /1.0/instances/<name>/files?path=...` | `file_pull`: the proof file and its directory |
| Write and remove the challenge | `PATCH /1.0/instances/<name>` with `If-Match` | `config_push`: two writes per attestation, both synchronous |
| Network project | `GET /1.0/projects/<name>` | When the `network` selector is enabled and the instance has a NIC on a managed network |

**Required API extensions**: `projects`, `instance_generation_id` and `api_filtering`, plus `auth_bearer` for a server entry with a bearer token. LXD 5.21 LTS and later offer them. The list is minimal on purpose: an extension is required only if the plugin uses the feature. A server that lacks one, or that does not trust the plugin's identity, is refused (`extension_missing`, `lxd_unavailable`) and logged once, until it changes. The plugin contacts no server when SPIRE Server starts, so SPIRE Server starts while an LXD server is down.

**The fixtures are the contract.** Responses of every call, error cases included (403, 404, 412, a VM's 500), were recorded from real LXD 5.21.8 and 6.9 servers, standalone and through cluster members that hold neither instance, and from a standalone 6.10.0 server. The tests replay them against the client, and the fake LXD server of the integration tests implements no behaviour that no fixture shows.

## Least privilege

| Proof mode | Grants of the SPIRE group |
| ---------- | ------------------------------------------------------------------ |
| every mode | project `can_view`, then project `can_view_instances`, on each allowed project |
| `file_pull` | plus instance `can_access_files` on the SPIRE-managed instances |
| `config_push` | plus instance `can_edit` on the SPIRE-managed instances, in restricted projects |

- LXD refuses every grant in a project the group cannot view, so project `can_view` comes first; it alone also reads a project's `features.networks`.
- Project `can_view_instances` lets the uniqueness check see instances without a grant of their own, such as an imported twin.
- An LXD group holds at most 99 permissions. The limit is per group, and an identity's grants over several groups are all honoured, so the per-instance grants are sharded over groups of at most 90.
- At its first connection to each server, the plugin reads its own identity's effective permissions and warns if the identity is not fine-grained (a legacy trusted certificate has every right) or holds the server's `admin` entitlement.

## Clusters

A cluster is one server entry. Every member forwards every call the plugin makes (instance read, files API, conditional `PATCH`, filtered list) to the member holding the instance, without redirects, so the client has no cluster logic and no failover of its own: the entry's URL can point at a load balancer.

- Identity keys (`volatile.uuid`, its generation, the cloud-init ID) survive moves and evacuations, so `location` is the only field that shows a move; the post-proof read compares it (`instance_moved`).
- A cold move or an evacuation stops the instance, so the `Running` check fails; it is made on both reads because the files API answers for a stopped container.
- After a VM's evacuation, its files API answers 500 for 25 to 30 seconds, handled like a VM's start-up.
- The `volatile.cluster.group` key is not used: it records where an instance was asked to be placed, not where it runs.

## Limits and the circuit breaker

Attestations reach SPIRE Server before any authentication, so the plugin protects LXD from floods (`T11`):

- **Before any LXD call**: a cap on concurrent attestations (default 16), a token bucket (default 20 per second), and a per-claim cooldown after a failure (5 s, doubling up to 5 minutes, keyed on the claimed project and name). An attestation over a limit is refused at once (`rate_limited`), not queued.
- **Per LXD server, a circuit breaker**: after 5 transport failures in a row (connection errors, timeouts, server errors other than a VM's agent starting, invalid responses; any answer resets the count), attestations through that server fail at once without calling it, for 5 seconds; then one trial attestation goes through on a new connection, checked again; its failure doubles the open time, up to 60 seconds.
- **No caching of outcomes**: every attestation asks LXD.
