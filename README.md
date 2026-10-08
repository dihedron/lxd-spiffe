# lxd-spiffe

SPIFFE node attestation for LXD instances: a pair of SPIRE plugins (`lxd_iid`).

- `lxd-agent-plugin`: the SPIRE Agent node attestor, run on the instance.
- `lxd-server-plugin`: the SPIRE Server node attestor, which verifies what the agent presents.

**Status**: skeleton. Both plugins are stubs that answer every attestation with `Unimplemented`; the design is being written in [.specs/lxd-spire-plugins.md](.specs/lxd-spire-plugins.md).

## Building

```bash
make            # build both binaries for linux/amd64 into dist/
make snapshot   # archives and deb/rpm packages for every supported platform
make help       # every target
go test ./...
```

Run with no arguments, each binary serves its plugin to SPIRE; with arguments it is a command line tool (e.g. `lxd-agent-plugin version --verbose`).
