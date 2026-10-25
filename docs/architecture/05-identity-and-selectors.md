# Identity and selectors

## What LXD can state reliably

Everything the plugin asserts comes from LXD, but not everything LXD reports is equally trustworthy. Each fact has a **reliability tier**, defined by who can change the value:

| Tier | Meaning | Used for |
| --------------- | ------------------------------------------------------ | ---------------------------- |
| R1 Authoritative | Maintained by LXD itself (`volatile.*` keys, derived fields); not writable by guests or by instance editors; changes only through LXD operations | Identity, the SPIFFE ID, high-trust selectors |
| R2 Admin-controlled | Set by LXD users with edit rights on the instance, a profile, a project or a network; as trustworthy as the LXD access control around them | Medium-trust selectors, after a review of who holds those rights |
| R3 Guest-asserted | Comes from the guest, including anything the guest can change through devLXD | Lookup hints only; **never** a selector or part of an ID |
| R4 Unavailable | LXD cannot provide it | A gap, filled by other means or accepted |

### Facts about identity

| Fact | LXD source | Tier | Notes |
| ---------------- | ------------------------ | ------ | -------------------------------------------------- |
| Instance exists and runs | instance `status` | R1 | A point-in-time statement, hence the re-read after the proof |
| Instance UUID | `volatile.uuid` | R1 | Basis of the SPIFFE ID. LXD documents it as globally unique, but it is unique per server only: an export and import keeps it, on another server and on the same one. A copy gets a new one; a move, a rename, a snapshot restore and `lxd recover` keep it |
| Generation | `volatile.uuid.generation` | R1 | Changes on a snapshot restore and a cross-server import; not on a move, rename or recovery |
| cloud-init instance ID | `volatile.cloud-init.instance-id` | R1 | A consistency check on the claim; a rename changes it |
| Type, architecture | instance `type`, `architecture` | R1 | |
| Cluster member | instance `location` | R1, volatile | Correct within 1 s after a move or evacuation; `none` on a standalone server |
| Name | instance `name` | R2 | Unique per project; editors can rename or recreate. The lookup key, never the identity |
| Project | instance `project` | R2 | An administrative namespace; the allow-list is the main scoping control |

### Facts about provenance and configuration

| Fact | LXD source | Tier | Notes |
| ---------------- | ------------------------ | ------ | -------------------------------------------------- |
| Base image | `volatile.base_image` | R1, historic | The image the instance was created from, not what runs in it today; unchanged by copy, move, rename, restore, import and recovery |
| Image properties | `image.os`, `image.release` | R2 | Copied from the image at creation; editable |
| Profiles | instance `profiles` | R2 | A profile edit changes many instances at once |
| Privileged container | `security.privileged` | R2 | Key for policy: root in a privileged container is effectively host root |
| Low-level overrides | `raw.lxc`, `raw.idmap`, `raw.qemu`, ... | R2 | Can weaken isolation; denied by configuration |
| Secure boot | `boot.mode` (6.x), `security.secureboot` and `security.csm` (5.21) | R2 | A statement about configuration, not measured boot |
| Networks | NIC `network` or `parent` | R2 | |
| Free-form keys | `user.*` | R2 | Any editor of the instance sets them |

### Facts that look reliable but are not

- **Container isolation**: all containers share the host kernel, and root in a privileged container can take over the host and every other instance. Privileged containers are refused by default and exposed as a selector.
- **IP addresses**: LXD reads them from inside the instance's network namespace, so root in the instance chooses them (R3). SPIRE's plugin interface does not give the plugin the caller's address either, so address binding is not possible.
- **The guest's own idea of its name**: the hostname and meta-data are the guest's to change (R3). A lookup hint, checked by the proof.

### What LXD cannot attest (R4)

The integrity of the software inside the instance; the integrity of the LXD host; which process inside the instance is calling; a signed identity document or a trusted clock inside the instance.

## The SPIFFE ID

```text
spiffe://<trust-domain>/spire/agent/lxd_iid/<server_id>/<volatile.uuid>
```

The path follows a template (`agent_path_template`) that may use only the plugin name, the configured `server_id` and the instance's `volatile.uuid`: configured and R1 values. The `server_id` is necessary because `volatile.uuid` is unique per LXD server only; it is also what tells instances of different servers apart in registration entries.

**Uniqueness on one server.** An attacker with the right to create instances and a backup of an instance could import a twin with the same UUID, on the same server, which would get the original's SPIFFE ID. Before every challenge, the plugin therefore lists, on every configured server and allowed project, the instances with the resolved UUID, and refuses the attestation unless exactly one exists, the resolved instance itself (`instance_ambiguous`). The list needs project `can_view_instances`: an identity without it does not see a twin it has no grant on, as the lab showed.

## The selectors

Selectors have type `lxd_iid` and take every value from the first instance read and the configuration; guest-asserted values are never offered. A value that is not valid UTF-8, holds a control character or is longer than 256 bytes is left out with a warning, never truncated, since two long values cut to the same prefix would give the same selector. Values are not escaped: SPIRE treats a selector value as an opaque string.

::: landscape

| Selector | Trust | Default | Source and notes |
| ------------------------------ | ---------------- | ----------- | ------------------------------------------------------------------------ |
| `lxd_iid:server:<server_id>` | High | on | The configured server entry the instance was found on |
| `lxd_iid:uuid:<volatile.uuid>` | High (R1) | on | |
| `lxd_iid:project:<name>` | Medium (R2) | on | |
| `lxd_iid:type:<type>` | High (R1) | on | `container` or `virtual-machine` |
| `lxd_iid:arch:<architecture>` | High (R1) | off | |
| `lxd_iid:privileged:<bool>` | Medium (R2) | on | Containers only; `false` when unset. The basis of "unprivileged only" entries |
| `lxd_iid:profile:<name>` | Medium (R2) | on | One per profile applied |
| `lxd_iid:network:<project>/<network>` | Medium (R2) | on | One per NIC on a managed network, with the project that holds the network: the instance's when its project has `features.networks=true`, `default` otherwise (read from the project). An unmanaged NIC gives `nic-parent:<parent>` |
| `lxd_iid:member:<member>` | High (R1), volatile | off | Clusters only: the member at attestation time; changes on migration and evacuation |
| `lxd_iid:base-image:<fingerprint>` | High (R1), historic | off | Only when `volatile.base_image` is set |
| `lxd_iid:image:<os>/<release>` | Medium (R2) | off | Only when both `image.os` and `image.release` are set |
| `lxd_iid:secureboot:<bool>` | Medium (R2) | off | VMs only: `true` when `boot.mode` is `uefi-secureboot` (6.x) or, without `boot.mode`, when `security.secureboot` is not false and `security.csm` is not true (5.21); secure boot is on when nothing is set |
| `lxd_iid:config:<key>:<value>` | Medium (R2) | off | Each key of `selectors.config_keys` that is set |
| `lxd_iid:user:<key>:<value>` | Medium (R2) | off | Each `user.<key>` of `selectors.user_keys` that is set |
| `lxd_iid:generation:<uuid>` | High (R1) | off | Pins entries to one timeline of the instance; changes on restore |
| `lxd_iid:name:<name>` | Low (R2, renamable) | off | |

:::

All values come from the expanded configuration and devices, so values set through profiles count. The trust of a medium-trust selector is the trust of the LXD access control over its source: whoever can edit a profile, a `user.*` key, an image property or a network attachment can make an instance match an entry that relies on it (`T9`).
