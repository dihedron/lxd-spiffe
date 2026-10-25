# 20261025T075006Z-e2e-4-summary

SPIRE 1.15.5, plugins 0.0.0-SNAPSHOT-1f5ce01, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/5ca21ed3-4c84-41f5-ba08-611f4daafad0 |  |
| lxd5 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/57ab7f20-23df-4c0a-80a5-d6247403c511 | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 284860120890682530522371760438926087223, after 284860120890682530522371760438926087223; the copy's agent log: time="2026-10-25T07:48:09Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/57ab7f20-23df-4c0a-80a5-d6247403c511" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd5 | e2e-re | container | file_pull |  | 16 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/57ab7f20-23df-4c0a-80a5-d6247403c511 |  |
| lxd5 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/57ab7f20-23df-4c0a-80a5-d6247403c511 | attested again, new serial |
| lxd6 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/09b6a6d0-6e41-4aad-b581-fb3bd0a79216 |  |
| lxd6 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/59ab394f-1d5b-4fb7-bf45-e5e08ee60043 | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 66714533396908052475852642841170256102, after 66714533396908052475852642841170256102; the copy's agent log: time="2026-10-25T07:48:09Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/59ab394f-1d5b-4fb7-bf45-e5e08ee60043" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd6 | e2e-re | container | file_pull |  | 15 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/59ab394f-1d5b-4fb7-bf45-e5e08ee60043 |  |
| lxd6 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/59ab394f-1d5b-4fb7-bf45-e5e08ee60043 | attested again, new serial |
