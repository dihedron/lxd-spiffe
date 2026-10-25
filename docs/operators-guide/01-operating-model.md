# Operating model

This guide is for the people who run *lxd-spiffe* day to day and on call: what to watch, the routine and incident procedures, what every error means, and how to troubleshoot. The *Setup Guide* covers installation and configuration; *Architecture and Design* explains how the attestation works.

## What runs where

| Part | Where | Lifecycle |
| ------------------ | ------------------------------ | -------------------------------------------------- |
| Server plugin | A subprocess of each SPIRE Server | Started and stopped by SPIRE Server; configured from SPIRE Server's configuration |
| Agent plugin | A subprocess of SPIRE Agent in each instance | Started by SPIRE Agent; runs only while SPIRE Agent attests |
| SPIRE identity | An LXD identity and its groups on each LXD server or cluster | Created once; instance grants follow the instances' lifecycle; its credential is rotated |

Neither plugin runs a service of its own, listens on a port, or keeps state on disk.

## What is stateful

- **The server plugin keeps no persistent state.** In memory it holds the limits' counters, each LXD server's connection and circuit breaker, and the challenge keys of the attestations it is running. A restart of SPIRE Server loses them harmlessly: attestations in flight fail and are retried.
- **The agent plugin keeps nothing.** The proof file exists for the seconds of one attestation, on a tmpfs.
- **SPIRE Agent's data directory** (`/var/lib/spire/agent`) holds the agent's SVID and key once attested. It is what makes a copied or restored instance pass for the original (*Routine procedures*).
- **LXD** holds, briefly, a `config_push` challenge key on the instance; a key left by an interrupted attestation is removed by the instance's next `config_push` attestation.

## What restarts do

| Restart | Effect |
| -------------------------- | ---------------------------------------------------------------- |
| SPIRE Server | The plugin restarts with it; attestations in flight fail and are retried. Agents keep their SVIDs. After a long outage, agents whose SVIDs expired all attest again: see SPIRE Server's per-address rate limit in *Operational guidance* |
| The server plugin process alone (crash) | **SPIRE does not restart it.** Every attestation then fails until SPIRE Server is restarted (*Monitoring*, *Incident procedures*) |
| SPIRE Agent | The agent reloads its SVID; it attests only if it has none or it expired |
| An LXD daemon crash | LXD restarts every container on that server (VMs keep running): every container's agent restarts and attests again at about the same time |
| `snap restart lxd`, `snap refresh lxd` | Stops and restarts every instance on the server; plan as maintenance |
| An instance reboot | Its agent attests again when its SVID has expired, otherwise not |

## Attestation, renewal and re-attestation

An attested agent holds an SVID valid for SPIRE Server's `agent_ttl`. With this attestor, the agent attests again at about half the SVID's lifetime rather than renewing it, which needs `reattest = "allow"` (the default) on the server. Expect, in steady state, about two attestations per agent per `agent_ttl`: with 1,000 agents and a one-hour lifetime, about one attestation every two seconds.
