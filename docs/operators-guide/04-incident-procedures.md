# Incident procedures

## The server plugin crashed

*Signs*: every attestation fails; SPIRE Server logs `plugin process exited` for the `lxd_iid` node attestor, then errors such as `connection refused` or `Nodeattestor(lxd_iid): connection error` for each attempt; no `attestation succeeded` or `attestation refused` lines from the plugin.

SPIRE Server (1.15.5, measured) keeps running when a plugin process dies and never restarts it. Agents keep their SVIDs until they expire, then cannot attest again.

1. Restart SPIRE Server: `systemctl restart spire-server`. It starts the plugin again.
2. Check that `attestation succeeded` lines resume.
3. Look for the cause in SPIRE Server's log just before the exit (a panic's trace, an out-of-memory kill in the kernel log), and report it with the log.

## An LXD server is unavailable

*Signs*: attestations through one `server_id` fail with `lxd_unavailable` (or `lxd_forbidden`, when LXD no longer trusts SPIRE's identity); the log has `refusing to attest through an LXD server` with that server, or errors with connection failures, timeouts or `the circuit breaker is open after LXD failures`.

1. Check the server from SPIRE Server's host: `curl -sk https://<url>/1.0` must answer.
2. Read the `error` of the refusals: a fingerprint that does not match the pin (the server's certificate changed), `the server does not trust this client` (SPIRE's identity was removed or its certificate or token expired), missing API extensions (`extension_missing`, after a downgrade or a different server behind the address), or a transport failure.
3. Fix the cause. The plugin retries the server by itself: the circuit breaker lets a trial attestation through every 5 to 60 seconds, and the agents retry.

While one configured server is unreachable, attestations whose claim it might also match fail too (the plugin cannot show the match is unique without it). Give each server its own projects to limit the effect, or remove the entry until the server is back.

## The LXD daemon crashed

*Signs*: a burst of `not_running`, then many attestations from one LXD server at once; LXD restarted every container on that server, and its VMs kept running. Lifecycle operations that were in flight (starts, stops) are lost: an instance can stay stopped.

1. Check with `lxc list` that every instance that should run does; start those that do not.
2. Expect every container's agent to attest again within minutes. If many containers share one NAT address and SPIRE Server's per-address rate limit is on, they queue at one attestation a second; agents whose `attestation_timeout` is shorter than their wait fail and retry with a backoff. Watch for `rate_limited` from the plugin's own limits too: raise `limits.rate_per_sec` and `limits.max_inflight` if one server's containers exceed them.

## A suspected forged attestation

*Signs*: `proof_mismatch` (a proof with the wrong content, owner or mode, or no proof file), `instance_ambiguous` (two instances with one UUID, or a claim matching two instances), `scope_denied` from an unexpected source, or an agent identity for an instance that should not have one.

1. Keep the evidence: SPIRE Server's log lines with their `correlation_id`, and LXD's lifecycle events for the instance and for the SPIRE identity around that time.
2. For `instance_ambiguous`: the `error` lists the instances found (`<server_id>/<project>/<name>`). Find out where the twin came from (`lxc info`, LXD's events: an `instance-created` from an import). Delete or re-identify the instance that should not exist (a copy gets a new UUID; an import keeps the original's).
3. For `proof_mismatch` from an instance that should attest: check its proof directory (`/run/spire/lxd`, root-owned, mode 0700, on a tmpfs), its SPIRE Agent running as root, and the same `proof_dir` on both sides. From an instance that should not attest, or with a claim naming another instance: someone is trying; find who controls the claiming instance.
4. If an identity was issued to the wrong instance, evict the agent (`spire-server agent evict`), delete the workload entries it could match, and find how: a shared disk at the proof directory (the server refuses those it sees, but check the instance's devices), a copy carrying the agent's data (*Routine procedures*), or grants that let an editor change selectors your entries rely on.

## SPIRE's LXD credential is stolen

The holder can do what SPIRE's group can: read every instance of the allowed projects and, with `file_pull`, read and write any file in the granted instances; with `config_push`, change their configuration.

1. **Revoke it at once** on every LXD server: delete the identity (`lxc auth identity delete tls/spire` or `bearer/spire`); attestation stops until a new one exists.
2. Create a new credential and identity, add it to the groups, update the plugin's files (*Routine procedures*, *Rotating the SPIRE credential*); with a bearer token, issuing a new token already revokes the old one.
3. Review LXD's lifecycle events requested by the old identity: file writes (`file_pull` never writes), configuration changes other than `user.spire.challenge.*`, anything outside the allowed projects.
4. Treat instances whose files or configuration were changed as compromised; evict their agents after cleaning them.
5. Find how it leaked: the key file's permissions, backups of SPIRE Server's host, the token's renewal job.

## A failed cluster evacuation

*Signs*: `lxc cluster evacuate` stopped with `Failed deleting instance on source member: Error deleting storage volume: Failed running: zfs destroy -r <pool>/containers/<name>: exit status 1 (cannot unmount '...': pool or dataset is busy)`. The instance LXD was moving is stopped on its new member, its old volume stays on the source member, and the instances after it were not touched. On LXD 6.9 the member stays `Evacuated` without a database role and `lxc cluster restore` fails with `Cannot create volume, already exists on migration target storage`; on 5.21.8 the member shows `Online` and moving the instance back fails with the same message. The usual cause: a files API read of the container during its evacuation, such as a `file_pull` attestation.

The lab tested this repair on 5.21.8 and 6.9, with ZFS. LXD has no command for step 3, which edits its database by hand, an unsupported operation: **take the backup first**.

1. Make sure nothing reads the instance's files API (stop the SPIRE Agents of the member's containers).
2. Find the leftover: `lxc storage volume list <pool>` shows the instance's name twice, once per member; the leftover is on the evacuated member.
3. `lxc storage volume delete` refuses it (`Storage volumes of type "container" cannot be deleted directly`). On that member, destroy the dataset with the `zfs` of the LXD snap, the version `lxc info` reports as `storage_version`: `/snap/lxd/current/zfs-<x.y>/bin/zfs destroy -r <pool>/containers/<name>`, with `LD_LIBRARY_PATH` set to that directory's `lib` and the snap's `lib/*`. Then **back up the database** (`lxd sql global .dump > backup.sql`) and delete the volume's row, only it:

   ```bash
   lxd sql global "SELECT id,name FROM nodes"                                   # the member's id
   lxd sql global "SELECT id,node_id FROM storage_volumes WHERE name='<name>'"
   lxd sql global "DELETE FROM storage_volumes WHERE id=<id>"                   # the row whose node_id is the evacuated member
   ```

4. On 6.9: `lxc cluster restore <member> --force` (it moves the instance back, stopped, and the member's database role returns). On 5.21.8: `lxc move <name> --target <member>`. Then `lxc start <name>`.
5. Check `lxc list`, `lxc storage volume list <pool>` (one volume per instance) and `lxc cluster list` (three database voters).

With `config_push`, a failed evacuation (`Instance is busy running a "update" operation`) leaves nothing behind: run `lxc cluster evacuate` again, or `lxc cluster restore <member> --force` to give up; then start any instance left stopped, and for one found on another member: `lxc stop --force <name>`, `lxc move <name> --target <member>`, `lxc start <name>`.

## A member's image cache is empty

*Signs*, after a member was powered off: launches from remote images fail on that member, or hang. LXD wrote empty files under `/var/snap/lxd/common/lxd/cache` (seen on 5.21.8). Remove the empty files and restart LXD on that member (`systemctl restart snap.lxd.daemon`, which, like `snap restart lxd`, stops every instance there).
