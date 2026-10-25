# End-to-end check

This checklist follows one instance from its creation to an attested agent. Run it after the first installation and after every change of LXD, SPIRE or the plugins. When a step fails, the *Operator's Guide* (*Troubleshooting* and *Error reference*) tells what the messages mean.

## SPIRE Server loaded the plugin

```bash
journalctl -u spire-server | grep -i lxd_iid
spire-server healthcheck
```

SPIRE Server logs the plugin's loading under the `NodeAttestor` subsystem. If it refused to start, its log names the configuration key at fault (`invalid plugin configuration: ...`), or reports that the binary's hash does not match `plugin_checksum`.

## An instance with its grants

```bash
lxc launch ubuntu:24.04 e2e-check --project spire-nodes
lxc auth group permission add spire instance e2e-check can_access_files project=spire-nodes
```

With `config_push`, grant `can_edit` instead. Install SPIRE Agent and the agent plugin in the instance (or use an image that has them), with SPIRE Agent's configuration pointing at your SPIRE Server and trust bundle, and `claim.project = "spire-nodes"`.

## The agent attests

Start SPIRE Agent in the instance and watch both sides:

```bash
lxc exec e2e-check --project spire-nodes -- journalctl -u spire-agent -f   # in the instance
journalctl -u spire-server -f | grep -E 'attestation (succeeded|refused)'  # on SPIRE Server
```

On SPIRE Server, the plugin logs `attestation succeeded` with the `server_id`, `project`, `instance_uuid`, `proof_mode`, the `selectors` it issued, and the attestation's `duration_ms` and `proof_ms`. A refusal logs `attestation refused` with a `reason_code` and an `error` that tells why; the agent only sees `attestation failed`, by design.

## The agent is listed

```bash
spire-server agent list
spire-server agent show -spiffeID spiffe://example.org/spire/agent/lxd_iid/lxd-prod/<uuid>
```

The agent's SPIFFE ID ends with the instance's `volatile.uuid`, which LXD shows with `lxc config get e2e-check volatile.uuid --project spire-nodes`. `agent show` lists the selectors, such as `lxd_iid:project:spire-nodes`.

## A workload gets its SVID

Create a node entry that matches the agent and a workload entry under it (*Installing the server plugin*, *Registration entries*), then, in the instance:

```bash
spire-agent api fetch x509 -socketPath /run/spire/agent/public/api.sock
```

## The negative checks

These confirm that the plugin refuses what it must refuse. Each must fail on the agent and log `attestation refused` with the reason shown:

| Check | Expected reason |
| -------------------------------------------------------- | ------------------ |
| An instance in a project that is not in the server's `projects` | `scope_denied` |
| An agent that claims another instance's name (`claim.instance_name`) | `instance_not_found` or `proof_mismatch` |
| An instance whose instance grant was removed | `lxd_unavailable` |
| A privileged container (`security.privileged=true`), with `allow_privileged = false` | `policy_denied` |

Clean up afterwards: `lxc delete --force e2e-check --project spire-nodes`, remove its grant, and evict its agent (`spire-server agent evict -spiffeID ...`).
