# Code quality and security testing

An attestor is only as trustworthy as the evidence that it does what it claims. No single kind of test gives that evidence: unit tests cannot show what LXD really does, a lab cannot try every malformed input, and neither finds a race that shows once in a million attestations. The project therefore layers its checks, each one catching what the others miss. This chapter describes the layers; *Validation* gives the lab's results in detail.

## Specification first

Every feature and every non-trivial fix starts as a change to a specification in the repository: the attestor's (`lxd-spire-plugins.md`), the validation probe's, the lab's and the stress sessions'. Requirements carry identifiers (`FR-S3`, `SEC-09`, `T7`), the threat model is part of the specification, and each change is recorded in the specification's change log with its reason. Tests are named after the requirement they check (`TestFRS15HungServer`, `TestSEC09NoLockoutByClaim`), so that a requirement and its evidence can be found from each other.

## Tests written first

A fix begins with a test that fails for the reason the fix addresses, and the test is kept. The code has over 440 test functions. Three choices make them meaningful rather than numerous:

- **Recorded behaviour, not assumed behaviour.** The fake LXD API that the plugins are tested against replays fixtures recorded from real servers (over 3,000 files from LXD 5.21.8, 6.9 and 6.10.0), and may not define behaviour that no fixture shows. A test against the fake is a test against what LXD was seen to do.
- **The plugins as SPIRE runs them.** The integration tests serve the server and agent plugins over gRPC with the plugin SDK's test harness, the way SPIRE loads them, and drive whole attestations: claim, challenge, proof, re-read, identity.
- **Refusals tested like successes.** The tests produce the refusals as well as the successes, check the reason code the server logs, and check that the agent sees only the same uniform error (`SEC-10`).

## Fuzzing

Everything the server reads from an agent is untrusted, and so is much of what it reads from a guest. The parsers of that input are fuzzed: the protocol's three decoders (`AttestRequest`, `Challenge`, `ChallengeResponse`), the claim's sources (devLXD meta-data and instance names), the uid and gid map parsers behind the privilege checks, the selector value rule, and the evidence sanitizer. A fuzz target also checks that the proof's encoding is injective: no two different claims give the same proof input.

## Static analysis

`make lint` runs before every build, locally and in CI: `gofmt`, `go vet` (with and without the lab's build tag) and `golangci-lint` with its standard linters plus `gosec`. A finding is fixed, or silenced at its line with the reason (`//nolint:gosec // G703: the operator names the profile file`); reviewed false positives of SonarQube Cloud are kept in `sonar-project.properties` with theirs. GitHub's CodeQL analyses every pull request and runs weekly.

## Gates on every change

A change reaches `main` through a pull request whose checks all pass:

- build, lint and tests, with coverage uploaded to Codecov (patch and project coverage);
- SonarQube Cloud's quality gate on new code: coverage of at least 80 %, at most 3 % duplicated lines, a security rating of A, every security hotspot reviewed. Changed lines count as new code, so touching an untested line means testing it;
- GitHub's dependency review of any new dependency;
- secret scans with gitleaks and TruffleHog.

Dependabot proposes updates of the Go modules and of the CI actions weekly, and the whole CI also runs nightly, so that a new vulnerability or a broken dependency shows without a change.

## Secrets and dependencies

Before every commit, locally: gitleaks and TruffleHog over what is committed, and `govulncheck` over the module. A vulnerability that `govulncheck` reports is either fixed or accepted with its reason; the accepted ones are listed (today, five advisories of the LXD server code with no fix, in packages the plugins do not use: they use only LXD's client SDK). Any other finding blocks the commit.

## Concurrency and resources

Go's race detector is run over the tests of the packages a change touches before it is committed; CI does not run it. For the long soaks, the lab can deploy a server plugin built with the race detector (`LAB_SERVER_PLUGIN_DEBUG=yes`); a data race it reports fails the session. That lab build also serves Go's profiler on a loopback address, which release builds do not contain, so that the soak can sample the plugin's goroutines and heap every few minutes, take CPU and heap profiles, and fail when a long-lived plugin process grows (INV-10).

## The lab

Statements about LXD that its documentation does not settle were answered by recorded experiments before the code that depends on them was written (the validation gates), and the end-to-end, stress, chaos and soak sessions run the real plugins against real LXD servers and SPIRE (*Validation*). Their evidence is committed only from clean builds, so that every result names the exact code it came from, and it passes the evidence sanitizer, which removes credentials and the values of user-defined keys before anything is written.

## The split soak

The longest session (`ST-8`, Nov 8, 2026) ran 300 real SPIRE Agents, 150 on a standalone LXD 5.21.8 server and 150 on a standalone 6.10.0 one, against one SPIRE Server with the race-detector build of the server plugin. It lasted five and a half hours, with a fault every 75 seconds. For three hours the faults were on LXD's side only (instances stopped, agents' data wiped, concurrent writers, network partitions, the LXD daemon killed or frozen with `SIGSTOP`), first in `file_pull` and then in `config_push`, so that one plugin process lived long enough to show a leak. Then for two and a half hours every fault ran, SPIRE Server restarts and plugin kills included, with the proof mode switched halfway. Faults hit one server at a time, so that the other server's agents show whether a fault stays where it happens.

Over 187 faults and 35,083 successful attestations, the session's invariants held. No identity was issued without a valid proof or with the wrong proof mode. Every agent was attested again after the last fault, and no challenge key was left behind. While one server was cut off or frozen, the other's agents kept attesting (`INV-9`). The race detector reported no data race. No plugin process grew: goroutines stayed between 16 and 37, and the heap in use between 2 and 5 MiB, in processes that lived up to 87 minutes (`INV-10`). The evidence is in `docs/validation/20261108T055009Z-st-8-summary`.

The first full run failed, on two bugs of the harness, not of the plugins. The fleet's service manager (OpenRC's `supervise-daemon`) stops restarting a service after a few exits in a row, so two agents refused through SPIRE outages stayed down. And a single read of the challenge keys at the end caught one of an attestation still in flight. Both were fixed and the session was run again from the start (`docs/validation/20261107T234746Z-st-8-summary`).

