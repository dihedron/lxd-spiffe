# Installing the server plugin

The server plugin runs inside every SPIRE Server that attests LXD instances. SPIRE Server starts it as a separate process and talks to it over a local socket; the plugin talks to the LXD servers.

## Installing the package

```bash
apt install ./lxd-server-plugin_<version>_linux_amd64.deb     # Debian, Ubuntu
dnf install ./lxd-server-plugin_<version>_linux_amd64.rpm     # RHEL-like
```

The package installs the binary as `/usr/bin/lxd-server-plugin` and a commented sample configuration as `/usr/share/doc/lxd-server-plugin/server.conf`. It installs no service: SPIRE Server runs the plugin. Check what you installed:

```bash
lxd-server-plugin version --verbose
sha256sum /usr/bin/lxd-server-plugin        # for plugin_checksum
```

## Configuring SPIRE Server

Add a `NodeAttestor "lxd_iid"` block to the `plugins` section of SPIRE Server's configuration. The sample, with every key at its default unless its comment says otherwise:

```{.hcl include="examples/server.conf"}
```

Set at least:

- **`plugin_checksum`**: the SHA-256 of the installed binary. SPIRE refuses to load a binary whose hash does not match, so an upgrade of the package needs the new checksum in the same change (*Upgrades and removal*).
- **`servers`**: one entry per LXD server or cluster, keyed by its `server_id`, with its `url`, the SPIRE identity's credential (`client_cert_file` and `client_key_file`, or `bearer_token_file`), the server's certificate fingerprint (`server_cert_fingerprint`) or CA bundle (`ca_bundle_file`), and the allowed `projects`.
- **`proof_mode`**, if not `file_pull`. The agent must offer the mode: an agent offers `file_pull` always, and `config_push` only when it can reach devLXD.

The rest has safe defaults. The *Configuration reference* describes every key; the main choices:

- **`policy`**: privileged containers are refused unless `allow_privileged = true`, and instances with any key of `deny_config_keys` set are refused. Deny the low-level keys that weaken isolation (`raw.lxc`, `raw.idmap`, `raw.qemu`, `raw.apparmor`) unless the project's restrictions already forbid them.
- **`selectors`**: which selectors the agents get. The defaults (`server`, `uuid`, `project`, `type`, `privileged`, `profile`, `network`) suit most registration entries; enable more only for what your entries use.
- **`reattest`**: `allow` (the default) lets an attested agent attest again, which agents of this attestor do at about half their SVID's lifetime; `deny` makes an agent that lost its SVID wait for an operator to evict it.
- **`proof_timeout`**: raise it (up to `2m`) when virtual machines attest right after a cold boot and their SPIRE Agent cannot be ordered after `lxd-agent` (*Installing the agent plugin*).

SPIRE Server checks the configuration when it starts or reloads its plugins: an unknown key, a missing pin, a key file readable by others or an inconsistent setting stops it with an error that names the key (*Operator's Guide*, *Error reference*). The plugin does not contact any LXD server at that point: it checks each server at its first attestation, so SPIRE Server starts even while an LXD server is down.

### SPIRE Server's own settings

- **`agent_ttl`** (in the `server` block): the lifetime of agent SVIDs. Keep it at one hour or less. Agents of this attestor attest again at about half their SVID's life instead of renewing it, and a short lifetime bounds how long an instance copied together with the agent's data passes for the original (*Operator's Guide*, *Routine procedures*).
- **`ratelimit { attestation = ... }`**: SPIRE Server allows one attestation a second per source address by default, before any plugin runs. Containers behind one NAT share an address; when many of them restart together (an LXD daemon restart restarts every container of the server), their attestations queue at that rate. Either keep the limit and raise the agents' `attestation_timeout`, or turn it off (`attestation = false`) and rely on the plugin's own `limits`.

## Registration entries

The plugin issues each agent the SPIFFE ID `spiffe://<trust-domain>/spire/agent/lxd_iid/<server_id>/<volatile.uuid>`, and the selectors enabled in `selectors`. Registration entries then give workloads their identities, with the agents chosen by selectors. A node entry that groups the agents of a project, and a workload entry under it:

```bash
spire-server entry create -node \
  -spiffeID spiffe://example.org/lxd/spire-nodes \
  -selector lxd_iid:server:lxd-prod \
  -selector lxd_iid:project:spire-nodes \
  -selector lxd_iid:privileged:false

spire-server entry create \
  -parentID spiffe://example.org/lxd/spire-nodes \
  -spiffeID spiffe://example.org/web \
  -selector unix:uid:1000
```

Choose selectors by who can change them:

- **High trust** selectors come from values LXD maintains or the SPIRE operator configures: `server`, `uuid`, `type`, `arch`, `generation`, `base-image`, `member`. No instance user and no LXD user can set them.
- **Medium trust** selectors come from values LXD users with edit rights can set: `project`, `privileged`, `profile`, `network`, `image`, `secureboot`, `config:*`, `user:*`. They are as trustworthy as your LXD access control: whoever can edit a profile, a `user.*` key or a network attachment can make an instance match an entry that uses it. Restrict those rights for everything an entry relies on.
- **Low trust**: `name`. Instances can be renamed, and names are reused.

Always include `server` in node entries: it is the only selector that ties an agent to one LXD server or cluster, since names, projects and profiles repeat across servers. Never base an entry on a value the guest reports about itself: the plugin never offers such values as selectors.

*Architecture and Design*, *Identity and selectors*, explains each selector's source and trust.
