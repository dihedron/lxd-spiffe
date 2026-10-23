# lxd-spiffe

SPIFFE node attestation for LXD instances: a pair of SPIRE plugins (`lxd_iid`).

- `lxd-agent-plugin`: the SPIRE Agent node attestor, run on the instance.
- `lxd-server-plugin`: the SPIRE Server node attestor, which verifies what the agent presents.
- `lxd-probe`: a lab-only tool that records how real LXD servers behave, as evidence for the design's validation gates ([.specs/lxd-probe.md](.specs/lxd-probe.md)). It is not shipped with the plugins.

**Status**: skeleton. Both plugins are stubs that answer every attestation with `Unimplemented`; the design is being written in [.specs/lxd-spire-plugins.md](.specs/lxd-spire-plugins.md).

## Building

```bash
make            # build both binaries for linux/amd64 into dist/
make snapshot   # archives and deb/rpm packages for every supported platform
make help       # every target
go test ./...
```

Run with no arguments, each binary serves its plugin to SPIRE; with arguments it is a command line tool (e.g. `lxd-agent-plugin version --verbose`).

## Lab

`test/lab/lab.sh` builds a lab of real LXD servers on libvirt VMs, on one Linux machine: `lxd5` (LXD 5.21) and `lxd6` (latest 6.x), each with the baseline instances, probe identities and projects that the gate runbooks use. It needs Linux on x86-64 with KVM and nested virtualization, and libvirt; `preflight` checks the rest and offers to install what is missing. The lab is specified in [.specs/lxd-test-environment.md](.specs/lxd-test-environment.md).

```bash
test/lab/lab.sh preflight            # check this machine (and install what is missing)
test/lab/lab.sh up                   # build the lab, snapshot it, deploy lxd-probe
test/lab/lab.sh status               # VMs, servers, instances, snapshot, deployed probe
test/lab/lab.sh test                 # lab checks, then gate sessions
test/lab/lab.sh test --collect       # ... and copy the sessions' evidence into the repository
test/lab/lab.sh reset                # back to the snapshot (then deploy)
test/lab/lab.sh deploy               # rebuild and install lxd-probe after a code change
test/lab/lab.sh probe lxd6 --as files -- server whoami
test/lab/lab.sh guest lxd5 c-unpriv -- version
test/lab/lab.sh down                 # destroy the lab
```

Settings are in `test/lab/lab.env`. Working evidence goes to `evidence/` (git-ignored); `--collect` puts the sanitized evidence under `docs/validation/<run_id>/` and the recorded LXD responses under `test/fixtures/<lxd-version>/`, which `go test ./...` replays.