The session also measured these points:

- **A frozen or unreachable server slows the others, briefly.** Every attestation queries every configured server, to find the instance and to check that its UUID is unique (`FR-S3`, `FR-S18`). Until the first calls to a dead server time out and open its circuit breaker, attestations on the healthy server wait for that timeout. About 450 of them took 10 to 20 seconds, around ten per fault, against a median of 250 ms.
- **A NAT reset leaves an agent waiting for two hours.** Sixteen agents that started while their LXD daemon was down hung after it came back. Restarting, LXD rebuilt the NAT of its bridge and dropped the state of their connections to SPIRE Server, and SPIRE Agent waits for the server's answer without a deadline. The connections ended only when TCP keepalive gave up, after the kernel's default of 7,200 seconds, and the agents then attested at once. Guests behind LXD's NAT can lower `net.ipv4.tcp_keepalive_time` (*Operator's Guide*, *Incident procedures*).
- **An agent plugin that sees no devLXD offers only `file_pull`.** While the LXD daemon was frozen, agents that started could not reach devLXD and offered `file_pull` alone, which a server in `config_push` refuses (`policy_denied`, 485 times). This is intended: the agent offers only what it can prove.
- **A quarter of the plugin's CPU time goes to TLS handshakes with LXD.** The server plugin used about 13 % of one core, two thirds of it in the race detector. A quarter of its time was TLS handshakes, in every profile, because LXD's client library disables HTTP keep-alive, so every request to LXD opens a new connection. A server entry can now reuse connections (`keep_alive`, `FR-S20`); it stays off by default, as the library chooses.
- **SPIRE does not restart a killed plugin.** This was seen again in all six kills; SPIRE Server had to be restarted.

## Reviews

Two reviews were checked against the code, finding by finding, before anything was changed: an external security and architecture review and a code audit. Each finding got a verdict (real, real but needing no code, or false), and only real findings became changes, each with its test. Of the external review's nineteen findings, five were false (among them the one rated critical), six needed no code and eight were fixed; the audit found, among others, that a reconfiguration through `plugin_data_file` reset the plugin's limits. The lab runs that verified those fixes found further bugs no review had seen: a log level that hung the probe, a server that hangs instead of refusing, a test harness whose plugin kill never killed the plugin.

## Releases

Releases are built by GoReleaser from a tag on `main` with CI green: static binaries (`CGO_ENABLED=0`, `-trimpath`), Debian and RPM packages, an SBOM for every archive and package, and a checksums file signed with the project's key, which is in the repository (`packaging/signing-key.asc`). Each release's signature and checksums are verified after publication, and its notes say what an upgrade changes.

## What these checks do not cover

- The agent plugin is not run under the race detector in the lab: the fleet's guests use musl, and the race detector needs glibc. Its unit and integration tests run with it.
- The specification asks for a CI check of the dependency allow-list (`NFR-03`); it is not automated yet, and the allow-list is checked by review.
- The limits of *Validation* apply: no storage driver but ZFS, no live migration, and a sustained rate above about 12 attestations per second not exercised.
