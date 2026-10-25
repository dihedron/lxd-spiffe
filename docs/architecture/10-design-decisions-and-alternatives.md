# Design decisions and alternatives

## Decisions

**The server goes to LXD for every fact.** The guest cannot produce verifiable evidence, so nothing it sends is trusted beyond a lookup hint, and every selector and ID component comes from LXD's record. The cost is an LXD credential in SPIRE Server, with grants that reach into instances; the design keeps them as narrow as LXD allows and makes them visible.

**A proof of presence, not of possession.** The proof shows that the party on the stream can act inside the running instance now, as LXD confirms. Possession of a key delivered earlier would prove only that someone once received it, from anywhere, and would travel with every copy and backup of the instance.

**`file_pull` by default.** It needs a narrower grant than `config_push` (`can_access_files` rather than `can_edit`), answers in about half the time, has a tighter tail under contention, and leaves nothing behind when an attestation fails. `config_push` remains for deployments that cannot grant file access, or that evacuate cluster members with containers.

**The identity is `volatile.uuid` and the server ID.** The UUID is the only R1 identifier that a copy changes and a rename keeps; since an import keeps it too, the configured server ID and the uniqueness check complete it. The name, which editors can change and which repeats across projects, is only the lookup key.

**One uniform error.** Every refusal looks the same to the agent, so that SPIRE Server's attestation endpoint, reachable before authentication, reveals nothing about which instances exist; operators get the reason and detail in the log.

**Fail closed on ambiguity.** Several matches, a server that cannot be asked, an unexpected owner, a change during the proof: all refuse. A refused attestation is retried by SPIRE Agent; a wrong identity cannot be taken back.

**No cluster logic.** LXD members forward every call the plugin makes; a cluster is one server entry. Failover belongs to a load balancer in front of the members.

**Limits before LXD.** Anyone who reaches SPIRE Server can start an attestation, so the plugin rate-limits and caps before its first LXD call, and stops calling a failing LXD server through a circuit breaker.

**Lab evidence before code.** Every behaviour of LXD the design depends on was recorded on real servers first, and the recordings are the tests' contract. Two findings changed the design substantially: imports keep `volatile.uuid` (hence the server ID and the uniqueness check), and LXD refuses grants in projects the group cannot view (hence project `can_view` in every mode).

**The LXD Go client SDK, wrapped.** Its client packages are Apache-2.0, unlike the rest of LXD (AGPL-3.0); the wrapper adds what the SDK does not enforce (pinning, limits, strict headers, a deadline on every call) and keeps the rest of the code independent of it.

## Alternatives considered

**SPIRE's `join_token`.** The operator mints a single-use token per instance and delivers it. It needs no LXD access at all, but it is a bearer secret that must be delivered and can leak, it says nothing verified about the instance (no LXD selectors), it binds to the instance only by operator discipline, and it cannot re-attest. Suitable for a first test, not for asserting facts about nodes.

**A host-resident SPIRE Agent with an LXD workload attestor.** One agent per LXD host, attested by whatever suits the host, with containers reaching its Workload API socket; a workload attestor maps calling processes to instances. No per-instance agent and no LXD credential in SPIRE Server, with identity rooted in the host, which is trusted anyway. But instances become workloads of the host rather than nodes, it does not suit VMs, and a compromised host agent affects every instance on it. Worth evaluating where most instances are containers; not this project.

**A narrow challenge broker.** A small service inside the LXD trust boundary holding the broad credential and offering SPIRE Server only the three operations the plugin needs (resolve an instance, read the proof at the derived path, write and remove the challenge key), with no pass-through to LXD. It would cut the blast radius of a compromised SPIRE Server (`T10`) at the cost of one more component. Not part of this release; the server plugin is built on small interfaces so that a broker-backed implementation could be added without a protocol change.

**Keys delivered through cloud-init.** A public key in the instance proves nothing about it; a signed statement is a replayable bearer credential; a per-instance private key proves possession, not presence, and cloud-init configuration is readable by every LXD user who can view the instance and travels with copies and backups. A signed, single-use bootstrap token as a *second* factor next to the proof remains a possible later addition.

**`x509pop` with a provisioned certificate.** Strong proof of possession, but a long-lived secret in the instance and no LXD facts. It can complement, not replace, this attestor.

**vTPM.** LXD VMs can have a software TPM, but without a hardware-rooted endorsement chain it adds little to what the LXD API states. A candidate for a separate plugin on hosts with attested hardware TPMs.

**Binding to the source address.** SPIRE's plugin interface does not give the plugin the caller's address, guests choose their own addresses, and NAT hides them.

**A withdrawn proof mode.** An early design had a third proof mode (`ready_signal`), withdrawn before implementation; its protocol numbers stay reserved.
