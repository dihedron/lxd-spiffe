# Clusters

An LXD cluster is **one** `servers` entry, with one `server_id`, one `url`, one credential and one fingerprint: every member answers for the whole cluster, forwarding each call the plugin makes (instance reads, the files API, the challenge write, the instance list) to the member that holds the instance, without redirects. The plugin has no cluster logic of its own.

```hcl
servers = {
  "lxd-cluster-a" = {
    url                     = "https://lxd-a.example.org:8443"   # a load balancer or virtual address
    client_cert_file        = "/etc/spire/lxd/cluster-a.crt"
    client_key_file         = "/etc/spire/lxd/cluster-a.key"
    server_cert_fingerprint = "sha256:<the cluster certificate's fingerprint>"
    projects                = ["spire-nodes"]
  }
}
```

- **`url`**: point it at a load balancer or virtual address in front of the members if attestation must survive a member being down. The plugin does not fail over between members by itself; with a single member's address, attestations through it fail while that member is down.
- **The identity and its grants** are cluster-wide: create them once, on any member.
- **The fingerprint** is the cluster certificate's, which every member presents.

## Selectors on a cluster

- **`member`** (off by default) states which member ran the instance when it attested, not where it runs now: it changes with every migration and evacuation. Use it for audit rather than for registration entries.
- There is **no cluster-group selector**: an instance's cluster group records where it was asked to be placed, not where it runs. For per-group policy, use separate projects with `restricted.cluster.groups`, and the `project` selector.

## Moves and evacuations

- A cold move or an evacuation stops the instance; an attestation in flight then fails (`not_running`) and the agent attests again once the instance runs on its new member. A move between the plugin's two reads of the instance fails with `instance_moved`.
- After a VM is evacuated, its files API answers errors for 25 to 30 seconds while `lxd-agent` starts on the new member; attestations meanwhile fail with `vm_agent_unavailable` and are retried.
- Live migration (`migration.stateful`) is untested.

## Maintenance

The lab found the following on LXD 5.21.8 and 6.9, and confirmed the first on 6.10.0; the *Operator's Guide* gives the recovery steps.

- **Do not evacuate a member whose containers may attest with `file_pull`.** A files API read of a container during its evacuation makes the evacuation fail with `cannot unmount ... pool or dataset is busy` (the holder is LXD's own `forkfile` helper), and leaves the container's volume on the source member, which blocks the member's restore. VMs under the same reads did not fail. Either stop the containers' agents first, or use `config_push` on members you evacuate.
- **With `config_push`, an evacuation that meets a challenge write fails** with `Instance is busy running a "update" operation`, leaves nothing behind, and can be repeated; an instance may be left stopped.
- **On LXD 6.9, an evacuated member loses its database role.** A three-member cluster with one member evacuated keeps two database voters, and losing one of them loses the cluster's quorum. Do not evacuate, restart or power off a second member until the first is restored and `lxc cluster list` shows three database voters again.
- **A power-off can empty a member's image cache.** A member powered off while LXD wrote its image cache came back with empty files under `/var/snap/lxd/common/lxd/cache` (5.21.8), and launches from remote images failed there until the files were removed and LXD restarted.
