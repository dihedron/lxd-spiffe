# Context and goals

This document describes how *lxd-spiffe* works and why it is built the way it is, for architects, security reviewers and auditors. It is self-contained; the project's specifications, in its source repository, hold the full design detail for its developers, and its lab evidence (`docs/validation/`) the measurements this document cites. The *Setup Guide* and the *Operator's Guide* cover installation and operations.

## The problem

SPIRE issues SPIFFE identities to workloads, and to decide which workloads run where it first identifies the machine each SPIRE Agent runs on: *node attestation*. A node attestor must let SPIRE Server tell, beyond doubt, which machine an agent runs on, and what can be trusted about that machine.

On public clouds the platform signs an *instance identity document* that the instance fetches from the hypervisor and hands to SPIRE, which verifies the signature. **LXD offers nothing like it.** What an instance can learn about itself through LXD's guest API, devLXD (`/dev/lxd/sock`), is read-only data about itself; LXD authenticates the caller on that socket, but gives the guest nothing that a third party could verify. Newer LXD versions can issue a guest a bearer token, but it authenticates the guest *to LXD*, and SPIRE Server cannot verify it.

So an LXD guest cannot carry any evidence about itself to SPIRE Server, and SPIRE Server cannot learn anything about the guest from what the guest sends. Pre-shared secrets (SPIRE's `join_token`, a certificate baked into the image) work, but they are bearer secrets that must be delivered and can leak, and they say nothing about the instance.

## The solution

SPIRE Server goes to LXD itself, with its own credential, for every fact it asserts, and proves that the agent it talks to sits inside the instance LXD describes:

1. The agent plugin sends a **claim**, which instance it believes it is (project, name, cloud-init instance ID), and a fresh nonce. The claim is a lookup hint and nothing more.
2. The server plugin **resolves** the instance through the LXD REST API and checks it against a policy.
3. It runs a **proof of co-location**: a challenge bound to both nonces that can only be answered from inside that instance, as root, and whose answer the server reads back through LXD.
4. It **reads the instance again**, checks that its identity, generation and location did not change during the proof, and issues the agent's SPIFFE ID, built from LXD's `volatile.uuid`, with selectors taken from LXD's record of the instance.

![How an instance's SPIRE Agent gets its identity](attestation-overview.pdf){width=100%}

Every claim made about the node comes from LXD, never from the guest.

## Goals

- **Identity from the platform**: the agent's SPIFFE ID and selectors derive from what the LXD API reports about the instance, never from what the guest says.
- **No secret in the instance**: nothing to bake into images or deliver per instance; the challenge values live for seconds.
- **Least privilege towards LXD**: SPIRE's LXD identity holds only the grants the chosen proof mode needs, in the projects of SPIRE-managed instances.
- **Fail closed**: any error, ambiguity or change during an attestation refuses it, with one uniform answer to the agent and the detail in SPIRE Server's log.
- **Real LXD behaviour**: every assumption about LXD that the design depends on was checked against real LXD servers (5.21.8, 6.9 and 6.10.0, standalone and clustered) before the code depending on it was written.

## Non-goals

- **Workload attestation**: the plugins attest nodes; SPIRE's workload attestors identify workloads.
- **Attesting the LXD host**: the host and the LXD daemon are trusted (*Threat model*); a host-resident agent is discussed among the alternatives (*Design decisions and alternatives*).
- **Windows guests**, **Incus** (a fork of LXD with a separate API), **LXD from distribution packages** (none ships a supported version), and **vTPM-based attestation**.
