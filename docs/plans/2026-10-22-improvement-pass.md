# git-auto-sync: Improvement Pass (5 agents)

Date: 2026-10-22
Status: Draft, awaiting approval

## Why

The daemon has run for ~7 months on three repos (`dotfiles`, `notes`, `toolbox`). The log (`~/git-auto-sync-daemon.err.log`, 15 MB, never rotated) shows where it hurts:

| Signal from the log | Count | Root cause |
|---|---|---|
| `synced` events | 43,545 | Emitted on every poll even when nothing happened. Floods the app timeline (500-event cap) |
| `rebase conflict` on dotfiles | 253 | After aborting a conflicted rebase the daemon retries on every poll/fs-event and conflicts again, notifying each time |
| `git fetch` failures (host unreachable, 502/504, TLS) | ~1,100 | Network blips are treated as hard errors (red state, notifications) instead of "offline, will retry" |
| `Connection timeout after 1064984 ms` | several | No timeout on git commands. One fetch hung a watcher for 17 minutes |
| `exit status 69: git config user.email` | 81 | Xcode license not accepted. Shown as a raw git error with no explanation |

Other findings:

- **Tests are red.** `go test ./...` fails `Test_NewFile`, `Test_OneFileChange`, `Test_MultipleFileChange`. The tests still expect the old porcelain-style commit message.
- **CI is dead.** It uses `ubuntu-20.04` (retired), Go 1.18 (go.mod says 1.24), triggers on `master` (branch is `main`), and golangci-lint v1.46.
- **Windows build is broken.** `daemon/main.go:276,303` use `syscall.Kill`.
- **Junk in the repo:** a gitlink at the literal path `~/.claude/plugins/marketplaces/claude-plugins-official`, `.playwright-mcp/` log, `icon-preview.png`, `assets/icon.svg.png`.
- **Swift build warning:** `Sources/Resources/AppIcon.icns` is not declared as a resource.

## Goals

1. **Quiet when healthy, loud only when you need to act.** No event or notification spam. Offline is not an error. A conflict notifies once.
2. **Never wedge or corrupt a repo.** No half-finished rebases, no hung git processes, no racing watchers.
3. **The app shows the truth and offers the fix.** Accurate status, persistent history, actionable banners, and feedback when a command fails.
4. **Green builds:** tests, vet, lint, CI, Swift build with no warnings.

## Non-goals

- No rewrite. The Go daemon and the Swift app keep their current structure.
- No new platforms. Linux and Windows must *compile*. The macOS experience is what gets polished.
- No merge strategies (YAML-header-aware merge, "pick newest"). Conflicts are still reported, not auto-resolved.
- No sync of individual subfolders (todo.md item). Deferred.

## Anti-goals (a failure even if it "works")

- Any code path that can leave a repo mid-rebase or mid-merge without telling the user.
- Losing local commits or uncommitted work.
- The daemon depending on the app being open.
- More notifications than today.

---

## Execution model

```
Phase 0 (lead, sequential)  →  Phase 1 (5 agents, parallel)  →  Phase 2 (lead, integration + verification)
   shared contracts + stubs        disjoint file ownership            merge, full build, live smoke test
```

- **Phase 0** gives every agent something to compile against: the protocol v2 types, new `RepoState` fields and methods, and the Swift `AppState` API surface, with minimal bodies. Agents then build in parallel without waiting on each other.
- **Phase 1** agents each own a disjoint set of files (below). An agent that needs a change in a file it does not own records the request in its final report. It does not make the edit.
- **Isolation:** each agent runs in its own git worktree. Worktrees branch from a commit, so the Phase 0 stubs must be committed first. *Decision needed from you (see the end).*
- **Phase 2:** the lead applies each worktree's diff to `main`'s working tree in order 1→5. It then runs the full verification list and a live smoke test against a scratch repo with a local bare remote.
- **Conventions for all agents:** match the surrounding style, add no inline code comments, make no git commits or pushes, and keep existing CLI commands and flags working.

---

## Phase 0: Shared contracts (lead)

### 0.1 Go: `common/protocol.go` (new)

```go
const ProtocolVersion = 2

type Request struct {
    ID       string            `json:"id,omitempty"`
    Cmd      string            `json:"cmd"`
    Repo     string            `json:"repo,omitempty"`
    Settings map[string]string `json:"settings,omitempty"`
    Since    uint64            `json:"since,omitempty"`
    Limit    int               `json:"limit,omitempty"`
}

type Response struct {
    ID     string      `json:"id,omitempty"`
    OK     bool        `json:"ok"`
    Result interface{} `json:"result,omitempty"`
    Error  string      `json:"error,omitempty"`
    Code   string      `json:"code,omitempty"`
}
```

`SocketCommand` becomes an alias of `Request`, so the existing call sites compile.

`func LogPath() string` and `func StateDir() string` (`~/.local/share/git-auto-sync`) live here, so Agents 2 and 3 agree on paths.

**Commands:** `status`, `list`, `add`, `remove`, `sync_now`, `sync_all`, `pause`, `resume`, `pause_all`, `resume_all`, `get_settings`, `set_settings`, `abort_rebase`, `events`, `subscribe`, `version`.

**Connection semantics (changed):** a connection gets exactly one `Response` line per `Request`. The `hello` frame and the pushed `state`/event frames go **only** to connections that sent `subscribe`. This removes today's "skip the hello frame" hack in `DaemonClient.sendCommand` and fixes the CLI, which currently prints the hello frame as the status response.

**Push frames (subscribers only):**
- `{"type":"hello","protocol":2,"version":"…","repos":[RepoState…],"lastEventId":N}`
- `{"type":"state","repo":…,"data":RepoState}`
- Event frames (shape below)

