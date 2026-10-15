# lxd-agent does not start in RHEL 9 VMs with SELinux enforcing

`lxd-agent` cannot start in a RHEL 9 derivative VM once the SELinux targeted policy is installed and enforcing. The agent runs in the `init_t` domain, which may not create the vsock socket it listens on. Without the agent, `lxc exec`, `lxc file`, devLXD and cloud-init (which reads its data through devLXD in a VM) all stop working.

## Environment

- LXD 5.21.8 (snap, `5.21/stable`, rev 40958) and LXD 6.9 (snap, `latest/stable`, rev 40916), on Ubuntu 24.04 hosts, ZFS storage.
- Guest: `images:rockylinux/9/cloud`, Rocky Linux 9.8, kernel 5.14.0-687.57.1.el9_8, `selinux-policy-targeted` 38.1.75-2.el9_8.1.
- The image ships without an SELinux policy (`getenforce` reports `Disabled`), so the problem appears only after a policy is installed, as most RHEL deployments do. AlmaLinux 9 uses the same agent setup and is expected to behave the same; it was only tested without a policy.

## Steps to reproduce

```sh
lxc launch images:rockylinux/9/cloud r9 --vm
# wait until `lxc exec r9 -- true` works
lxc exec r9 -- dnf -y install selinux-policy-targeted audit   # sets SELINUX=enforcing
lxc exec r9 -- touch /.autorelabel
lxc restart --force r9
# the relabel boot ends with the VM powered off in our runs: lxc start r9
lxc exec r9 -- true
```

## Expected

`lxd-agent` starts and `lxc exec r9 -- true` succeeds, as it does before the policy is installed.

## Actual

`lxc exec` fails with `Error: LXD VM agent is not currently running`, indefinitely. The guest's journal shows the agent failing until systemd's start limit is reached:

```
lxd-agent[437]: level=info msg=Starting
lxd-agent[437]: level=info msg=Stopped
lxd-agent[437]: Error: Failed initialising daemon: Failed starting HTTP server: Failed listening on vsock: listen vsock vm(4294967295):8443: socket: permission denied
systemd[1]: lxd-agent.service: Main process exited, code=exited, status=1/FAILURE
...
systemd[1]: lxd-agent.service: Start request repeated too quickly.
```

and the audit log:

```
type=AVC msg=audit(...): avc:  denied  { create } for  pid=437 comm="lxd-agent"
  scontext=system_u:system_r:init_t:s0 tcontext=system_u:system_r:init_t:s0
  tclass=vsock_socket permissive=0
```

## Cause

`lxd-agent.service` runs `ExecStartPre=/lib/systemd/lxd-agent-setup`, which copies the agent to `/run/lxd_agent/lxd-agent` on a tmpfs. Files there are labelled `var_run_t`:

```
-r-x------. 1 root root system_u:object_r:var_run_t:s0 14845144 lxd-agent
```

systemd then executes `ExecStart=/run/lxd_agent/lxd-agent`. Executing a `var_run_t` file triggers no domain transition, so the agent stays in systemd's domain, `init_t`. The targeted policy does not allow `init_t` to create `vsock_socket`s.

## Workaround

A systemd drop-in that relabels the fresh copy as `bin_t` before every start. systemd then runs it in `unconfined_service_t`, as the targeted policy does for any service binary labelled `bin_t`:

```ini
# /etc/systemd/system/lxd-agent.service.d/selinux.conf
[Service]
ExecStartPre=/usr/bin/chcon -t bin_t /run/lxd_agent/lxd-agent
```

The drop-in's `ExecStartPre` runs after the unit's own `lxd-agent-setup`, so it labels the copy that setup has just made. Tested with SELinux enforcing on LXD 5.21.8 and 6.9:

- the agent runs as `system_u:system_r:unconfined_service_t:s0`, and `lxc exec` and `lxc file` work;
- no AVC denials across three cold boots;
- with the drop-in skipped for one start, that start fails with the error above, and the next start (with the drop-in) succeeds.

Copying the binary to `/usr/local/bin` and pointing `ExecStart` there also works. But it freezes the agent at that version, while LXD delivers a fresh agent matching the host on every boot.

## Suggested fix

When SELinux is enabled, have `lxd-agent-setup` give the copy an executable label, for example `chcon -t bin_t /run/lxd_agent/lxd-agent`, or `restorecon` with a file-context rule for `/run/lxd_agent/lxd-agent`. Alternatively, ship a small policy module that gives `lxd-agent` a domain of its own with the vsock permissions it needs.

## Side observation

In our runs, the boot that performs the SELinux relabel ended with the VM powered off, not restarted, so `lxc start` was needed. We have not looked into whether this comes from LXD or from the guest's relabel service.
