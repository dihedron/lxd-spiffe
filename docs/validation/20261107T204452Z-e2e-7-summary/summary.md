# 20261107T204452Z-e2e-7-summary

SPIRE 1.15.5, plugins 0.2.0-SNAPSHOT-7744adc, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-7-attacker | container |  | instance_not_found | 0 |  |  |
| lxd5 | e2e-7-victim | container | file_pull |  | 16 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/e3113c75-df1e-453b-910c-bab4357d2224 |  |
| lxd6 | e2e-7-attacker | container |  | instance_not_found | 0 |  |  |
| lxd6 | e2e-7-victim | container | file_pull |  | 15 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/b1076c32-8d95-4803-b02e-c9f45a35913e |  |
