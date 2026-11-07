# Ponytail Audit Report

What this repo does: Provides SPIRE (a system that gives workloads cryptographic identities) NodeAttestor plugins for LXD (a Linux container and virtual machine manager), plus `lxd-probe`, a lab tool that records LXD API behavior. An agent inside an LXD guest sends an identity claim to the SPIRE server, which verifies the guest against the LXD daemon and proves the caller is inside that guest via a secret file pull (`file_pull`) or a live device-socket config event (`config_push`). Assumed load: a SPIRE server attesting fleets of hundreds of LXD guests across several LXD clusters, with bursts of dozens of concurrent re-attestations and periodic config reloads.

### Must fix

1. **Config reload resets active concurrency limit and per-guest cooldowns** (`internal/plugin/server/lxdiid/plugin.go:L32-L62`, `internal/plugin/server/lxdiid/config.go:L213-L214`)
   - **What this is:** When SPIRE reloads configuration, the server plugin keeps its active challenge tracker (`inflight`) alive across the reload, but creates a brand-new concurrency limiter (`s.limiter`, which caps parallel LXD checks and enforces a cooldown timer after failed attempts).
   - **Problem:** If 32 attestations (`max_inflight = 32`) are running when `Configure` runs, those 32 release their slots on the old limiter while 32 new attestations enter the new limiter, allowing 64 concurrent attestations (`2 * max_inflight`); also, any guest currently locked out by the 5-second failure cooldown (`claim_cooldown`) has its failure count wiped and can retry immediately.
   - **Fix:** Move `limiter` onto `Plugin` (`internal/plugin/server/lxdiid/plugin.go:L24-L34`) alongside `inflight`, and add a method on `limiter` (`internal/plugin/server/lxdiid/limiter.go:L23-L34`) to update `maxInflight`, `cooldown`, and `maxClaims` in place under its mutex.
   - **If we skip it:** A config reload during an attestation storm doubles LXD API load and clears brute-force cooldowns.

2. **Startup CPU/memory profiler crashes on bad path and never flushes in plugin mode** (`cmd/lxd-agent-plugin/init.go:L116-L152`, `cmd/lxd-agent-plugin/main.go:L18-L25`, `cmd/lxd-server-plugin/init.go:L116-L152`, `cmd/lxd-server-plugin/main.go:L18-L25`, `cmd/lxd-probe/init.go:L116-L152`)
   - **What this is:** Each binary's `init()` function checks environment variables for CPU and memory profiling (recording where the program spends time or memory) and registers a `cleanup()` function to flush those files on exit.
   - **Problem:** First, in both plugin `main()` functions, when SPIRE launches the plugin (`len(os.Args) == 1`), `main()` returns on line 23 before `defer cleanup()` on line 25, so profile files stay empty and unclosed. Second, if `os.Create` fails (for example, `LXD_SERVER_PLUGIN_CPU_PROFILE=/nonexistent/cpu.prof`), `f` is `nil`, yet line 125 still calls `pprof.StartCPUProfile(f)`, causing an immediate crash when flushing.
   - **Fix:** Move `defer cleanup()` to the top of `main()` before the `len(os.Args) == 1` branch, and guard `pprof.StartCPUProfile(f)` and `cleanup` registration behind an `else` branch when `err == nil`.
   - **If we skip it:** Profiling a live SPIRE plugin produces 0-byte profile files, and a typo in the profile path crashes the binary on startup.

### Should fix

3. **`lxd-probe server project show --redact-names` labels the project as an instance** (`internal/probe/server/project.go:L33-L36`, `internal/probe/server/session.go:L56-L81`)
   - **What this is:** `newSession` accepts variadic `names ...string` and registers every passed name with the sanitizer (the helper that replaces real host/guest names with fake ones like `instance-1`) as `evidence.KindInstance`.
   - **Problem:** `ProjectShow.Execute` passes `cmd.Args.Name` (a project name) into `newSession(cmd.ServerCommand, cmd.stdout, "server project show", name)`, so running `lxd-probe server project show prod --redact-names` rewrites the project name `prod` to `instance-1` instead of `project-1` in recorded evidence and fixtures.
   - **Fix:** Pass no trailing name to `newSession` in `ProjectShow.Execute` (`internal/probe/server/project.go:L34`) and call `s.run.Sanitizer.RedactName(evidence.KindProject, name)` when `cmd.RedactNames` is set.
   - **If we skip it:** Sanitized lab evidence and fixtures from `server project show` corrupt project names into `instance-N` aliases.

4. **`metadata.PrintFull` ignores its output writer and prints `Git Time` twice** (`pkg/metadata/metadata.go:L107-L131`)
   - **What this is:** `PrintFull(writer io.Writer)` prints full build metadata for the `version -v` command and accepts an `io.Writer` (a destination stream for text output).
   - **Problem:** Every line inside `PrintFull` calls `fmt.Printf` (which always writes to standard output) instead of `fmt.Fprintf(writer, ...)`, so passing a buffer in tests or redirecting output via `writer` writes nothing to `writer`; additionally, lines 124 and 128 print the exact same `Git Time` line twice.
   - **Fix:** Replace `fmt.Printf` with `fmt.Fprintf(writer, ...)` in `PrintFull` (`pkg/metadata/metadata.go:L107-L131`) and delete the duplicate line 128.
   - **If we skip it:** Callers cannot capture `PrintFull` output through its `writer` parameter, and `version -v` outputs duplicate `Git Time` lines.

