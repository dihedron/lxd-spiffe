# LXD SPIRE node attestation — documentation spec

Oct 25, 2026 · @Andrea Funtò · **Approved Oct 25, 2026**

## Overview

The plugins have a README for developers and evaluators and specs that hold the authoritative design. People who deploy and run them need documents written for them. This spec adds three, built as PDFs with pandoc and published with every release next to the archives and packages, modelled on the documents of `github.com/dihedron/openstack-spiffe`:

| Document | For | Answers |
| ------------ | ----------------------------- | ------------------------------------------------------------ |
| **Setup Guide** | Whoever installs the plugins: LXD and SPIRE operators, image builders | How do I prepare LXD, install and configure the server plugin and the agent plugin, and check that they work? |
| **Architecture and Design** | Architects, security reviewers, auditors | How does the attestation work, why is it built this way, what does it protect against and what does it not? |
| **Operator's Guide** | Whoever runs it day to day, and on call | How do I watch it, what are the routine and incident procedures, what does each error mean and what do I do about it? |

**Audience and relation to the specs**: the documents speak to operators and architects and are self-contained: a reader never needs the specs. The specs stay the developers' design; the documents restate what their readers need. Architecture and Design restates the full threat register with its current residual risks. The lab evidence (`docs/validation/`) is cited by its findings, not copied.

**The README** gives a concise description of what the plugins do and the table of the selectors that registration entries can use, with their trust level, and points to the documents.

**Out of scope**: an HTML site, translations, documentation of the code.

## Toolchain

The toolchain of openstack-spiffe, already in `docs/`:

- pandoc with LaTeX and the Eisvogel template, from the `pandoc/extra` container image pinned by version and digest; Mermaid diagrams (`docs/common/diagrams/*.md`, one `mermaid` block each) rendered by the pinned `mermaid-cli` image; `docs/build.sh` (`make docs`) builds into `build/docs/`.
- One directory per document (`setup-guide/`, `architecture/`, `operators-guide/`), one file per chapter, in file order; `metadata.yaml` per document; `docs/common/` for the shared defaults, the front matter, the filters, the diagrams and the artwork.
- **Filters**: `include.lua` replaces a code block marked `{include="examples/server.conf"}` with the file, so the documents show the packaged samples; `landscape.lua` turns a `::: landscape` div into a landscape page, for a table or a diagram that does not fit in portrait (decided Oct 25); `breakcode.lua` lets LaTeX break inline code after `_`, `.`, `/`, `-` and `:`, so that keys and paths in narrow table columns wrap instead of overflowing (the open item of the openstack-spiffe documents).
- **Artwork**: the project logo (`docs/common/artwork/logo.png`, 1024 × 1024 pixels) on every title page; the copyright page credits the Go gopher (Renée French, CC BY 4.0).
- **Version stamping**: the release tag, or `git describe`, and the build date on the title page and in the header.

## Build and publication

- `make docs` builds `lxd-spiffe-setup-guide-<version>.pdf`, `lxd-spiffe-architecture-and-design-<version>.pdf` and `lxd-spiffe-operators-guide-<version>.pdf` into `build/docs/`; `make release` builds them with the release tag first.
- `.github/workflows/docs.yml` builds them on every change to `docs/` or `examples/` and attaches them to the run.
- goreleaser attaches them to the release (`release.extra_files`) and lists them in the signed checksums file (`checksum.extra_files`). The packages do not install them.
- The packages install the samples `examples/server.conf` and `examples/agent.conf` under `/usr/share/doc/<package>/`; they are complete, commented configurations that the plugins accept (checked by a test).

## Content

The outlines fix the scope, not the wording. Every configuration key, path, command and log message is in code font.

### Setup Guide

1. **Introduction**: what the plugins do; where each runs; what the reader needs.
2. **Prerequisites**: LXD (5.21 LTS or later, as the snap), SPIRE, guests (Linux, amd64 and arm64; the families tested), network paths, what to decide first (proof mode, projects, `server_id`).
3. **Getting and verifying a release**: download, checksums and signatures, `plugin_checksum`.
4. **Preparing LXD**: dedicated projects; the SPIRE identity (client certificate or bearer token); the authorization groups and the least-privilege grants per proof mode, sharding beyond 90 instances; restricted projects for `config_push`; the server certificate fingerprint.
5. **Installing the server plugin**: package, SPIRE Server configuration from the sample, `servers`, policy, selectors, `reattest`, `agent_ttl`, limits; registration entries.
6. **Installing the agent plugin**: package or image, SPIRE Agent as root, the claim, VMs (`lxd-agent` ordering), RHEL 9 derivatives with SELinux enforcing, timeouts.
7. **Choosing the proof mode**: the two modes compared with the lab's measurements, when to use which, what attestation is not compatible with.
8. **Clusters**.
9. **End-to-end check**.
10. **Upgrades and removal**.
11. **Configuration reference**: every key of both plugins, with purpose, default, values or range, dependencies, caveats and security notes; checked for completeness by a test.

### Architecture and Design

1. **Context and goals**: the problem (no signed identity document in LXD), the solution in one diagram, goals and non-goals.
2. **Components and trust boundaries**.
3. **The attestation sequence**, step by step, with the wire protocol.
4. **Proof of co-location**: both modes, the isolation check, the expected owner, mode negotiation, the privilege cost of each mode.
5. **Identity and selectors**: what LXD can state reliably (the reliability tiers), the SPIFFE ID, the selectors and their trust, uniqueness of `volatile.uuid`.
6. **LXD integration**: the client, TLS and credentials, the calls and required extensions, least privilege, clusters, limits and the circuit breaker.
7. **Observability**.
8. **Threat model**: assets, actors, trust assumptions, the threat register with residual risks, the security requirements.
9. **Validation**: how the design was validated (gates, end-to-end, stress, soak) and on which versions.
10. **Design decisions and alternatives**.

### Operator's Guide

1. **Operating model**: what runs where, what is stateful, what restarts do.
2. **Monitoring**: the log lines, the metrics derived from them, the alerts to set (a crashed plugin above all).
3. **Routine procedures**: adding instances (grants), adding an LXD server, rotating credentials, upgrading, cluster maintenance, evicting agents, copies and restores.
4. **Incident procedures**: a stolen SPIRE credential, a suspected forged node, an LXD daemon crash, a crashed plugin, a failed evacuation.
5. **Error reference**: every reason code; every error the plugins log or return, configuration errors included; checked for completeness by a test.
6. **Troubleshooting**: from symptoms to causes.
7. **Operational guidance**: the attestor spec's *Operational guidance* items 1–18, which must appear in the user documents.

## Keeping the documents true

- A change of behaviour, configuration, error or log message updates the documents in the same change.
- `docs/docs_test.go`: the configuration reference holds every key of both plugins (their `ConfigKeys()`); the error reference holds every reason code and the fixed text of every error of the plugins and the policy; the samples decode with the plugins' own key lists; every table sets its column proportions.

## Implementation plan

| Chunk | Content |
| ------ | ------------------------------------------------------------ |
| 1 | README and this spec |
| 2 | Toolchain: the two new documents registered, the filters, the samples, `docs/docs_test.go` |
| 3 | Setup Guide |
| 4 | Architecture and Design, with its diagrams |
| 5 | Operator's Guide |

One pull request for all of them (decided Oct 25).
