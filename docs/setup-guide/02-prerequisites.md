# Prerequisites

## Supported versions

| Component | Supported | Tested |
| ------------ | ------------------------------------- | -------------------------------------- |
| LXD | 5.21 LTS or later, installed as the snap | 5.21.8, 6.9 and 6.10.0, standalone and in three-member clusters, with ZFS storage |
| SPIRE | The release matching the plugin SDK the plugins are built with (1.15) | SPIRE 1.15.5 |
| Guests | Linux containers and virtual machines, amd64 and arm64 | Ubuntu 24.04, Debian 12, Alpine 3.24, Rocky Linux 9 and AlmaLinux 9 |

- **LXD 5.21 LTS is the minimum**: earlier versions lack API extensions the server plugin needs (`projects`, `instance_generation_id`, `api_filtering`), and it checks them before it attests through a server. LXD is supported as the snap only: no distribution package ships 5.21 or later (Debian ships 5.0.2).
- **Bearer tokens** as the SPIRE identity's credential need LXD 6.x (API extension `auth_bearer`); 5.21 has client certificates only.
- **Guests**: Windows guests are not supported. Virtual machines need `lxd-agent` running in the guest, which LXD's images start by default; RHEL 9 derivatives with SELinux enforcing need a drop-in (*Installing the agent plugin*).
- **Storage**: every measurement in these documents was made on ZFS. Other storage drivers are expected to behave the same for attestation, but the cluster evacuation findings of *Clusters* were not checked on them.

## Platforms and builds

Both plugins are static Go binaries with no dependency on any library or tool: the agent plugin needs neither `lxc`, `cloud-init` nor `curl` in the guest. They are built for Linux on x86-64 and arm64. For x86-64 there are three builds, for the x86-64 microarchitecture levels: `v1` runs on any x86-64 processor, `v2` and `v3` (archives with `x86_64v2` and `x86_64v3` in their names) use newer instructions and run only on processors that have them. The packages carry the `v1` build.

## Network paths

| From | To | Port | Why |
| ------------------ | ---------------------------- | ----------- | --------------------------------------- |
| Instances (SPIRE Agent) | SPIRE Server | `8081` by default | Node attestation and SVID renewal |
| SPIRE Server | Each LXD server or cluster (its API address) | `8443` by default | The server plugin's LXD API calls |

The instances never talk to the LXD API, and SPIRE Server never talks to the instances directly: the proof travels through LXD. Firewall the SPIRE Server port to the instance networks and the LXD API to SPIRE Server and the LXD administrators (*Operator's Guide*, *Operational guidance*).

SPIRE Server rate-limits node attestation per source address, by default one attestation a second. Containers behind one LXD bridge with NAT reach SPIRE Server from one address, so after an event that restarts many of them at once their attestations queue: see `attestation_timeout` in *Installing the agent plugin*.

## Decisions to make first

Before installing anything, decide:

- **The proof mode**: `file_pull` (the default and the recommendation for most deployments) or `config_push`. The mode decides which grant SPIRE's LXD identity needs: `can_access_files` on the instances for `file_pull`, `can_edit` for `config_push`. *Choosing the proof mode* explains the choice.
- **The projects** that hold SPIRE-managed instances. A dedicated LXD project is preferred: SPIRE's identity gets grants in it only, and with `config_push` it should be a restricted project.
- **The `server_id`** of each LXD server or cluster: a short name that becomes part of every SPIFFE ID issued for its instances (`lxd_iid/<server_id>/<uuid>`). It must be unique across all the LXD servers SPIRE attests instances of, and should never change: changing it changes every agent's SPIFFE ID.
- **The SPIRE identity's credential** on each LXD server: a client certificate (any LXD version), or on LXD 6.x a bearer token.
- **The agent SVID lifetime** (SPIRE Server's `agent_ttl`): one hour or less is recommended. A short lifetime bounds how long an instance copied together with the agent's data is taken for the original (*Operator's Guide*, *Routine procedures*).
