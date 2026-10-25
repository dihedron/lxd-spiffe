# Routine procedures

## Adding and removing instances

An instance attests only if SPIRE's group holds its instance grant (`can_access_files` for `file_pull`, `can_edit` for `config_push`) and it is in an allowed project. Make the tooling that creates instances grant it, and the tooling that deletes them remove it:

```bash
lxc auth group permission add spire instance web-01 can_access_files project=spire-nodes
lxc auth group permission remove spire instance web-01 can_access_files project=spire-nodes
```

A group holds at most 99 permissions: once a group has 90 instance grants, create the next one, grant it project `can_view` first, and add the SPIRE identity to it (*Setup Guide*, *Preparing LXD*). Removing an instance's grant does not revoke its agent's SVID: evict the agent when you delete the instance (below).

## Adding an LXD server or cluster

1. Prepare it as in *Setup Guide*, *Preparing LXD*: projects, the SPIRE identity, the grants, the fingerprint.
2. Add a `servers` entry with a new, unique `server_id`. Never reuse a `server_id` that a decommissioned server used while agents of it may still exist.
3. Restart SPIRE Server (one at a time when there are several).

An entry is checked at its first attestation: watch for `refusing to attest through an LXD server` with that `server_id`.

## Rotating the SPIRE credential

- **Client certificate**: create a new key and certificate, add the new certificate as a second fine-grained identity in the same groups (or replace the identity's certificate), write the new files over the old ones (write and rename, so the plugin never reads half a file), and the plugin reloads them when they change, without a restart. Then delete the old identity. Keep the key mode `0400` or `0600`.
- **Bearer token** (LXD 6.x): issue a new token with an expiry (`lxc auth identity token issue bearer/spire --expiry 30d`) and write it atomically to `bearer_token_file`. Issuing it revokes the previous token at once, so write the file right away; the plugin rereads it when it changes, and once more when LXD refuses the old one. Automate it well before expiry: an expired token is refused everywhere at once.
- **LXD's server certificate** changed (a new cluster certificate): update `server_cert_fingerprint` and restart SPIRE Server. Until then, attestations through that server fail with `lxd_unavailable` and the error `server certificate fingerprint ... does not match the pin`.

## Upgrading

See *Setup Guide*, *Upgrades and removal*: the server plugin's `plugin_checksum` changes with every upgrade, and SPIRE Server must restart to load the new binary. Upgrade LXD servers only after an end-to-end check against an upgraded test server; a `snap refresh` of LXD restarts every instance of the server.

## Changing the configuration

Change one SPIRE Server at a time and watch its log as it starts: the plugin refuses a bad configuration as a whole, and SPIRE Server then stops with `invalid plugin configuration: ...` naming the key. Keep the previous configuration to roll back. Changes that alter identities:

- `agent_path_template` or a `server_id` changes every affected agent's SPIFFE ID: their next attestation gives them the new ID, and node entries that name agents by ID stop matching.
- `selectors.enable` changes which selectors agents get at their next attestation: entries that rely on a removed selector stop matching.

## Evicting agents

Evict an agent with `spire-server agent evict -spiffeID <id>`. Its SVID stops being renewable; with `reattest = "allow"`, the agent attests again if its instance still exists and passes, with `deny` only after the eviction. Evict:

- when you delete an instance, so that its identity cannot outlive it;
- when you **copy or restore** an instance that runs an agent (below);
- when you suspect an instance was compromised: after fixing it, its agent attests afresh.

## Copies and restores

A copy of an instance (`lxc copy`), or a restore of a backup, that includes SPIRE Agent's data directory (`/var/lib/spire/agent`) is **the original's node as far as SPIRE is concerned**: its agent loads the original's SVID and key, never attests, so the plugin is never asked, and workloads in the copy are issued the original's workload SVIDs. The lab confirmed this. The plugin cannot refuse what it never sees. So:

- **Before the copy first starts**, remove the agent's data in it: `lxc file delete --recursive copy/var/lib/spire/agent` (or from inside, before SPIRE Agent starts). The copy then attests with its own `volatile.uuid` and gets its own identity.
- **Or evict the original's agent** right after copying: both agents then lose the shared SVID at its next renewal, and each attests as itself.
- **Restoring a snapshot of the same instance** keeps its `volatile.uuid` and changes its `volatile.uuid.generation`: it is the same instance, and attests as such. Entries using the `generation` selector stop matching until re-created.

A short `agent_ttl` bounds the window: agents of this attestor attest again at about half their SVID's life, so a copy that kept the agent's data passes for the original for at most about that long after the original is evicted. Keeping the agent's data on a tmpfs, so that no copy has it, is a mitigation under evaluation; its cost is an attestation at every boot.

## Cluster maintenance

Before evacuating a cluster member:

- With `file_pull`, make sure no container on it attests during the evacuation: stop the SPIRE Agents of its containers, or move the containers first while idle. A files API read during a container's evacuation makes the evacuation fail and leave a volume behind (*Incident procedures*, *A failed evacuation*). VMs are not affected.
- With `config_push`, an evacuation that meets a challenge write fails with `Instance is busy running a "update" operation`; run `lxc cluster evacuate` again.
- On LXD 6.9, an evacuated member loses its database role: do not evacuate, restart or power off another member until the first is restored and `lxc cluster list` shows three database voters.
- After every evacuation, check that the instances' volumes left the member (`lxc storage volume list <pool>`, its `location` column) before restoring it.

## Orphaned challenge keys

A `config_push` attestation whose cleanup could not run (LXD unreachable, SPIRE Server killed) leaves `user.spire.challenge.<id>` on the instance. It is harmless: an old nonce, useless for any other attestation, visible only to those who can view the instance. The instance's next `config_push` attestation removes it. On instances that will not attest again:

```bash
lxc config show <instance> --project <project> | grep user.spire.challenge
lxc config unset <instance> <key> --project <project>
```
