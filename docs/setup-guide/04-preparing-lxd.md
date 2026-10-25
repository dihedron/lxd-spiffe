# Preparing LXD

The server plugin reaches into instances through the LXD API with its own LXD identity. This chapter creates that identity on each LXD server or cluster, with the narrowest grants the chosen proof mode allows, and the projects that hold SPIRE-managed instances. Run the commands as an LXD administrator, on the server or through a remote.

## Projects

Keep SPIRE-managed instances in **dedicated projects**. The SPIRE identity gets grants in those projects only, and the server plugin's allow-list (`projects` in its configuration) names them: an instance in any other project is refused (`scope_denied`).

```bash
lxc project create spire-nodes
```

With `config_push`, make the project **restricted**. `config_push` needs `can_edit` on the instances, which lets whoever holds SPIRE's credential change any instance setting; in a restricted project LXD refuses privileged containers, raw LXC and QEMU settings, host paths in disk devices and other settings that reach the host, so `can_edit` stays within the instance:

```bash
lxc project set spire-nodes restricted=true
```

`restricted=true` blocks those settings by default; review the `restricted.*` keys if the project needs exceptions, and keep `restricted.containers.privilege` and `restricted.containers.lowlevel` at their defaults.

A project's `features.networks` decides where the networks its instances use live, and so the value of the `network` selector (`network:<project>/<network>`): the project itself when `features.networks=true`, `default` otherwise.

## The SPIRE identity

SPIRE authenticates to LXD with a dedicated identity that is used for nothing else and never holds administrator rights. The server plugin warns at its first connection when its identity looks like an administrator (`the LXD identity has more rights than the attestor needs (SEC-16)`). Two kinds of credential work:

- **A client certificate**, on any supported LXD version. It is long-lived: rotate it regularly (*Operator's Guide*, *Routine procedures*).
- **A bearer token**, on LXD 6.x only. Tokens can be short-lived, but whatever issues the next one holds identity-management rights: the long-lived privilege moves to that job rather than disappearing.

### With a client certificate

Create a key pair and a self-signed client certificate on the SPIRE Server host, readable only by the user SPIRE Server runs as:

```bash
install -d -m 0750 -o spire -g spire /etc/spire/lxd
umask 077
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 730 \
  -subj "/CN=spire-lxd-prod" \
  -addext "keyUsage=critical,digitalSignature" -addext "extendedKeyUsage=clientAuth" \
  -keyout /etc/spire/lxd/client.key -out /etc/spire/lxd/client.crt
chown spire:spire /etc/spire/lxd/client.key /etc/spire/lxd/client.crt
chmod 0400 /etc/spire/lxd/client.key
```

The plugin refuses a key file that its group or others can read. Then create the authorization group and add the certificate to LXD as a fine-grained identity in that group (copy `client.crt` to the LXD server first):

```bash
lxc auth group create spire
lxc query -X POST /1.0/auth/identities/tls -d "$(jq -n --arg name spire \
  --rawfile cert client.crt '{name: $name, certificate: $cert, groups: ["spire"]}')"
lxc auth identity list
```

The identity must be fine-grained (listed with authentication method `tls` and its group). Do not add the certificate with `lxc config trust add` without restrictions: an unrestricted trusted certificate has every right on the server, and the plugin warns about it.

### With a bearer token (LXD 6.x)

```bash
lxc auth group create spire
lxc auth identity create bearer/spire --group spire
lxc auth identity token issue bearer/spire --expiry 30d
```

Write the token to a file on the SPIRE Server host, readable only by SPIRE Server's user (mode `0600` or `0400`), and name it in `bearer_token_file`. Without `--expiry`, LXD issues a token valid for 10 years: always set an expiry. Issuing a new token revokes the previous one at once, so the job that renews it must write the file atomically (write a new file, then rename it over the old one) right after issuing it. The plugin rereads the file when it changes.

## Grants

LXD grants entitlements to groups, never to identities directly. The SPIRE group needs:

| Proof mode | Grants |
| ------------ | -------------------------------------------------------- |
| every mode | project `can_view`, then project `can_view_instances`, on each project of the allow-list |
| `file_pull` | plus instance `can_access_files` on each SPIRE-managed instance |
| `config_push` | plus instance `can_edit` on each SPIRE-managed instance (in a restricted project) |

```bash
# every mode, for each allowed project; can_view first: LXD refuses any
# other grant in a project the group cannot view
lxc auth group permission add spire project spire-nodes can_view
lxc auth group permission add spire project spire-nodes can_view_instances

# file_pull, for each SPIRE-managed instance
lxc auth group permission add spire instance web-01 can_access_files project=spire-nodes

# config_push, for each SPIRE-managed instance
lxc auth group permission add spire instance web-01 can_edit project=spire-nodes
```

Why each grant:

- **Project `can_view`** lets LXD accept the group's other grants in the project at all, and lets the plugin read the project's `features.networks` for the `network` selector.
- **Project `can_view_instances`** lets the plugin read every instance of the project, including those it has no instance grant on. The plugin needs that to check that no other instance shares the attested instance's `volatile.uuid`: an instance imported from a backup keeps the original's UUID, and without this grant the plugin would not see such a twin.
- **Instance `can_access_files`** lets the plugin read the proof file. It also lets the holder read and write any file in the instance: grant it on SPIRE-managed instances only.
- **Instance `can_edit`** lets the plugin write and remove the challenge key. It also lets the holder change any setting of the instance: hence the restricted project. `can_edit` does not include reading files.

Grant the instance entitlement when an instance is created (from the tooling that creates it) and remove it when the instance is deleted. An instance without the grant cannot attest: its attestation fails with `lxd_unavailable`, the LXD API having refused the proof read or the challenge write.

### More than 90 instances

An LXD group holds at most 99 permissions; LXD refuses the 100th. The limit is per group, and an identity in several groups has every grant of all of them. So spread the instance grants over several groups of at most 90 instance grants each, every group also holding project `can_view` (granted first), and make the SPIRE identity a member of all of them:

```bash
lxc auth group create spire-2
lxc auth group permission add spire-2 project spire-nodes can_view
lxc auth identity group add tls/spire spire-2        # or bearer/spire
lxc auth group permission add spire-2 instance web-91 can_access_files project=spire-nodes
```

Add a group whenever the existing ones are full.

## The server certificate fingerprint

The plugin verifies the LXD server's TLS certificate by its SHA-256 fingerprint (or against a CA bundle, if the server's certificate is issued by a CA). LXD's certificates are self-signed by default, so the fingerprint is the usual choice. Take it from the server itself, not over the network you want to protect:

```bash
lxc info | grep certificate_fingerprint
```

On a cluster, every member presents the cluster certificate, so one fingerprint covers all members. The plugin accepts the fingerprint with or without a `sha256:` prefix and colons, in either case.

## Instances

Nothing is installed on the LXD hosts. The instances need:

- **SPIRE Agent and the agent plugin**, running as root (*Installing the agent plugin*).
- **`security.devlxd=true`** (the default) for `config_push`, and for the agent to learn its instance name and type from devLXD. With `file_pull` and the instance name configured in the agent, devLXD can be off (`security.devlxd=false`).
- **No shared storage at, above or below the proof directory** (`/run/spire/lxd`, so not at `/run` either): the server refuses an instance with a disk device there, since another instance sharing that disk could write the proof.
- **Not privileged**, unless the server's policy allows privileged containers (`policy.allow_privileged`): root in a privileged container is root on the host.
