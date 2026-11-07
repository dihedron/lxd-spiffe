# 20261107T205058Z-e2e-3-summary

SPIRE 1.15.5, plugins 0.2.0-SNAPSHOT-7744adc, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | a claim naming another instance | container |  | instance_not_found | 0 |  | time="2026-11-07T20:49:34Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
| lxd5 | an identity lacking can_access_files | container |  | lxd_forbidden | 0 |  | time="2026-11-07T20:49:55Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
| lxd5 | outside the allowed projects | container |  | scope_denied | 0 |  | time="2026-11-07T20:49:15Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
| lxd6 | a claim naming another instance | container |  | instance_not_found | 0 |  | time="2026-11-07T20:50:31Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
| lxd6 | an identity lacking can_access_files | container |  | lxd_forbidden | 0 |  | time="2026-11-07T20:50:50Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
| lxd6 | outside the allowed projects | container |  | scope_denied | 0 |  | time="2026-11-07T20:50:13Z" level=error msg="Agent crashed" error="failed to receive attestation response: rpc error: code = PermissionDenied desc = nodeattestor(lxd_iid): attestation failed" |
