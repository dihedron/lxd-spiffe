# Components and trust boundaries

## Components

| Component | Runs | Role |
| ------------- | ------------------------ | -------------------------------------------------- |
| Agent plugin (`lxd-agent-plugin`) | Inside each instance, as a subprocess of SPIRE Agent, as root | Collects the claim, generates the agent nonce, answers the challenge: writes the proof file (`file_pull`) or reads the challenge key through devLXD (`config_push`) |
| Server plugin (`lxd-server-plugin`) | On each SPIRE Server host, as a subprocess of SPIRE Server | Validates the claim, resolves the instance, applies the policy, runs the challenge, re-verifies, builds the SPIFFE ID and selectors |
| LXD daemon | On each LXD host or cluster member | Holds the authoritative record of every instance; serves the REST API the server plugin uses and devLXD inside instances |
| SPIRE identity | An LXD identity and its authorization groups | The server plugin's credential towards LXD: a client certificate or, on LXD 6.x, a bearer token, with narrow grants |

Both plugins are static Go binaries without CGO, for Linux on amd64 and arm64, built with the SPIRE plugin SDK. The server plugin talks to LXD through the LXD Go client SDK (its `client` and `shared` packages, Apache-2.0 licensed), wrapped by the project's own client, which adds certificate pinning, response size limits, strict decoding and an explicit project on every call. The agent plugin has no dependency in the guest beyond the kernel.

The protocol between the two plugins is protobuf, carried in the opaque byte fields of SPIRE's node attestation stream: the payload, the challenge and the challenge response. It is versioned (version 1); its field numbers never change after a release.

## Trust boundaries

![Components and trust boundaries](trust-boundaries.pdf){width=55%}

| ID | Boundary | Protection |
| ---- | ------------------------ | -------------------------------------------------------- |
| TB1 | Instance and LXD host | Containers: the shared kernel's namespaces and LXD's confinement; VMs: the hypervisor (QEMU/KVM). Containers are a weaker boundary than VMs, and *privileged* containers weaker still: their root is the host's |
| TB2 | Agent and SPIRE Server | SPIRE's TLS, with the server authenticated through the agent's bootstrap trust bundle |
| TB3 | SPIRE Server and the LXD API | TLS 1.3, the LXD server's certificate pinned by fingerprint or verified against a CA, mutual TLS with a dedicated client identity or a bearer token |
| TB4 | Plugin binary and SPIRE process | SPIRE's go-plugin subprocess, with the binary's SHA-256 pinned in SPIRE's configuration (`plugin_checksum`) |
| TB5 | Processes inside one instance | Not a boundary: any root process in the instance *is* the node |

## Trust assumptions

- **The LXD daemon, its hosts and LXD's server administrators are trusted.** The design limits and detects misbehaving instance-level users, but cannot defend against an LXD administrator or a compromised host, which controls every claim (`T17`).
- **Root in an instance is the node.** Node identity is instance identity: any root process in the instance can obtain the node's identity, and so the workload identities issued to it.
- **LXD identifies devLXD callers correctly and reports instance state correctly.**
- **Instance names are unique per project, not globally**, and projects are an administrative namespace. The identity therefore never rests on a name.

## Where the data comes from

The server plugin decides from three sources, and from nothing else:

- **The claim** (from the guest): project, instance name, cloud-init instance ID. Only a lookup hint. A wrong claim makes the attestation fail; it can never make it succeed for another instance, because the proof is bound to the instance the server looked up.
- **The LXD API** (with SPIRE's credential): the instance's record (`volatile.*` keys, expanded configuration and devices, status, type, location, profiles), its project's `features.networks`, and the proof read back.
- **The server's configuration**: the `server_id`, the allowed projects, the policy and the selectors.