5. **`.env` loader in `init()` logs before logger setup and logs success after failure** (`cmd/lxd-agent-plugin/init.go:L31-L37`, `cmd/lxd-server-plugin/init.go:L31-L37`, `cmd/lxd-probe/init.go:L31-L37`)
   - **What this is:** At startup, `init()` loads an optional `.env` file specified by `LXD_SPIFFE_DOTENV` before configuring the structured logger (`slog`).
   - **Problem:** If `LXD_SPIFFE_DOTENV=/missing/.env` is set, `godotenv.Load(dotenv)` fails on line 33, logs `"error loading .env file"`, and then immediately executes line 36 to log `"successfully loaded .env file"`; because this runs before `slog.SetDefault` on line 102, both messages write to default `stderr` even when logging is set to `none`.
   - **Fix:** Put line 36 in an `else` block, or defer logging the `.env` result until after `slog.SetDefault` on line 102.
   - **If we skip it:** Missing `LXD_SPIFFE_DOTENV` files produce contradictory error-plus-success log lines on `stderr` before log settings take effect.

6. **Interactive rehearsal leaks a background reader when timed out** (`internal/probe/rehearse/rehearse.go:L160-L188`)
   - **What this is:** `ReadAnswer` spawns a background goroutine (a lightweight concurrent thread) that blocks on `os.Stdin.Read` waiting for the operator to paste a challenge nonce.
   - **Problem:** If `ctx` expires before the operator presses Enter, `ReadAnswer` returns `ctx.Err()`, but the spawned goroutine remains blocked on `r.Read` until the process exits or another line is typed, consuming the next line of standard input if called again in the same process.
   - **Fix:** No code change needed for single-command CLI runs, or close the input descriptor on context cancel in tests.
   - **If we skip it:** Harmless in one-shot CLI invocations, but any in-process caller invoking `ReadAnswer` twice after a timeout will have its next `stdin` line stolen by the leaked goroutine.

### Nice to have

7. **Three identical 170-line `init.go` files across `cmd/`** (`cmd/lxd-agent-plugin/init.go:L1-L170`, `cmd/lxd-server-plugin/init.go:L1-L170`, `cmd/lxd-probe/init.go:L1-L170`)
   - **What this is:** All three binaries copy-paste the exact same 170-line `init.go` for `.env` loading, `slog` setup, and CPU/memory profiling.
   - **Problem:** Every bug in startup initialization (such as findings 2 and 5 above) exists in three places and must be fixed three times.
   - **Fix:** Move the shared startup and `Cleanup()` logic into an internal package (for example, `internal/runtime`) and call it from each `cmd/*/init.go` (-310 lines).
   - **If we skip it:** Future startup fixes will continue to drift across the three binaries.

8. **External dependency `github.com/golang-jwt/jwt/v5` used for one unverified base64 decode** (`internal/probe/server/bearer.go:L12-L120`, `internal/lxd/client.go:L194-L227`, `go.mod:L7`)
   - **What this is:** `internal/lxd/client.go` (`L196-L227`) already decodes JWT (JSON Web Token) headers and claims without external libraries using standard `encoding/base64` and `encoding/json`, whereas `internal/probe/server/bearer.go` (`L113-L115`) pulls in `github.com/golang-jwt/jwt/v5` solely to call `ParseUnverified`.
   - **Problem:** Calling `jwt.NewParser().ParseUnverified` in `bearer.go` adds a third-party cryptographic dependency to `go.mod` just to split a string on `.` and base64url-decode the first two segments.
   - **Fix:** Reuse the stdlib segment decoder from `internal/lxd/client.go` (`L221-L227`) in `decodeToken` (`internal/probe/server/bearer.go:L113-L143`) and remove `github.com/golang-jwt/jwt/v5` from `go.mod` (-1 dependency, -15 lines).
   - **If we skip it:** One extra external module to track for security advisories (`govulncheck`).

9. **Duplicate `/proc/<pid>/uid_map` parser in `internal/probe/guest` and dead `probe.NotImplemented` helper** (`internal/probe/guest/preflight.go:L243-L272`, `internal/policy/idmap.go:L64-L87`, `internal/probe/options.go:L109-L113`)
   - **What this is:** `guest/preflight.go` (`L251`) defines its own `parseIDMap` function that duplicates `policy.ParseProcIDMap` (`internal/policy/idmap.go:L66`, which `probe/util/idmap.go` already uses), and `probe.NotImplemented` (`internal/probe/options.go:L111`) is a scaffolding helper from early development with no production callers left.
   - **Problem:** Maintaining two parsers for `/proc/self/uid_map` (only one of which is fuzz-tested via `FuzzParseProcIDMap` in `internal/policy/fuzz_test.go:L38`) and keeping dead scaffolding code adds noise.
   - **Fix:** Call `policy.ParseProcIDMap` (`internal/policy/idmap.go:L66`) from `guest/preflight.go` (`L79`), and delete `parseIDMap`, `probe.NotImplemented` (`internal/probe/options.go:L109-L113`), and `TestNotImplemented` (-45 lines). Also remove the duplicate `_APPLICATION_VERSION` line in `Makefile` (`L5-L10`, -1 line).
   - **If we skip it:** ~45 lines of duplicate/dead code remain in the tree.

Verdict: Well-structured, thoroughly tested codebase with strong security boundaries; fix the `Configure` limiter reset (#1) and plugin profiling/init bugs (#2) first.
Lean: -370 lines, -1 dependency possible.
Not checked: Live LXD + SPIRE lab integration tests under `test/lab/acceptance` (require a running LXD daemon and root privileges).