### 0.2 Go: `Event` gains fields (`common/events.go`)

```go
type Event struct {
    ID      uint64 `json:"id"`
    Type    string `json:"type"`
    Repo    string `json:"repo"`
    Message string `json:"message,omitempty"`
    Status  string `json:"status,omitempty"`
    Code    string `json:"code,omitempty"`
    Ts      int64  `json:"ts"`
}
```

**Event types:** `commit`, `pull` (remote changes applied), `push`, `synced` (emitted **only** when a commit, pull, or push actually happened), `error`, `conflict`, `preflight`, `offline`, `recovered` (offline/error → ok transition), `paused`, `resumed`.

**Error codes** (`Code`): `network`, `auth`, `timeout`, `conflict`, `lock`, `xcode_license`, `git_missing`, `no_author`, `push_rejected`, `config`, `unknown`.

`ID` is a process-wide monotonic sequence assigned by the emitter (Agent 3 owns the implementation). The Swift app uses it as the identity, which fixes the duplicate SwiftUI IDs when two events share a second.

### 0.3 Go: `RepoState` additions (`common/repostate.go`)

New status: `StatusOffline = "offline"`.

New fields:
- `ErrorCode string`
- `LastChangeTs int64`: last time something was committed, pulled, or pushed
- `LastCheckTs int64`: last completed sync attempt, with or without changes
- `NextRetryTs int64`
- `Failures int`: consecutive failures

`LastSyncTs` stays and keeps its meaning (last success) for compatibility.

New methods, stubbed with real bodies since they are trivial:
- `MarkOffline(path, msg string, nextRetry int64)`
- `MarkErrorCode(path, code, msg string)`
- `MarkChecked(path string, changed bool)`
- `MarkConflict` keeps its signature

### 0.4 Swift: model + AppState surface

In `DaemonEvent.swift`:

- `enum RepoStatus: String, Codable { idle, syncing, ok, error, conflict, preflight, paused, offline, unknown }`, decoding unknown values to `.unknown`.
- `RepoState` gains `statusValue: RepoStatus`, `errorCode`, `lastChange`, `lastCheck`, `nextRetry`, and `failures`.
- `DaemonEvent` gains `id: UInt64` and `code: String?`.

In `AppState.swift`, these exist with stub bodies so Agent 5 can build against them:

```swift
enum DaemonConnection: Equatable { case connecting, connected, daemonNotRunning, incompatible(daemonProtocol: Int) }
struct AppNotice: Identifiable, Equatable { let id = UUID(); let text: String; let isError: Bool }
struct RepoSettings: Equatable { var pollInterval: Int; var batchWindow: Int }

var connection: DaemonConnection
var notice: AppNotice?
func addRepos(_ urls: [URL]) async
func removeRepo(_ path: String) async
func syncNow(_ path: String) async
func syncAll() async
func setPaused(_ path: String, paused: Bool) async
func setAllPaused(_ paused: Bool) async
func saveSettings(_ path: String, _ settings: RepoSettings) async -> Bool
func abortRebase(_ path: String) async
func startDaemon() async
func eventsFor(repo: String) -> [DaemonEvent]
var allEvents: [DaemonEvent]
```

Existing members (`repos`, `selectedRepoPath`, `sidebarSearch`, `filteredRepos`, `summary`, `hasError`, `anySyncing`) stay.

### 0.5 Phase 0 as built (authoritative where it differs from above)

- `common/protocol.go`: `Request` also has `Force bool`. It adds `Cmd*` constants for every command, the helpers `OKResponse(result)` and `ErrResponse(code, msg)`, plus `StateDir()` and `LogPath()`. `DefaultSocketPath()` now uses `StateDir()`.
- `common/events.go`: `Event*` type constants and `Code*` constants. Besides the 0.2 codes there are protocol codes: `not_a_repo`, `not_watching`, `missing_repo`, `bad_request`, `unknown_command`, `already_exists`, `not_found`. `NewCodedEvent(type, repo, msg, code)` is new.
- `common/repostate.go`: the new fields include `ConflictHead`/`ConflictUp` (json `conflictHead`/`conflictUpstream`), used by the 1.4 conflict guard. There are new methods `MarkErrorCode`, `MarkOffline`, `MarkChecked`, and `SetConflictRefs`. `MarkError` delegates to `MarkErrorCode(CodeUnknown)`. `MarkSuccess` clears all error/conflict/retry fields.
- Swift `AppState`: `syncNow(_:force:)` with `force` defaulting to `false`. **Transitional members:** `connectionState`, `addRepo(_:)`, and `saveSettings(_:pollInterval:batchWindow:)` stay until Phase 2. Agent 5 must stop using them, and Agent 4 must keep them compiling. The lead deletes them during integration.
- Swift `DaemonEvent.id` is now `UInt64` decoded from the daemon. The protocol requires `id` on every event.
- `common/protocol.go` also has `CodedError{Code, Msg}`, `NewCodedError(code, msg)`, and `ErrorCode(err) string`. Daemon methods return `CodedError`s, and the socket layer maps them to `Response.Code`.
- `common/gitroot.go`: `RepoRoot(path) (string, error)` resolves to the absolute, symlink-resolved, cleaned top-level, or returns a `CodeNotARepo` `CodedError`. Agent 1 owns this file but must keep the signature. Agents 2 and 3 use it.
- `RepoConfig` (in `watch.go`) gains `Force bool` and `MessagePrefix string`. Agent 2 sets `Force` for a forced `sync_now` run and fills `MessagePrefix` from `auto-sync.messagePrefix`. Agent 1 reads both.
- **Daemon split (ownership change):** socket wiring (`startSocket`) and `HandleCommand` routing live in **`daemon/socket.go` (Agent 3)**. The command implementations live in **`daemon/commands.go` (Agent 2)** as `Daemon` methods: `AddRepo(path) (root string, err)`, `RemoveRepo(path) error`, `SyncNow(path, force) error`, `SyncAll()`, `SetPaused(path, paused) error`, `SetAllPaused(paused)`, `AbortRebase(path) error`, `readRepoSettings`, `writeRepoSettings`. Agent 3 does **not** edit `daemon/main.go` or `daemon/commands.go`. Agent 2 does **not** edit `daemon/socket.go`. If Agent 3 needs a hook from `run()` (e.g. event-log setup), it goes inside `startSocket()`, which `run()` already calls before watchers start.

