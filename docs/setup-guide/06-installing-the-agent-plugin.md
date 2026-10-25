# Installing the agent plugin

The agent plugin runs inside every LXD instance that runs SPIRE Agent. It needs nothing else in the guest: no LXD client, no `cloud-init`, no external program.

## Installing the package

Install it when you build the image, so that every instance created from it can attest at its first boot, or at first boot from cloud-init or your configuration management:

```bash
apt install ./lxd-agent-plugin_<version>_linux_amd64.deb     # Debian, Ubuntu
dnf install ./lxd-agent-plugin_<version>_linux_amd64.rpm     # RHEL-like
```

On distributions without deb or rpm (Alpine), unpack the archive and install the binary as `/usr/bin/lxd-agent-plugin`. The package installs the binary and a commented sample, `/usr/share/doc/lxd-agent-plugin/agent.conf`. Record the binary's checksum for `plugin_checksum`:

```bash
sha256sum /usr/bin/lxd-agent-plugin
```

## Configuring SPIRE Agent

Add a `NodeAttestor "lxd_iid"` block to the `plugins` section of SPIRE Agent's configuration:

```{.hcl include="examples/agent.conf"}
```

- **`claim.project`**: set it. An instance cannot learn its project from inside (devLXD does not report it), so without it the server looks for the instance's name in every project of its allow-list. That works, but costs one LXD call per project, and an instance with the same name in two allowed projects is refused as ambiguous.
- **`claim.instance_name`**: leave it empty. The agent then takes the instance name from devLXD, or from the hostname when devLXD is not available. The claim is only a lookup hint: the proof decides whether it points at the right instance.
- **`proof_dir`**: must be the same as the server's. Leave both at `/run/spire/lxd`.

SPIRE Agent **must run as root**: `file_pull` writes a root-owned proof file in a directory only root can enter, and `config_push` reads devLXD, which only root can use. The plugin refuses to start otherwise (`the agent plugin must run as uid 0`). Running as root is also what makes the proof meaningful: an unprivileged user in the instance cannot attest as the node.

The SPIRE Agent's data directory (`data_dir`, usually `/var/lib/spire/agent`) holds the agent's SVID and key once it has attested. An instance copied or restored with that directory is the original's node as far as SPIRE is concerned: see *Operator's Guide*, *Routine procedures*, before you copy or restore an instance that runs an agent.

## Virtual machines

In a virtual machine, LXD's files API and devLXD are served by `lxd-agent`, which LXD's images start at boot. Until it runs, the server cannot read the proof (`vm_agent_unavailable`). After a cold boot, `lxd-agent` answered in 16 to 27 seconds on Debian, Alpine and Ubuntu, and in 30 to 72 seconds on Rocky Linux and AlmaLinux 9, in the lab; times vary with the OS and the host.

**Start SPIRE Agent after `lxd-agent`**:

- systemd: in SPIRE Agent's unit, `Wants=lxd-agent.service` and `After=lxd-agent.service` (`lxd-agent.service` is `Type=notify`, so systemd waits until it is ready).
- OpenRC: `depend() { after lxd-agent; }` in SPIRE Agent's service script.

Where the order cannot be set, give a VM's attestation more time: the server's `proof_timeout = "90s"`, and on the agent `proof_timeout = "90s"` and `attestation_timeout = "2m"`. SPIRE Agent retries a failed attestation anyway, with a backoff.

### RHEL 9 derivatives with SELinux enforcing

LXD's Rocky Linux 9 and AlmaLinux 9 images ship no SELinux policy for `lxd-agent`. Once the targeted policy enforces, `lxd-agent` cannot start (run from `/run/lxd_agent`, labelled `var_run_t`, it stays in `init_t`, which may not create vsock sockets), and neither proof mode works. Install this drop-in, which relabels the per-boot copy before every start so that systemd runs it as `unconfined_service_t`:

```ini
# /etc/systemd/system/lxd-agent.service.d/selinux.conf
[Service]
ExecStartPre=/usr/bin/chcon -t bin_t /run/lxd_agent/lxd-agent
```

Run `systemctl daemon-reload`, restart the VM, and check with `ps -eZ | grep lxd-agent`.

## Timeouts

Two agent settings bound an attestation:

- **`attestation_timeout`** (default `30s`) bounds the whole attestation on the agent: reading devLXD, waiting for the server's challenge (which includes the server's LXD lookups and any queue in front of it), and the time the proof stays.
- **`proof_timeout`** (default `15s`) is how long the agent keeps the proof after answering, while the server reads it. Keep it at least as long as the server's `proof_timeout` (default `10s`), or the proof can vanish while the server still retries; it must be shorter than `attestation_timeout`.

Raise `attestation_timeout` when many agents share one source address behind NAT and SPIRE Server's per-address rate limit is on: with 100 containers behind one address, the last one waits about 100 seconds after a mass restart. The lab's soak uses `4m` for 100 agents; with the default `30s`, only 41 of 100 attested after a restart.

## Containers

Containers need nothing special: the files API and devLXD work as soon as the container runs. Keep them unprivileged; a privileged container is refused unless the server's policy allows it, and its root is root on the host.

Do not mount shared storage at, above or below `/run/spire/lxd` (so not at `/run` either): the server refuses an instance with such a disk device, since another instance sharing the disk could write the proof.
