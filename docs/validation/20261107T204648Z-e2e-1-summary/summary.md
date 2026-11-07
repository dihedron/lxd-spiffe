# 20261107T204648Z-e2e-1-summary

SPIRE 1.15.5, plugins 0.2.0-SNAPSHOT-7744adc, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-priv | privileged container | file_pull |  | 41 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/1b2da6a2-692e-4e77-835d-0241e7a20a89 |  |
| lxd5 | e2e-unpriv | container | file_pull |  | 43 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/95075d29-801a-42f6-a67c-20950ec001cc |  |
| lxd5 | e2e-vm | vm | file_pull |  | 92 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/4473ade0-dd03-4a70-a3d6-04b79b940e17 |  |
| lxd6 | e2e-priv | privileged container | file_pull |  | 41 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/a6c7c2f2-c3b4-4882-87d0-e00c87e85489 |  |
| lxd6 | e2e-unpriv | container | file_pull |  | 41 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/29231aef-2706-4a0b-bc2f-44fd5e8b40b3 |  |
| lxd6 | e2e-vm | vm | file_pull |  | 82 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/fd877634-9d63-4d3f-9531-eb5ea7b08a52 |  |