### 0.6 Phase 0 exit check

`go build ./...`, `GOOS=linux go build ./...`, and `swift build` all pass. The Phase 0 diff is committed (by you) so worktrees can branch from it.

---

## Phase 1: Agents

Priority labels: **P0** must ship, **P1** should ship, **P2** if time allows. Each agent reports, per item: done, skipped (with reason), or blocked (with which file and owner).

### Agent 1: Sync engine correctness

**Owns:** `common/autosync.go`, `commit.go`, `fetch.go`, `rebase.go`, `push.go`, `author.go`, `preflight.go`, `ignore.go`, all their `_test.go` files, and `common/testdata/`. New files allowed: `common/git.go`, `common/classify.go`.

| # | Pri | Item |
|---|---|---|
| 1.1 | P0 | **Fix the 3 failing tests** (`commit_test.go`). Update the expectations to the current `--name-status` message format. Do not revert the format. |
| 1.2 | P0 | **Timeouts on every git invocation.** `GitCommand` runs under `exec.CommandContext` with per-operation timeouts (fetch/push 2 min, others 30 s). It also sets `-c http.lowSpeedLimit=1000 -c http.lowSpeedTime=30`. Timeout → code `timeout`. Signature: add `GitCommandCtx(ctx, cfg, args)`; `GitCommand` delegates with a background context. |
| 1.3 | P0 | **Error classification** (`classify.go`). Map stderr/exit code to the 0.2 codes. Network/timeout/5xx → `MarkOffline` + `offline` event, no notification. Exit 69 with the Xcode-license text → `xcode_license`, with the message "Run `sudo xcodebuild -license accept`". Auth (401/403, "Authentication failed", "could not read Username") → `auth`. |
| 1.4 | P0 | **Conflict loop.** After aborting a conflicted rebase, record `(HEAD, upstream)` SHAs in state. Later syncs skip the rebase while both SHAs are unchanged and the repo is in conflict: they still commit locally, do not notify, and do not log again. Retry when either SHA moves or the user hits Sync Now (`force`). |
| 1.5 | P0 | **push.go can leave a rebase in progress.** `_, _ = GitCommand(rebase …)` ignores conflicts. Route the retry through `rebase()` so conflicts abort and report. |
| 1.6 | P0 | **Only emit `synced` when something changed.** `commit` returns whether it committed. Detect a pull (HEAD moved after rebase) and a push (ahead > 0 before push). Emit `commit`/`pull`/`push` events accordingly. Call `MarkChecked(changed)`. Emit `recovered` on an error/offline → ok transition. |
| 1.7 | P0 | **Temp files get committed anyway.** `commit.go:43-61` drops `.swp`/`~` files from the *message* but `git add -A` already staged them. Unstage ignored paths (`git reset -q -- <paths>`) before committing. |
| 1.8 | P1 | **Filenames with spaces/unicode.** Use `git diff --cached --name-status -z` and parse NUL-separated records. `strings.Fields` currently breaks names with spaces, and `core.quotePath` mangles unicode. |
| 1.9 | P1 | **Cache remote facts.** `isGitHubRepo` and `repoUsesSSH` spawn `git remote get-url` on *every* git command (2 extra processes per call). Resolve once per sync. Move the SSH warning out of the per-attempt loop and into the log, not stdout. |
| 1.10 | P1 | **Environment for launchd.** `toEnvString` passes only HOME/PATH/USER/LOGNAME, and launchd's PATH lacks `/opt/homebrew/bin`, so `gh` is not found. Append `/opt/homebrew/bin:/usr/local/bin` to PATH when missing. Also pass `SSH_AUTH_SOCK`, `LANG`, and `GIT_*` if present. |
| 1.11 | P1 | **Fetch only the upstream remote**, not every remote (`fetch.go`). A dead secondary remote currently fails every sync. |
| 1.12 | P1 | **Drop go-git (v4, abandoned) from the sync path.** Use `git rev-parse --git-dir`, `--abbrev-ref HEAD`, and `@{u}` instead. This makes preflight work for worktrees and submodules, where `.git` is a file (`preflight.go:34,42`, `rebase.go:99-104`). |
| 1.13 | P1 | **ignore.go correctness + speed.** `isFileIgnoredByGit` re-reads every `.gitignore` in the worktree on every FS event and passes an absolute path as one segment to the matcher (wrong). `filePath[len(repoPath)+1:]` panics when `filePath == repoPath`. Use a fast path for `.git/` prefixes, and use `git check-ignore` (batched or cached). Keep the `ShouldIgnoreFile(repoPath, path) (bool, error)` signature. |
| 1.14 | P1 | **Tests:** add table tests for `classify.go`, the conflict-loop guard (1.4), and push-retry-with-conflict (1.5). Use a temp bare remote, created the same way the existing fixtures are. |
| 1.15 | P2 | Commit message prefix configurable via `auto-sync.messagePrefix` (e.g. hostname). This helps tell machines apart in history. Read in `NewRepoConfig` (Agent 2 file): request it in the report if needed. |

