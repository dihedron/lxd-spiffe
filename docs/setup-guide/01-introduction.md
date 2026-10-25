# Introduction

This guide installs and configures *lxd-spiffe*, which lets a SPIRE Agent running inside an LXD container or virtual machine obtain its node identity from SPIRE Server, with nothing secret baked into the instance's image. It is written for the people who run the LXD servers, the SPIRE Server and the instance images. The companion documents explain how the attestation works and why it is safe (*Architecture and Design*), and how to run it day to day (*Operator's Guide*).

## What the plugins do

SPIRE gives every workload a SPIFFE identity, but first it must trust the machine the workload runs on: its *node attestation*. For an LXD instance, the only party that knows for certain which instance it is, in which project, with which configuration, is the LXD server that runs it. LXD, unlike public clouds, offers no signed identity document that an instance could hand to SPIRE. *lxd-spiffe* therefore makes SPIRE Server ask LXD directly, and prove that the agent it talks to really runs inside the instance LXD describes:

1. Inside the instance, SPIRE Agent's **agent plugin** (`lxd-agent-plugin`) sends a *claim*: the instance's project and name, as the instance believes them, and a random nonce.
2. In SPIRE Server, the **server plugin** (`lxd-server-plugin`) looks the instance up through the LXD REST API, with its own LXD credential, and checks it against a policy (running, in an allowed project, not privileged, ...).
3. The server plugin sends a challenge that only code running as root inside that instance can answer. With the default proof mode, `file_pull`, the agent writes a proof file into `/run/spire/lxd` and the server reads it back through LXD's files API. With `config_push`, the server writes a one-time value into the instance's configuration and the agent reads it through `/dev/lxd/sock`.
4. The server plugin reads the instance again, checks that nothing changed meanwhile, and issues the agent's SPIFFE ID, `spiffe://<trust-domain>/spire/agent/lxd_iid/<server_id>/<volatile.uuid>`, with *selectors* built from what LXD reports, such as `lxd_iid:project:spire-nodes`.

Nothing the instance says about itself becomes part of its identity: the claim only tells the server where to look.

## The components and where they run

| Component | Package | Runs on |
| --------------- | ------------------ | ---------------------------------------------- |
| Server plugin | `lxd-server-plugin` | Every SPIRE Server, as a plugin process SPIRE starts |
| Agent plugin | `lxd-agent-plugin` | Every LXD instance that runs SPIRE Agent, usually baked into the image |
| LXD identity | none | An LXD identity (client certificate or bearer token) with narrow grants, created on every LXD server or cluster that SPIRE Server attests instances of |

![How an instance's SPIRE Agent gets its identity](attestation-overview.pdf){width=100%}

## How this guide is organized

The chapters follow the order of a first installation:

- *Prerequisites* and *Getting and verifying a release* prepare the hosts, the decisions to make first, and the packages.
- *Preparing LXD* creates the projects, the SPIRE identity and its grants.
- *Installing the server plugin* and *Installing the agent plugin* install and configure each plugin.
- *Choosing the proof mode* compares `file_pull` and `config_push` with what the lab measured, and *Clusters* covers LXD clusters.
- *End-to-end check* confirms that an instance attests, and *Upgrades and removal* covers what comes later.
- The *Configuration reference* describes every configuration key of both plugins: its purpose, default, choices, dependencies, caveats and security implications.

Commands are shown for a root shell (or with `sudo`) on Debian and Ubuntu (`apt`, `dpkg`) and RHEL-like systems (`dnf`, `rpm`); LXD commands use the `lxc` client against the LXD server. Paths are those the packages install.
