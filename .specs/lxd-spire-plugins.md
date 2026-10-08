# LXD node attestor plugins — implementation spec

Oct 8, 2026 · @Andrea Funtò

*Status*: draft skeleton. The repository holds stub plugins only: they accept an empty configuration and answer every attestation with `Unimplemented`. Each section below must be written and approved before the plugins are implemented (see the SDD workflow in `CLAUDE.md`).

## Overview

This spec defines a matched pair of SPIRE plugins, `lxd_iid`, that attest an LXD instance's identity to a SPIRE Server.

- **Agent-side plugin** (`lxd-agent-plugin`): runs inside SPIRE Agent on the instance. Gathers the instance's identity evidence and forwards it to the Server.
- **Server-side plugin** (`lxd-server-plugin`): runs inside SPIRE Server. Verifies the evidence, then emits the agent's SPIFFE ID and selectors.

**Non-goals**: TBD.

## Trust model

TBD: what the evidence is, who vouches for it, and how SPIRE Server verifies it independently of the instance.

## Agent-side plugin

TBD.

## Server-side plugin

TBD: verification, agent SPIFFE ID, selectors, replay protection.

## Configuration

TBD: the `plugin_data` keys of each plugin (`examples/agent.conf`, `examples/server.conf`).

## Testing

TBD.