### Agent 2: Daemon runtime, watcher, config, service

**Owns:** `common/watch.go`, `daemon/main.go`, `daemon/commands.go`, `common/awake_notifier_*.go`, `common/service.go`, `common/repostate.go`, `common/config/*`. New files allowed under `common/` and `daemon/`.

| # | Pri | Item |
|---|---|---|
| 2.1 | P0 | **Remove self-SIGHUP.** Socket `add`/`remove` call `d.reconcileWatchers` directly (`daemon/main.go:276,303`). This fixes the Windows build. Keep the SIGHUP handler behind a unix build tag. |
| 2.2 | P0 | **restartWatcher race.** It cancels the old watcher but starts the new one without waiting (`daemon/main.go:200-223`). Two `AutoSync`s can run on one repo, and the old watcher's deferred `SetWatching(false)` can land after the new one's `true`. Give each handle a `done` channel and wait for it before restarting. |
| 2.3 | P0 | **Per-repo sync mutex.** Serialize `AutoSync` per repo across poll, fs-event, `sync_now`, and wake. |
| 2.4 | P0 | **Interruptible sleeps.** `fire()` uses `time.Sleep` for FSLag, backoff (up to 60 s), and cooldown, which blocks ctx cancellation, `sync_now`, and shutdown (Stop only waits 10 s). Replace them with `select` on `ctx.Done()` and timers. |
| 2.5 | P0 | **Offline-aware backoff.** When state is `offline`, retry on an exponential schedule (30 s → 10 min cap) and publish `NextRetryTs`. A wake-from-sleep or `sync_now` retries immediately. |
| 2.6 | P0 | **Config validation.** `syncInterval=0` panics in `time.NewTicker`. Treat non-numeric or out-of-range values as defaults with a logged warning, not a dead watcher. Clamp to poll ≥ 30 s and batch 0–86400 s. Apply the same validation in `writeRepoSettings` and return a clear error string. |
| 2.7 | P0 | **Atomic config writes** (`config/v1.go`). Write a temp file + rename. A reader during `os.Create` truncation currently gets a JSON decode error. Also normalize repo paths on read (Clean, dedupe). |
| 2.8 | P0 | **Validate `add` from the socket.** Resolve to the repo root (`git rev-parse --show-toplevel`). Reject non-repos with `{ok:false, code:"not_a_repo"}`. Dropping a subfolder in the app currently registers the subfolder. |
| 2.9 | P1 | **Single wake broadcaster.** Every watcher calls `NewAwakeNotifier().Start()`. On darwin, that shares one singleton channel, so only one repo receives each wake, and the goroutines leak on restart. Start one notifier in the daemon and fan out to all handles' trigger channels. |
| 2.10 | P1 | **Logs.** Move them out of `$HOME` into `~/Library/Logs/git-auto-sync/daemon.log` on macOS (or `$XDG_STATE_HOME` on Linux). Rotate at 5 MB × 3 files. Use the kardianos `LogDirectory` option if the vendored version supports it; otherwise use an in-process rotating writer. Demote per-poll "success" and fs-event lines to debug, gated by `GAS_DEBUG=1`. |
| 2.11 | P1 | **Persist RepoState** (`lastSyncTs`, `lastChangeTs`, conflict SHAs, `paused`) to `~/.local/share/git-auto-sync/state.json` on change (debounced), and restore it on start. Paused repos currently resume after a daemon restart. |
| 2.12 | P1 | **Commands needing a watcher handle:** implement `sync_all`, `pause_all`, `resume_all`, `abort_rebase` (only when `rebase-merge`/`rebase-apply` exists; then re-run preflight), and `sync_now` with `force` (clears the conflict guard from 1.4). Agent 3 wires the protocol routing; Agent 2 implements the `Daemon` methods it calls. |
| 2.13 | P2 | Stagger the initial sync across repos by a few seconds so a daemon start doesn't fire N fetches at once. |

### Agent 3: Socket protocol, CLI, repo hygiene, CI

**Owns:** `common/socket.go`, `common/socket_client.go`, `common/events.go`, `common/protocol.go` (after Phase 0), `daemon/socket.go`, `main.go`, `daemon.go`, `completions/`, `.github/`, `.goreleaser.yaml`, `justfile` (Go targets only), `README.md`, `todo.md`, `.gitignore`, the junk files. Owns `daemon/socket.go` (socket wiring + `HandleCommand` routing); see 0.5.

