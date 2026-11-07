# 20261107T204903Z-e2e-2-summary

SPIRE 1.15.5, plugins 0.2.0-SNAPSHOT-7744adc, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-priv | privileged container | config_push |  | 21 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/c6f28c43-72e8-420c-8d25-1650a409af03 |  |
| lxd5 | e2e-unpriv | container | config_push |  | 17 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/195488f7-5d35-4cd5-96df-04ac740bd41e |  |
| lxd5 | e2e-vm | vm | config_push |  | 67 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/673954d8-3bff-4ddf-ad25-58cae8506b29 |  |
| lxd6 | e2e-priv | privileged container | config_push |  | 18 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/52434502-2b6f-44f0-88d4-6c3253f48302 |  |
| lxd6 | e2e-unpriv | container | config_push |  | 13 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/b202f8ad-75de-46f0-8fee-62e8c40ac5dd |  |
| lxd6 | e2e-vm | vm | config_push |  | 54 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/48203e12-ced6-49c4-949e-79b7c0fcbc09 |  |
