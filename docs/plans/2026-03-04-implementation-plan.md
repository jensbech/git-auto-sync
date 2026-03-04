# git-auto-sync: Implementation Plan

Date: 2026-03-04
Parent: [menubar-app-design.md](./2026-03-04-menubar-app-design.md)
Status: Draft

---

## Phase 1: Daemon Bug Fixes & Graceful Shutdown

These are prerequisites — the socket server and menu bar app depend on a stable daemon with clean lifecycle management.

### Step 1.1: Add shutdown channel and fix goroutine leak

**Files:** `daemon/main.go`, `common/watch.go`

- Add a `done chan struct{}` to `daemon` struct in `daemon/main.go`
- Pass it through `watchForChanges()` into `WatchForChanges()`
- Refactor `WatchForChanges()` to accept a `ctx context.Context` (derived from `done` channel) instead of running forever
- Add `select` with `ctx.Done()` case to the infinite loop in `watch.go:134`
- Add `ctx.Done()` case to the goroutine's `select` at `watch.go:84-121`
- Stop `pollTicker` and call `notify.Stop()` on context cancellation
- In `daemon.Stop()`: close `done` channel, then `wg.Wait()` with a timeout

### Step 1.2: Fix panic on config read

**File:** `daemon/main.go:21-24`

- Replace `panic(err)` with `log.Printf` + return error
- Change `run()` to return `error`
- On config read failure: log error, attempt retry after 5s, give up after 3 attempts and trigger graceful shutdown

### Step 1.3: Fix env var duplication

**File:** `common/commit.go:145-147`

- Change line 146-147 from:
  ```go
  vals := repoConfig.Env
  vals = append(vals, repoConfig.Env...)
  ```
  to:
  ```go
  vals := make([]string, len(repoConfig.Env))
  copy(vals, repoConfig.Env)
  ```
- Add a test case that verifies no duplicate env vars in subprocess

### Step 1.4: Add SIGHUP config reload

**Files:** `daemon/main.go`

- Add signal handler for `SIGHUP` in `run()`
- On SIGHUP: re-read config, diff repo lists, stop watchers for removed repos (via their contexts), start watchers for new repos
- This requires refactoring `run()` to track per-repo contexts in a map: `map[string]context.CancelFunc`

### Step 1.5: Add structured event emitter

**Files:** new `common/events.go`, modifications to `common/autosync.go`, `common/commit.go`, `common/push.go`, `common/fetch.go`, `common/rebase.go`

- Define `Event` struct:
  ```go
  type Event struct {
      Type    string `json:"type"`
      Repo    string `json:"repo"`
      Message string `json:"message,omitempty"`
      Status  string `json:"status,omitempty"`
      Ts      int64  `json:"ts"`
  }
  ```
- Define `EventEmitter` interface:
  ```go
  type EventEmitter interface {
      Emit(Event)
  }
  ```
- Add `EventEmitter` field to `WatchConfig` (the config struct passed through watch/autosync)
- Emit events at key points:
  - `commit.go`: after successful commit → `{type: "commit", repo, message}`
  - `push.go`: after push attempt → `{type: "push", repo, status: "ok"/"error"}`
  - `fetch.go`: after fetch → `{type: "fetch", repo, status}`
  - `rebase.go`: after rebase → `{type: "rebase", repo, status}`
  - `autosync.go`: on error → `{type: "error", repo, message}`
- Default `EventEmitter` implementation: log-only (prints to stdout via `log.Printf`)
- Socket server will provide a second implementation that broadcasts to clients (Step 2.1)

---

## Phase 2: Unix Socket Server

### Step 2.1: Socket server core

**File:** new `common/socket.go`

- `SocketServer` struct with:
  - `listener net.Listener` (Unix socket)
  - `clients map[net.Conn]struct{}` with `sync.RWMutex`
  - `events chan Event` (buffered channel, size 256)
- `NewSocketServer(socketPath string) (*SocketServer, error)` — remove stale socket, bind, listen
- `Accept()` loop in goroutine: accept connections, add to clients map, spawn per-client reader
- `Broadcast(event Event)` — JSON-encode + newline, write to all clients, remove failed clients
- Implement `EventEmitter` interface so it can be plugged into the watcher pipeline from Step 1.5
- `Close()` — close all clients, close listener, remove socket file
- Socket path: `~/.local/share/git-auto-sync/daemon.sock`

### Step 2.2: Command handling

**File:** `common/socket.go` (continued)

- Per-client reader goroutine reads newline-delimited JSON commands
- Command dispatch:
  - `list` → read config, respond with `{type: "status", repos: [...]}` on that connection only
  - `status` → same as list but includes daemon uptime, version
  - `add` → append to config file, emit event, trigger SIGHUP to self (`syscall.Kill(syscall.Getpid(), syscall.SIGHUP)`)
  - `remove` → remove from config file, emit event, trigger SIGHUP to self
- Responses go only to the requesting client (not broadcast)

### Step 2.3: Wire socket into daemon

**File:** `daemon/main.go`

- In `run()`: create `SocketServer`, pass it as `EventEmitter` to all watchers
- On daemon stop: call `SocketServer.Close()`
- Socket server failures are non-fatal — log and continue without socket

### Step 2.4: CLI integration

**File:** `daemon.go`

- Add socket client to `daemon status`, `daemon list` commands
- Try socket first for real-time data; fall back to config file read if socket unavailable
- This makes CLI commands work without restarting the daemon

---

## Phase 3: macOS Menu Bar App

### Step 3.1: Xcode project setup

**Directory:** `macos/GitAutoSyncMenuBar/`