| # | Pri | Item |
|---|---|---|
| 3.1 | P0 | **Protocol v2** as specified in 0.1. Use the request/response envelope with echoed `id`. Push frames only after `subscribe`. Include `protocol` in the hello, `status`, and `version` responses. |
| 3.2 | P0 | **Slow clients can stall syncing.** `Broadcast`/`Emit` write synchronously under `RLock` with no deadline, and they are called from inside `AutoSync` via the state listener. Give each subscriber a buffered queue (e.g. 256 frames) drained by a goroutine with a 2 s write deadline. Drop and disconnect on overflow. |
| 3.3 | P0 | **Event ring buffer + `events` command.** Keep the last 1,000 events in the daemon (assign `ID` here) and persist them to `~/.local/share/git-auto-sync/events.jsonl` (truncate to the last 1,000 on start). `events {since, repo?, limit?}` returns history. The hello includes `lastEventId` so the app can backfill. |
| 3.4 | P0 | **Socket permissions:** the directory is `0700` and the socket `0600`. |
| 3.5 | P0 | **CLI output.** `daemon status`/`ls` print a human table: name, status, branch, ahead/behind, last change, and error short-form. Add `--json` for raw output. `daemon status` should not error when the service is not installed; it should say so. |
| 3.6 | P0 | **`daemon rm` fails for deleted repos** (`daemon.go:162` requires a valid repo). Match on the cleaned absolute path first and validate only if not found. |
| 3.7 | P0 | **`daemon add`/`rm` reinstall and restart the service every time** (`daemon.go:110`, `s.Enable()`). If the daemon is reachable, send `add`/`remove` over the socket. Only fall back to config-write + `Enable()` when it is not running. |
| 3.8 | P0 | **CI:** use `ubuntu-latest` + `macos-latest`, Go from `go.mod`, branch `main`, and current `golangci-lint-action`. Add a macOS job running `swift build` for the app. Add a `GOOS=windows go build ./...` step. |
| 3.9 | P0 | **Repo hygiene:** remove the `~/.claude/...` gitlink, `.playwright-mcp/`, `icon-preview.png`, and `assets/icon.svg.png` (check nothing references it first). Extend `.gitignore` with `.build/`, `.playwright-mcp/`, and the built binaries. |
| 3.10 | P1 | New CLI subcommands that map 1:1 to socket commands: `daemon sync [repo|--all]`, `daemon pause|resume [repo|--all]`, `daemon logs [-f]` (tails the log path chosen in 2.10; agree the path via the Phase 0 constant `common.LogPath()`), and `daemon events [-n N] [repo]`. |
| 3.11 | P1 | `daemon env`: replace `log.Fatalln` with a returned error, and sort the output (map iteration is random today). |
| 3.12 | P1 | README: document the new commands and the event/notification behaviour, plus the Xcode-license and `gh` PATH gotchas. Prune `todo.md` of items this pass completes. |
| 3.13 | P2 | Shell completions: regenerate for the new subcommands. |

### Agent 4: macOS app: data layer, notifications, packaging

**Owns:** `DaemonClient.swift`, `AppState.swift`, `DaemonEvent.swift`, `NotificationManager.swift`, `GitAutoSyncApp.swift`, `Package.swift`, and the `justfile` app targets (`build-app`, `install-app`, `run-app`).

| # | Pri | Item |
|---|---|---|
| 4.1 | P0 | **Protocol v2 client.** One long-lived subscriber connection (send `subscribe`, then read frames) plus short request connections that read exactly one response line, with `id` matching. Delete the "read 2 lines, skip hello" logic. |
| 4.2 | P0 | **Thread safety.** `DaemonClient` is `@unchecked Sendable` with unsynchronized mutable state (`connectionState`, `fileDescriptor`, `reconnectTask`) touched from detached tasks. Make it an `actor` or guard it with a lock. Make `AppState` `@MainActor`. Blocking `read()` must not run on the main actor. |
| 4.3 | P0 | **Stop polling every 10 s while subscribed.** Status comes from hello/state frames. Poll only while disconnected, to detect the daemon coming back. |
| 4.4 | P0 | **History backfill.** On hello, call `events {since: lastSeenId}` (first launch: last 500) so the timeline survives app restarts. Key events by `id`. |
| 4.5 | P0 | **Surface command results.** Every `AppState` action decodes the `Response`. On `ok:false` it sets `notice` with a human message (e.g. "Not a git repository: ~/Downloads"). `saveSettings` returns `false` on failure. |
| 4.6 | P0 | **Notifications on transitions only.** Notify when a repo *enters* `conflict`, `error` (non-network), or `preflight`, or on `xcode_license`/`auth`. Never notify for `offline`. Optionally notify on `recovered` after a conflict. Use the title "Git Auto Sync" and the repo name as subtitle. Clicking a notification opens the dashboard with that repo selected (pass the repo path in `userInfo`; `AppState` exposes `selectedRepoPath`). Remove the 60 s debounce; transitions make it unnecessary. |
| 4.7 | P0 | **`connection` state** derived from the subscriber: `.daemonNotRunning` when the socket is absent, and `.incompatible` when `protocol != 2`. |
| 4.8 | P1 | **`startDaemon()`** runs `launchctl kickstart -k gui/$UID/git-auto-sync-daemon`, falling back to `git-auto-sync daemon start` (resolve the binary in `/usr/local/bin` and `/opt/homebrew/bin`). It sets `notice` on failure. |
| 4.9 | P1 | **Packaging.** Declare `Resources/AppIcon.icns` in `Package.swift` (fixes the warning). In `install-app`: set `CFBundleShortVersionString` from `git describe`, ad-hoc codesign (`codesign -s - --force --deep`), and `NSUserNotificationAlertStyle`. Make `launchAtLogin` reflect `SMAppService` status on open. |
| 4.10 | P1 | **Event cap.** Raise it to 2,000 in memory. `allEvents` is newest-last, and `eventsFor` is cached per repo, not re-filtered on every view access. |

### Agent 5: macOS app: UI and UX

**Owns:** `DashboardView.swift`, `RepoDetailView.swift`, `MenuBarView.swift`, `ActivityLogView.swift`, `LiveTime.swift`. New files allowed under `Sources/UI/`, e.g. `RepoActions.swift` and `StatusStyle.swift`.

