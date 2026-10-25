# 20261025T135204Z-e2e-4-tmpfs-summary

SPIRE 1.15.5, plugins 0.1.0-SNAPSHOT-b054f04, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-tmpfs-copy | container |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b527a70d-7a6e-4691-b609-3cefe961c34b | lxc copy of the running instance |
| lxd5 | e2e-tmpfs-copy | container | file_pull |  | 10 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b527a70d-7a6e-4691-b609-3cefe961c34b |  |
| lxd5 | e2e-tmpfs-vm-copy | vm |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/a9de38ef-39a3-4e7c-8765-84299c924a27 | lxc copy of a stateless snapshot |
| lxd5 | e2e-tmpfs-vm-copy | vm | file_pull |  | 42 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/a9de38ef-39a3-4e7c-8765-84299c924a27 |  |
| lxd5 | e2e-tmpfs-vm | vm |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b349b4f4-6697-43d5-8a60-95d23ac89768 | kept its SVID, no attestation |
| lxd5 | e2e-tmpfs-vm | vm | file_pull |  | 66 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b349b4f4-6697-43d5-8a60-95d23ac89768 |  |
| lxd5 | e2e-tmpfs-vm | vm |  |  | 42 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b349b4f4-6697-43d5-8a60-95d23ac89768 | attested again as the same instance |
| lxd5 | e2e-tmpfs | container |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/5d280373-9bee-4490-8280-94bbcfa76af1 | kept its SVID, no attestation |
| lxd5 | e2e-tmpfs | container | file_pull |  | 17 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/5d280373-9bee-4490-8280-94bbcfa76af1 |  |
| lxd5 | e2e-tmpfs | container |  |  | 10 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/5d280373-9bee-4490-8280-94bbcfa76af1 | attested again as the same instance |
| lxd6 | e2e-tmpfs-copy | container |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/31dc5ec2-097b-43db-bf76-d43fbb42b500 | lxc copy of the running instance |
| lxd6 | e2e-tmpfs-copy | container | file_pull |  | 9 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/31dc5ec2-097b-43db-bf76-d43fbb42b500 |  |
| lxd6 | e2e-tmpfs-vm-copy | vm |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/b52aa49a-f153-4143-b2ed-7c107abceaf9 | lxc copy of a stateless snapshot |
| lxd6 | e2e-tmpfs-vm-copy | vm | file_pull |  | 42 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/b52aa49a-f153-4143-b2ed-7c107abceaf9 |  |
| lxd6 | e2e-tmpfs-vm | vm |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bab7b48a-7521-4243-ac57-72989d10bc2c | kept its SVID, no attestation |
| lxd6 | e2e-tmpfs-vm | vm | file_pull |  | 60 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bab7b48a-7521-4243-ac57-72989d10bc2c |  |
| lxd6 | e2e-tmpfs-vm | vm |  |  | 42 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bab7b48a-7521-4243-ac57-72989d10bc2c | attested again as the same instance |
| lxd6 | e2e-tmpfs | container |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/d39804fa-ae9e-4e59-bbfd-c504d1af5984 | kept its SVID, no attestation |
| lxd6 | e2e-tmpfs | container | file_pull |  | 17 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/d39804fa-ae9e-4e59-bbfd-c504d1af5984 |  |
| lxd6 | e2e-tmpfs | container |  |  | 11 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/d39804fa-ae9e-4e59-bbfd-c504d1af5984 | attested again as the same instance |
