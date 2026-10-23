# 20261023T125059Z-e2e-4-summary

SPIRE 1.15.5, plugins 0.0.0-SNAPSHOT-e2f13e2, c5 LXD 5.21.8, c6 LXD 6.9, lxd5 LXD 5.21.8, lxd6 LXD 6.9

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/9d426139-86ef-46d5-87cc-178ffe8dfb62 |  |
| lxd5 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/309fd075-4e45-4559-89ab-5679462bb181 | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 159105258535515176819901683462780621768, after 159105258535515176819901683462780621768; the copy's agent log: time="2026-10-23T12:48:57Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/309fd075-4e45-4559-89ab-5679462bb181" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd5 | e2e-re | container | file_pull |  | 15 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/309fd075-4e45-4559-89ab-5679462bb181 |  |
| lxd5 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/309fd075-4e45-4559-89ab-5679462bb181 | attested again, new serial |
| lxd6 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/1c05cbbc-e266-4700-ba1f-e098e799d17f |  |
| lxd6 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bf5084c2-a5b5-4090-8c7c-0a3f28ad7668 | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 144761967555880467836621085209791547674, after 144761967555880467836621085209791547674; the copy's agent log: time="2026-10-23T12:48:57Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bf5084c2-a5b5-4090-8c7c-0a3f28ad7668" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd6 | e2e-re | container | file_pull |  | 15 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bf5084c2-a5b5-4090-8c7c-0a3f28ad7668 |  |
| lxd6 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/bf5084c2-a5b5-4090-8c7c-0a3f28ad7668 | attested again, new serial |