| # | Pri | Item |
|---|---|---|
| 5.1 | P0 | **Deduplicate helpers** into `Sources/UI/`: `abbreviatePath` (×3), `eventColor` (×2), status colour/symbol, open-in-Finder/Terminal (×3), copy path (×2), and the `NSOpenPanel` add flow (×2). One `StatusStyle` drives the badge, pill, and menu, and handles `offline`. |
| 5.2 | P0 | **Syncing spinner bug.** `StatusBadge` starts its rotation in `onAppear`, so a row that *becomes* syncing never spins and a row that stops keeps its angle. Same pattern in `MenuBarIcon`. Use `.symbolEffect(.rotate)` where available (macOS 15), else a `TimelineView`-driven angle. |
| 5.3 | P0 | **Confirm before remove.** The footer `−`, context menu, and toolbar currently remove instantly. Use a `.confirmationDialog` that says files are untouched and only monitoring stops. |
| 5.4 | P0 | **Daemon-offline UX.** When `connection == .daemonNotRunning`, show a sidebar banner and empty-state copy with a **Start Daemon** button (`appState.startDaemon()`). Show `.incompatible` as "Daemon is outdated: run `just install`". |
| 5.5 | P0 | **Actionable banners.** Offline: "Offline: retrying in 3m" using `nextRetry`, no red. Conflict: list files, plus **Abort & retry** (`syncNow` force), **Open in Terminal**, **Open in editor** (`NSWorkspace` default app for the first file), and **Copy resolve commands**. Preflight `rebase_in_progress`: **Abort rebase** button. Preflight `no_upstream`: copyable `git branch -u origin/<branch>`. `xcode_license`: copyable `sudo xcodebuild -license accept`. |
| 5.6 | P0 | **Notice toast.** Render `appState.notice` as a transient banner at the top of the detail pane (auto-dismiss after 4 s for non-errors). |
| 5.7 | P0 | **Menu bar menu shows repos.** List each repo with a status symbol and last-change time. Each submenu has Sync Now / Pause / Open in Finder / Show in Dashboard. Add **Sync All** and **Pause All / Resume All**. The icon gets a slash variant when the daemon is offline. |
| 5.8 | P1 | **Global activity view.** An "All Activity" entry at the top of the sidebar shows every repo's events, using the existing `ActivityLogView` pieces (`EventFilter`, `EventRow`, repo filter). This restores the cross-repo log the new UI dropped. Filters: All / Changes (commit, pull, push) / Problems (error, conflict, preflight, offline). |
| 5.9 | P1 | **Overview stats.** "Last change" (`lastChange`) and "Last check" (`lastCheck`) replace the single "Last sync". Divergence uses colour only when non-zero. |
| 5.10 | P1 | **Keyboard:** ⌘N add, ⌘F focus filter, ⌘⌫ remove (with confirm), ⌘. pause/resume, ⌘⇧R sync all. ⌘R stays as is. |
| 5.11 | P1 | **Settings tab.** Disable Save while saving. Only show "Saved" when `saveSettings` returns true, and show the daemon's error otherwise. Show the repo's git config section read-only (`get_settings`) for transparency. |
| 5.12 | P1 | **Accessibility.** Status badges and pills get `accessibilityLabel` (status is currently colour + glyph only). Every icon-only button gets `.help` and a label. |
| 5.13 | P2 | **"Open in…" preference.** A Terminal / iTerm / Ghostty / Warp picker stored in `@AppStorage`, used by every "Open in Terminal". |

---

## Phase 2: Integration and verification (lead)

1. Apply worktree diffs in order 1 → 5 and resolve any cross-ownership requests from the agents' reports.
2. Run `gofmt -l .` (empty output), `go vet ./...`, `go test ./...`, `golangci-lint run`, and `GOOS=linux go build ./...`; `GOOS=windows go build ./...`; `cd macos/GitAutoSyncMenuBar && swift build -c release` with zero warnings.
3. **Live smoke test** against a scratch repo cloned from a local bare remote (not your real repos):
   - edit a file → one `commit` + `push` + `synced`; idle polls emit nothing
   - create a conflicting upstream commit → exactly one `conflict` event and one notification. Polls stay quiet. Sync Now force retries.
   - point the remote at an unreachable URL → `offline` with a countdown, no notification. Restoring the URL produces `recovered`.
   - kill the daemon while the app is open → Start Daemon works and the timeline history survives
   - `daemon add <subfolder>` → registers the repo root; `daemon rm <deleted path>` works
4. `just install-all`, restart the real daemon, and confirm your three repos come back green and the old log is no longer growing in `$HOME`.

## Calls I made (check these)

