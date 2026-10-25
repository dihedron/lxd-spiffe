# 20261025T134749Z-e2e-4-summary

SPIRE 1.15.5, plugins 0.1.0-SNAPSHOT-b054f04, c5 LXD 5.21.8, c6 LXD 6.10.0, lxd5 LXD 5.21.8, lxd6 LXD 6.10.0

| server | guest | kind | mode | refused with | seconds | SPIFFE ID | notes |
|---|---|---|---|---|---|---|---|
| lxd5 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/a3daaac7-eafc-4cd7-9da2-580af43f43d7 |  |
| lxd5 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b6405e59-634c-4948-80a6-72e26ab6897a | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 235508449207641867270087957721897515534, after 235508449207641867270087957721897515534; the copy's agent log: time="2026-10-25T13:45:56Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b6405e59-634c-4948-80a6-72e26ab6897a" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd5 | e2e-re | container | file_pull |  | 16 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b6405e59-634c-4948-80a6-72e26ab6897a |  |
| lxd5 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd5/b6405e59-634c-4948-80a6-72e26ab6897a | attested again, new serial |
| lxd6 | e2e-re-copy | container | file_pull |  | 7 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/3a1fd888-682d-4005-9187-a6c4e177b9c4 |  |
| lxd6 | e2e-re-kept |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/ef333622-0c96-40aa-a313-9b8c75784124 | copy attests as itself: false; the copy's workload gets the original's workload SVID spiffe://lxd.lab/e2e-clone: true; original's agent serial before 307880565431294173616485717778083439219, after 307880565431294173616485717778083439219; the copy's agent log: time="2026-10-25T13:45:56Z" level=info msg="SVID loaded" spiffe_id="spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/ef333622-0c96-40aa-a313-9b8c75784124" subsystem_name=attestor trust_domain_id="spiffe://lxd.lab" |
| lxd6 | e2e-re | container | file_pull |  | 16 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/ef333622-0c96-40aa-a313-9b8c75784124 |  |
| lxd6 | e2e-re |  |  |  | 0 | spiffe://lxd.lab/spire/agent/lxd_iid/lxd6/ef333622-0c96-40aa-a313-9b8c75784124 | attested again, new serial |
