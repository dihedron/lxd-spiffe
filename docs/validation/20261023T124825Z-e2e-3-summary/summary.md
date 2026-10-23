# 20261023T124825Z-e2e-3-summary

SPIRE 1.15.5, plugins 0.0.0-SNAPSHOT-e2f13e2, c5 LXD 5.21.8, c6 LXD 6.9, lxd5 LXD 5.21.8, lxd6 LXD 6.9

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | a claim naming another instance | container |  | instance_not_found | 0 |  | time="2026-10-23T12:46:50Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
| lxd5 | an identity lacking can_access_files | container |  | lxd_unavailable | 0 |  | time="2026-10-23T12:47:14Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
| lxd5 | outside the allowed projects | container |  | scope_denied | 0 |  | time="2026-10-23T12:46:25Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
| lxd6 | a claim naming another instance | container |  | instance_not_found, rate_limited | 0 |  | time="2026-10-23T12:47:56Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
| lxd6 | an identity lacking can_access_files | container |  | lxd_unavailable | 0 |  | time="2026-10-23T12:48:16Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
| lxd6 | outside the allowed projects | container |  | scope_denied | 0 |  | time="2026-10-23T12:47:31Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_instance): attestation failed" |