- Create Swift Package or Xcode project targeting macOS 13+
- Add `Package.swift` or `.xcodeproj`
- App delegate using `@main` and `MenuBarExtra`
- Deployment target: macOS 13 (Ventura) for `MenuBarExtra` and `SMAppService`

### Step 3.2: DaemonClient (Swift)

**File:** `macos/GitAutoSyncMenuBar/Sources/DaemonClient.swift`

- Swift actor that manages Unix socket connection
- Uses `NIOPosix` (SwiftNIO) or Foundation's `FileHandle`/`InputStream` for socket I/O
- Reads newline-delimited JSON, decodes to `DaemonEvent` structs
- Publishes events via `AsyncStream<DaemonEvent>`
- Sends commands as JSON
- Auto-reconnect with exponential backoff (1s → 2s → 4s → ... → 30s max)
- Connection state: `.connected`, `.disconnected`, `.reconnecting`

### Step 3.3: App state model

**File:** `macos/GitAutoSyncMenuBar/Sources/AppState.swift`

- `@Observable class AppState`
- Properties:
  - `repos: [RepoStatus]` — path, lastSync date, status enum
  - `events: [DaemonEvent]` — last 200 events (ring buffer)
  - `connectionState: ConnectionState`
  - `daemonRunning: Bool`
- Subscribes to `DaemonClient` stream, updates state on each event
- On `status` event: refresh full repo list
- On `commit`/`push`/`error`: append to events, update relevant repo's status

### Step 3.4: Menu bar UI

**File:** `macos/GitAutoSyncMenuBar/Sources/MenuBarView.swift`

- `MenuBarExtra` with status icon (SF Symbols)
- Icon state derived from `AppState`:
  - All repos OK → `arrow.triangle.2.circlepath` (green tint)
  - Any repo error → `exclamationmark.triangle` (yellow)
  - Disconnected → `arrow.triangle.2.circlepath` (gray)
- Menu content:
  - `ForEach` over `appState.repos` showing path, relative time, status badge
  - "Activity Log..." button → opens log window
  - Divider
  - "Add Repository..." → `NSOpenPanel` folder picker → send `add` command
  - "Remove Repository..." → submenu per repo → send `remove` command
  - Divider
  - "Launch at Login" toggle → `SMAppService`
  - "Quit" → `NSApp.terminate`

### Step 3.5: Activity Log window

**File:** `macos/GitAutoSyncMenuBar/Sources/ActivityLogView.swift`

- `Window` scene or `NSPanel`
- `List` of events with:
  - Color-coded icon per type (commit=gray, push=green, error=red)
  - Repo path (abbreviated)
  - Message
  - Relative timestamp
- Auto-scrolls to bottom on new events
- Search/filter bar (optional, post-MVP)

### Step 3.6: Notifications

**File:** `macos/GitAutoSyncMenuBar/Sources/NotificationManager.swift`

- Request notification permission on first launch
- On `error` events from socket: fire `UNUserNotificationCenter` notification
- Notification body: repo path + error message
- Click action: bring app to front, open Activity Log
- Debounce: max 1 notification per repo per 60 seconds

### Step 3.7: Launch at Login

- Toggle in menu writes to `SMAppService.mainApp`
- Read current state on app launch to set toggle position
- Requires app to be in `/Applications` or signed for sandbox

---

## Implementation Order

```
Phase 1 (Go daemon fixes)           Phase 2 (Socket)           Phase 3 (macOS app)
─────────────────────────           ────────────────           ────────────────────
1.1 Shutdown channel ──────┐
1.2 Fix panic             │
1.3 Fix env duplication   │
1.4 SIGHUP reload ────────┤
1.5 Event emitter ─────────┼──→ 2.1 Socket server ──┐
                           │    2.2 Command handling │
                           └──→ 2.3 Wire into daemon─┤
                                2.4 CLI integration   │
                                                      └──→ 3.1 Xcode project
                                                           3.2 DaemonClient
                                                           3.3 App state
                                                           3.4 Menu bar UI
                                                           3.5 Activity Log
                                                           3.6 Notifications
                                                           3.7 Launch at Login
```

Steps within each phase are sequential. Phases 1→2→3 are sequential (each builds on the prior). Steps 1.1–1.3 can be done in any order but must all complete before 1.4.

---

## Testing Strategy

| Step | Test approach |
|------|--------------|
| 1.1 Shutdown | Unit test: create watcher, cancel context, verify goroutine exits and resources freed |
| 1.2 Panic fix | Unit test: corrupt config file, verify daemon logs error and retries |
| 1.3 Env fix | Unit test: verify `toEnvString` returns no duplicates |
| 1.4 SIGHUP | Integration test: start daemon, modify config, send SIGHUP, verify new repo watched |
| 1.5 Events | Unit test: mock emitter, run autosync, verify events emitted |
| 2.1 Socket | Integration test: start server, connect client, verify events received |
| 2.2 Commands | Integration test: send `list`/`add`/`remove` over socket, verify responses |
| 3.x macOS | Manual testing + XCTest for DaemonClient (mock socket) and AppState |

---

## Open Questions

1. **SwiftNIO vs Foundation for socket I/O?** SwiftNIO is more robust but adds a dependency. Foundation's `InputStream`/`OutputStream` work but are callback-based and less ergonomic with async/await. Leaning SwiftNIO.
2. **App distribution:** Direct download `.app` bundle? Homebrew cask? Both? This affects signing and notarization requirements.
3. **Should the daemon log to a file in addition to socket events?** Useful for debugging when no client is connected. Could write to `~/.local/share/git-auto-sync/daemon.log` with rotation.