- **Protocol v2 is a breaking change** between the daemon and the app. Both ship from this repo, and the app shows "daemon outdated" on a mismatch instead of trying to stay compatible.
- **go-git removed from the sync path** (1.12) in favour of the `git` CLI, which the daemon already shells out to for everything that matters. `daemon.go`'s `isValidGitRepo` can follow in 3.x if it is cheap.
- **Offline is its own status**, not an error, and never notifies.
- **Logs move** to `~/Library/Logs/git-auto-sync/`. The old `~/git-auto-sync-daemon.*.log` files are left for you to delete.
- **Windows stays compiling but untested.**
- **Commit-noise reduction** (todo.md's Obsidian complaint) is covered by the existing `batchWindow` plus quieter events. Squashing unpushed auto-commits is out of scope this pass.

## Decision needed before Phase 1

**Worktrees need a commit to branch from.** Your global rule is that I never commit. Pick one:

- **(a)** After Phase 0, you commit the stubs; then agents run in isolated worktrees and I apply their diffs uncommitted in Phase 2. *Recommended:* cleanest isolation and the fewest cross-agent build breaks.
- **(b)** All five agents work in this one working tree with strict file ownership and nothing gets committed. This is simpler, but agents will see each other's half-finished edits break `go build`/`swift build` mid-run.

---

# Round 2: Refinement pass

Status: done. Round 2 is merged into the working tree (uncommitted) and verified; see "Round 2 outcome" at the end. See "Round 1 outcome" below.

## Round 1 outcome (what the second pass starts from)

- All 64 items landed. Go: gofmt, vet, race tests, linux/windows builds and golangci-lint v2.15 are all clean. Swift release build has zero warnings.
- Live smoke test passed: edit, conflict (exactly one event), forced retry, manual resolve leading to `recovered`, offline with countdown leading to `recovered`, restart keeping history and paused state, subfolder add, deleted-path rm.
- Lead fixes during integration: `common.ResolvePath` (symlink-safe matching for deleted paths), a `chdir` failure is classified as `not_a_repo`, the Swift transitional members are removed, and Launch at Login is bound to `AppState`.
- **Known rough edges seen in the smoke test:**
  1. While offline, file events are deferred until the retry time (up to 10 min), so local edits aren't committed promptly.
  2. After a daemon restart, branch/upstream show `-` until the first sync completes.
  3. A deleted repo folder shows `config error: stat …: no such file` instead of a clear "repository folder missing" state.
  4. Keyboard shortcuts ⌘F and ⇧⌘R live on invisible zero-size buttons, and the menu bar spinner re-renders every ~70 ms. Neither has been seen running.
  5. The app has never been looked at visually.
  6. There are no Swift tests.

## Round 2 baseline additions (lead)

- `GIT_AUTO_SYNC_SOCKET` env var overrides the socket path in both `common.DefaultSocketPath()` and Swift `DaemonClient.defaultSocketPath`. Use it to run the real app against a test daemon. Never point anything at the real `~/.local/share/git-auto-sync/daemon.sock`.
- `RepoConfig.LocalOnly bool`: when true, `AutoSync` commits locally and skips fetch/rebase/push and every network-dependent state change. Agent 1 implements this; Agent 2's scheduler uses it.

## How round 2 works

Same five areas and the same ownership as round 1. Ownership extends to the files each agent created in round 1. Each agent starts with an **adversarial review of its own area as it is now**: read the code, run it, and hunt for bugs, races, edge cases, unclear errors, UX rough edges and missing tests. It writes down what it found, then fixes the real problems in priority order, together with its seed items below. Each item is still sorted as **P0** (must fix), **P1** (should fix) or **P2** (if time allows). Agents drop anything not worth its risk and say why. **No rewrites for taste:** change things only where there is a concrete defect or a clear user-visible improvement.

### Agent 1: sync engine (refine)
Owns: `common/{autosync,commit,fetch,rebase,push,author,preflight,ignore,gitroot,git,classify}.go`, their tests (`sync_test`, `git_test`, `classify_test`, `gitroot_test`, `ignore_test`, `commit_test`, `rebase_test`, `fetch_test`), `common/testdata/`.
- P0: implement `RepoConfig.LocalOnly`: commit only, no network, no offline/error transition, but still emit `commit` and call `MarkChecked(changed)`.
- P0: adversarial review covering:
  - `commit.gpgsign=true` with no agent: must fail fast and clearly, not hang until the timeout.
  - Pre-commit hooks that modify files or fail.
  - Git LFS repos, submodules, sparse checkout.
  - Repos with no commits yet.
  - An upstream branch deleted on the remote, or force-pushed.
  - Huge change sets: cap the commit message at ~50 lines plus "… and N more".
  - Concurrent manual `git commit` by the user while a sync runs.
- P1: count git subprocesses per idle poll (no changes) and per edit sync, and cut redundant calls. Report the numbers before and after.
- P1: error summaries, so every `ClassifyError` code gives a one-line human message fit for the app banner.

### Agent 2: daemon runtime (refine)
Owns: `common/{watch,repostate,repostate_persist,repoconfig,atomicfile,logging,service,awake_notifier_*}.go` + tests, `common/config/*`, `daemon/{main,commands,logging,state,signals_unix,signals_windows}.go`, `daemon/main_test.go`.
- P0: while offline or backing off, file events trigger a prompt `LocalOnly` run, so local commits happen within the normal debounce. The network retry stays on its own schedule.
- P0: restore branch/upstream/ahead/behind on daemon start without waiting for a full sync: a cheap local read, no network.
- P0: a deleted or moved repo folder gets a distinct, clear state (reuse `preflight` with code `repo_missing`, or similar; coordinate the string via the report). It shows no stack trace and recovers automatically when the folder reappears.
- P1: stress the scheduler and daemon. Run `go test -race -count=50` on the scheduler and daemon tests. Cover shutdown during a sync, `state.json` corruption or truncation (must start cleanly), config edited by hand while running, and 20+ repos at once.
- P1: inspect the launchd plist kardianos generates on macOS (render it to a temp dir, don't install it): `LogDirectory`, KeepAlive, an `EnvironmentVariables` PATH that includes Homebrew. Fix what's wrong.
- P2: retry offline repos promptly when the network comes back, if it can be done without cgo; otherwise skip and say so.

### Agent 3: protocol, CLI, hygiene (refine)
Owns: `common/{socket,socket_client,events,protocol}.go` + `socket_test.go`, `daemon/socket.go` + `daemon/socket_test.go`, `main.go`, `daemon.go`, `completions/`, `.github/`, `.goreleaser.yaml`, `.golangci.yml`, the Go targets in `justfile`, `README.md`, `todo.md`, `.gitignore`.
- P0: `git-auto-sync doctor` (the todo.md config checker). It checks:
  - git present, Xcode license, `gh` present and authenticated (only if a repo uses GitHub)
  - per repo: author, upstream, remote reachable (with a timeout)
  - daemon running, protocol version, launchd plist pointing at an existing binary
  - log dir writable, socket permissions
  - old `~/git-auto-sync-daemon.*.log` files present

  Each check prints ✓/⚠/✗ with a fix hint, and `--json` gives machine output. Read-only: it never fixes anything itself.
- P1: protocol robustness:
  - fuzz `HandleCommand` and the line reader with odd input (huge lines, invalid UTF-8, missing fields)
  - a 50-concurrent-client stress test
  - event log recovery from a truncated or corrupt last line
- P1: CLI polish: consistent exit codes (non-zero on any failure), errors to stderr, and `--json` everywhere a table is printed.
- P1: validate `.github/workflows/ci.yml` with actionlint (`go run github.com/rhysd/actionlint/cmd/actionlint@latest`) and `.goreleaser.yaml` with `goreleaser check` if it's cheap to run. Fix the findings.
- P1: make README accurate against the actual behaviour (spot-check every command it documents). Document `GIT_AUTO_SYNC_SOCKET` and `doctor`.

### Agent 4: app data layer (refine)
Owns: `Sources/{DaemonClient,AppState,DaemonEvent,NotificationManager,GitAutoSyncApp,DaemonLauncher}.swift`, `Package.swift`, the app targets in `justfile`, and a new `Tests/` directory.
- P0: add a test target and turn round 1's ad-hoc fake-daemon harness into real tests. Cover:
  - decoding fixtures taken from the **real** daemon wire format (generate them by running the Go daemon with a temp HOME + `GIT_AUTO_SYNC_SOCKET`)
  - `AppState` notification-transition logic (error→syncing→error must not re-notify; conflict→ok gives one "resolved")
  - backfill and dedupe
  - incompatible-protocol detection
- P0: adversarial review: reconnect storms, daemon restart mid-request, a request timeout while the subscriber is healthy, a large backfill (2,000 events) on the main actor, retain cycles.
- P1: persist `lastSeenEventId` across app launches so backfill fetches only what's new.
- P1: `startDaemon()` when the LaunchAgent isn't installed at all: fall back to `git-auto-sync daemon enable`, with clear notices.
- P1: provide app menu commands (in `GitAutoSyncApp.swift` `.commands`) for Sync All ⇧⌘R, Find ⌘F, Add ⌘N, Pause ⌘. etc. They call `AppState` or post focus requests that Agent 5's views can observe. Expose what you add, e.g. a `focusSearchRequest` counter on `AppState`, and list it in the report so Agent 5's invisible buttons can be removed in integration.

### Agent 5: app UI/UX (refine)
Owns: `Sources/{DashboardView,RepoDetailView,MenuBarView,ActivityLogView,LiveTime}.swift`, `Sources/UI/*`.
- P0: **actually look at the UI.** Build a snapshot harness in the scratchpad (not in the package). Compile the app sources minus `GitAutoSyncApp.swift`, plus a `main.swift`, with `swiftc`, as the lead did in round 1. Render the key views to PNG with `ImageRenderer`, using fixture `RepoState`/`DaemonEvent` decoded from JSON. Do every state in light and dark:
  - ok, syncing, offline, conflict, each preflight code, xcode_license, auth, paused
  - daemon offline, incompatible
  - empty
  - All Activity with 2,000 events
  - the menu content

  View the PNGs with the Read tool and fix what looks wrong. Before/after PNGs go in the scratchpad.
- P0: design review against macOS HIG: hierarchy, spacing rhythm, alignment, truncation of long repo names and paths, banner copy (use the ux-copy guidance: plain, actionable, no jargon), consistency of colours, symbols and pills across sidebar, detail, menu and notices.
- P1: performance: Timeline and All Activity with 2,000 events must use lazy stacks and avoid re-filtering on every render. Check the menu bar spinner's cost while syncing, and keep it cheap.
- P1: accessibility audit (VoiceOver labels, contrast in both themes, keyboard reachability).
- Keep the invisible shortcut buttons this round. Agent 4 adds real menu commands, and the lead removes the duplicates during integration.

## Shared rules (unchanged)
Apply the round-2 baseline patch, stage it with `git add -A` (never commit), and only edit files you own. Add no inline code comments. Never touch the real daemon, socket, config, logs, LaunchAgents, `/Applications` or the user's repos. Test daemons run with `HOME=/tmp/gas-<agent>-<random>` (short, because of the socket path limit) plus `GIT_AUTO_SYNC_SOCKET`; clean them up afterwards. Finish with `git add -N .` and `git diff > patches/r2-agentN.patch`, and a report.

## Round 2 outcome

- All five patches applied cleanly in order. Go: gofmt, vet, race tests, linux/windows builds and golangci-lint v2.15 are all clean. Swift: release build has 0 warnings; `swift test` passes 49/49.
- The live smoke test passed: duplicate add through `/tmp` returns already monitoring, edit then push, offline local commits followed by `recovered`, branch shown right after restart, missing folder gives `repo_missing` then `recovered`, deleted remote branch gives `upstream_gone`, and `doctor` is correct.
- Lead integration:
  - wrapped user-visible daemon errors with `common.ErrorMessage`
  - replaced the hidden view shortcut buttons with the app menu commands (`focusSearchRequest`, `addRepoRequest` and `removeSelectedRequest` are observed in `DashboardView`)
  - CLI wording: "Already monitoring"
- Still open:
  - the app has only been rendered offscreen; it has never run as an installed app
  - a GUI pinentry for signed commits waits until the commit timeout
  - goreleaser `brews` deprecation
  - rebase can't be cancelled during shutdown
