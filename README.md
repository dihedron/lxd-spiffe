# lxd-spiffe

SPIFFE node attestation for LXD instances: a pair of SPIRE plugins, `lxd_iid`, that let a SPIRE Agent running inside an LXD container or virtual machine prove *which instance it runs in*, so that SPIRE Server issues it an identity built from what LXD itself says about that instance.

- `lxd-agent-plugin`: the SPIRE Agent node attestor, run inside the instance.
- `lxd-server-plugin`: the SPIRE Server node attestor, which verifies the agent through the LXD API.

LXD has no signed instance identity document, so the server plugin does not trust anything the guest says about itself. The agent sends a lookup claim (project, instance name) and a nonce; the server finds the instance through the LXD REST API, then proves that the agent really sits inside it with a challenge that only that instance can answer: a root-owned proof file the server reads back through the LXD files API (`file_pull`, the default), or a one-time value the server writes into the instance's configuration and the agent reads through `/dev/lxd/sock` (`config_push`). After the proof it reads the instance again, checks that nothing changed, and issues the SPIFFE ID

```text
spiffe://<trust-domain>/spire/agent/lxd_iid/<server_id>/<volatile.uuid>
```

with selectors taken from the LXD API only.

**Status**: implemented and validated in a lab of real LXD servers (5.21.8, 6.9 and 6.10.0, standalone and three-member clusters) with SPIRE 1.15.5: end-to-end tests, stress and chaos sessions, and a soak with 100 agents. Released: binaries, packages and documents are on the [releases page](https://github.com/dihedron/lxd-spiffe/releases).

## Selectors

Selectors have type `lxd_iid` (in a registration entry, `-selector lxd_iid:project:spire-nodes`). Their trust follows who can change the value they come from:

- **High**: maintained by LXD itself (`volatile.*` keys, derived fields), or configured by the SPIRE operator. No guest and no instance editor can set it.
- **Medium**: set by LXD users with edit rights on the instance, a profile, a project or a network. As trustworthy as the LXD access control around it.
- **Low**: can be changed by those users at will, such as a rename.

Nothing the guest reports about itself (hostname, addresses, OS data) is ever a selector.

| Selector | Trust | Default | Value |
|---|---|---|---|
| `server:<server_id>` | High | on | The `servers` entry (LXD server or cluster) the instance was found on, set by the SPIRE operator |
| `uuid:<uuid>` | High | on | `volatile.uuid`, the instance's identity on its server |
| `type:<type>` | High | on | `container` or `virtual-machine` |
| `project:<name>` | Medium | on | The instance's LXD project |
| `privileged:<bool>` | Medium | on | `security.privileged` (containers only) |
| `profile:<name>` | Medium | on | One per profile applied to the instance |
| `network:<project>/<network>` | Medium | on | One per NIC on a managed network; `nic-parent:<parent>` for an unmanaged NIC |
| `arch:<architecture>` | High | off | The instance's architecture |
| `generation:<uuid>` | High | off | `volatile.uuid.generation`; changes when a snapshot or backup is restored |
| `base-image:<fingerprint>` | High (historic) | off | `volatile.base_image`: the image the instance was created from, not what runs in it now |
| `member:<member>` | High (volatile) | off | The cluster member running the instance at attestation time (clusters only) |
| `image:<os>/<release>` | Medium | off | `image.os` and `image.release` |
| `secureboot:<bool>` | Medium | off | Secure boot configured (VMs only): `boot.mode`, or `security.secureboot` and `security.csm` on 5.21 |
| `config:<key>:<value>` | Medium | off | Each key listed in `selectors.config_keys` |
| `user:<key>:<value>` | Medium | off | Each `user.<key>` listed in `selectors.user_keys` |
| `name:<name>` | Low | off | The instance name (renamable) |

`selectors.enable` in the server plugin's configuration chooses the selectors; the *Setup Guide* describes each one.

## Documentation

Every release publishes three documents (PDF) next to its binaries, built from [docs/](docs/):

- *Setup Guide*: preparing LXD, installing and configuring both plugins, the configuration reference.
- *Architecture and Design*: how the attestation works, the trust model and the threat register.
- *Operator's Guide*: monitoring, routine and incident procedures, the error reference, troubleshooting.

`make docs` builds them into `build/docs/` (needs Docker or Podman). The design specifications, for developers, are in [.specs/](.specs/), and the lab evidence behind them in [docs/validation/](docs/validation/).

## Building

```bash
make            # build both binaries for linux/amd64 into dist/
make snapshot   # archives and deb/rpm packages for every supported platform
make docs       # the release documents
make help       # every target
go test ./...
```

Run with no arguments, each binary serves its plugin to SPIRE; with arguments it is a command line tool (e.g. `lxd-agent-plugin version --verbose`).

`lxd-probe`, also built here, is a lab tool that records how real LXD servers behave, as evidence for the design's validation gates ([.specs/lxd-probe.md](.specs/lxd-probe.md)); the plugins do not need it.

## Lab

`test/lab/lab.sh` builds a lab of real LXD servers on libvirt VMs, on one Linux machine: `lxd5` (LXD 5.21) and `lxd6` (latest 6.x), optionally a three-member cluster of each, and a SPIRE Server, each with the baseline instances, identities and projects that the tests use. It needs Linux on x86-64 with KVM and nested virtualization, and libvirt; `preflight` checks the rest and offers to install what is missing. The lab is specified in [.specs/lxd-test-environment.md](.specs/lxd-test-environment.md).

```bash
test/lab/lab.sh preflight            # check this machine (and install what is missing)
test/lab/lab.sh up                   # build the lab, snapshot it, deploy lxd-probe
test/lab/lab.sh status               # VMs, servers, instances, snapshot, deployed probe
test/lab/lab.sh test                 # lab checks, then gate sessions
test/lab/lab.sh test --scenario plugins --collect   # end-to-end tests with SPIRE, evidence collected
test/lab/lab.sh reset                # back to the snapshot (then deploy)
test/lab/lab.sh deploy               # rebuild and install after a code change
test/lab/lab.sh down                 # destroy the lab
```

Settings are in `test/lab/lab.env`. Working evidence goes to `evidence/` (git-ignored); `--collect` puts the sanitized evidence under `docs/validation/<run_id>/` and the recorded LXD responses under `test/fixtures/<lxd-version>/`, which `go test ./...` replays.
