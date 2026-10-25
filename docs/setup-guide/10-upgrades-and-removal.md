# Upgrades and removal

## Upgrading the server plugin

SPIRE Server checks the plugin binary's hash against `plugin_checksum` when it loads it, so an upgrade changes the binary and the configuration together:

1. Install the new package (`apt install ./lxd-server-plugin_<version>_linux_amd64.deb` or `dnf install ...`) after verifying it (*Getting and verifying a release*).
2. Compute the new checksum, `sha256sum /usr/bin/lxd-server-plugin`, and set it as `plugin_checksum`.
3. Restart SPIRE Server. Until it restarts it keeps running the old plugin process.

With several SPIRE Servers, upgrade them one at a time. Agents keep their SVIDs during the restart; those whose attestation falls into it retry.

A configuration key a new release removes or renames makes SPIRE Server refuse the old configuration with `unknown keys`: read the release notes before upgrading.

## Upgrading the agent plugin

Update the package in the image, or in running instances, together with SPIRE Agent's `plugin_checksum`, and restart SPIRE Agent. An agent that already holds an SVID keeps it across the restart and attests again only when it needs to.

The plugins speak version 1 of their protocol on both sides, so agents and servers of different releases work together; a release that changes this says so in its notes.

## Upgrading LXD

The plugin checks a server's version and API extensions again after every reconnection, and refuses to attest through a server that lacks a required extension (`extension_missing`). Before upgrading production servers, run the *End-to-end check* against an upgraded test server. A `snap refresh` of LXD restarts the daemon; plan for what follows (*Operator's Guide*, *Operational guidance*).

## Removal

1. Remove the `NodeAttestor "lxd_iid"` block from SPIRE Server's configuration and restart SPIRE Server, then remove the package (`apt remove lxd-server-plugin` or `dnf remove lxd-server-plugin`).
2. In the instances, remove the agent's node attestor configuration and the package. Evict their agents from SPIRE Server (`spire-server agent evict`).
3. On each LXD server, remove the SPIRE identity and its groups:

   ```bash
   lxc auth identity delete tls/spire          # or bearer/spire
   lxc auth group delete spire
   ```

4. With `config_push`, look for challenge keys that an interrupted attestation left on instances, and remove them:

   ```bash
   lxc config show <instance> --project spire-nodes | grep user.spire.challenge
   lxc config unset <instance> <key> --project spire-nodes
   ```

5. Delete the SPIRE identity's credential files (`/etc/spire/lxd/`) on the SPIRE Server hosts.
