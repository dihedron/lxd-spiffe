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

The SPIRE Agent's data directory (`data_dir`, usually `/var/lib/spire/agent`) holds the agent's SVID and key once it has attested. An instance copied or restored with that directory is the original's node as far as SPIRE is concerned: see *Operator's Guide*, *Routine procedures*, before you copy or restore an instance that runs an agent, and *tmpfs mode* below if you copy instances routinely.

## tmpfs mode (optional)

By default SPIRE Agent keeps its data on the instance's disk, and that is the right choice unless instances that run an agent get copied. *tmpfs mode* keeps the agent's data in memory instead. It is a way of configuring SPIRE Agent, for containers and VMs alike: the plugins neither need nor notice it.

**What it protects against.** Anything that copies an instance's storage (`lxc copy`, a snapshot copied into a new instance, the usual way to copy a running VM, `lxc export` and `import`, a copy or backup of the volume) also copies the agent's SVID and private key. The copy's agent then never attests: it uses the original's identity, and its workloads receive the original's workload SVIDs. The plugin never sees the copy, so it cannot refuse it. In tmpfs mode the storage holds no SVID and no key, so a copy starts without them and has to attest as itself (it has its own `volatile.uuid`, so its own SPIFFE ID). A stolen volume or backup also yields no node key.

**What it does not protect against.** Stateful snapshots and stateful migration (`lxc snapshot --stateful`, `migration.stateful`), which save the instance's memory, tmpfs included; and anyone with root in the running instance.

**When you do not need it.** If your instances are never copied, cloned, imported or restored into new instances, or if every copy follows the procedure in *Operator's Guide*, *Routine procedures*, *Copies and restores*, the threat does not exist in your deployment, and tmpfs mode only adds its costs. Leave it off.

**What it costs.**

- **Every boot attests again**, as the same instance. Mass reboots, and LXD daemon restarts, which restart every container on the server, cause a burst of attestations. A VM also waits for `lxd-agent` at every boot.
- **No SVID until the attestation succeeds.** If SPIRE Server or the LXD server is down when an instance boots, its workloads get no SVID until both are back. An agent with persistent data keeps serving its SVID until it expires.
- **`reattest = "allow"` on the server** (the default). With `deny`, the attestation after a reboot is refused until the agent is evicted.
- **The trust bundle file must stay current.** After a reboot SPIRE Agent bootstraps from `trust_bundle_path` again, not from the bundle it kept up to date. Once SPIRE Server's CA has rotated past that file, rebooted agents cannot connect. Refresh the file from your configuration management when the CA rotates.

**Configuring it** (systemd). Point the agent's data and key at a runtime directory:

```hcl
agent {
  data_dir = "/run/spire/agent-data"
  # ...
}
plugins {
  KeyManager "disk" {
    plugin_data {
      directory = "/run/spire/agent-data"
    }
  }
  # NodeAttestor "lxd_iid" and the others as before
}
```

and let systemd create it, in SPIRE Agent's unit or a drop-in:

```ini
[Service]
RuntimeDirectory=spire/agent-data
RuntimeDirectoryMode=0700
RuntimeDirectoryPreserve=yes
```

`RuntimeDirectoryPreserve=yes` keeps the directory when SPIRE Agent restarts, so a restart does not attest; a reboot empties it. `KeyManager "memory"` is the stricter variant: the key does not survive an agent restart either, so every restart attests. Do not put the data under `/run/spire/lxd`, the proof directory.

Check it after the first attestation: `ls /run/spire/agent-data` lists the agent's files and `/var/lib/spire/agent` holds none.

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
