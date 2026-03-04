# git-auto-sync: macOS Menu Bar Companion App — Design

Date: 2026-03-04
Status: Draft (approved through Section 3, implementation not started)

---

## Goals

- Add a native macOS menu bar companion app as an **additive, optional** layer on top of the existing CLI/daemon
- Do **not** rewrite or replace the Go daemon — it must remain fully functional standalone
- Fix known bugs in the daemon as part of this work
- The Unix socket the app uses must also work on Linux (POSIX standard)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  macOS Menu Bar App (Swift/SwiftUI)            [optional]   │
│  - MenuBarExtra                                             │
│  - Views: RepoList, ActivityLog, Settings                   │
│  - DaemonClient (reads/writes unix socket)                  │
└────────────────────────┬────────────────────────────────────┘
                         │ ~/.local/share/git-auto-sync/daemon.sock
                         │ (JSON newline-delimited protocol)
┌────────────────────────▼────────────────────────────────────┐
│  go daemon  (existing, minimally modified)                  │
│  + SocketServer (new, ~150 lines in common/socket.go)       │
│    - streams events: {type, repo, message, ts}              │
│    - accepts commands: add, remove, list, status            │
│  + Bug fixes (see below)                                    │
└─────────────────────────────────────────────────────────────┘
```

**Coupling principle:** The daemon works 100% without any socket client connected. The socket server starts in the background; any failure is non-fatal and logged. The macOS app is purely additive.

---

## Part 1: Daemon Changes (Go)

### New file: `common/socket.go`

A background goroutine that:

1. Creates a Unix socket at `~/.local/share/git-auto-sync/daemon.sock`
2. Accepts multiple concurrent connections (one per client)
3. Broadcasts newline-delimited JSON events to all connected clients

**Event types streamed to clients:**
```json
{"type": "commit", "repo": "/Users/jens/notes", "message": "M file.md", "ts": 1234567890}
{"type": "push",   "repo": "/Users/jens/notes", "status": "ok", "ts": 1234567890}
{"type": "error",  "repo": "/Users/jens/notes", "message": "rebase failed", "ts": 1234567890}
{"type": "status", "repos": [{"path": "...", "lastSync": ..., "status": "ok"}], "daemon": "running", "ts": 1234567890}
```

**Commands accepted from clients (newline-delimited JSON):**
```json
{"cmd": "list"}
{"cmd": "add",    "repo": "/path/to/repo"}
{"cmd": "remove", "repo": "/path/to/repo"}
{"cmd": "status"}
```

**Socket lifecycle:**
- On daemon start: remove stale socket if exists, then bind and listen
- On daemon stop: close socket, remove socket file
- Socket path: `~/.local/share/git-auto-sync/daemon.sock` (XDG data home)

### Bug fixes (bundled into this work)

These are existing bugs found in the codebase that will be fixed as part of this effort:

| Bug | Location | Fix |
|-----|----------|-----|
| Panic on config read error | `daemon/main.go:24` | Log error + graceful shutdown instead of `panic(err)` |
| Env var duplication | `commit.go:147` | `vals = append(vals, repoConfig.Env...)` duplicates same list — fix to not double-append |
| Goroutine leak | `daemon/main.go:62-65` | Add shutdown channel to `watchForChanges` so goroutines can be stopped cleanly |
| No SIGHUP handling | `daemon/main.go:77` | Add signal handler to reload config on SIGHUP |
| No activity logging | `autosync.go:10` | Emit structured events to socket (and optionally a log file) on commit/push/error |

---

## Part 2: macOS Menu Bar App (Swift/SwiftUI)

### Project location

`macos/GitAutoSyncMenuBar/` — a separate Xcode project inside the repo. Built and distributed independently from the Go daemon.

### Menu bar icon

SF Symbols used for state indication:
- Idle: `arrow.triangle.2.circlepath` (gray)
- Syncing: animated `arrow.triangle.2.circlepath` (blue)
- Error: `exclamationmark.triangle` (yellow/red)

### Menu structure

```
● git-auto-sync  [status dot: green/yellow/red]
─────────────────────────────────────────────
  /Users/jens/notes        Last sync: 2m ago  ✓
  /Users/jens/dotfiles     Last sync: 5m ago  ✓
  /Users/jens/work         ERROR: rebase failed ⚠
─────────────────────────────────────────────
  Activity Log...          (opens popover/window)
─────────────────────────────────────────────
  Add Repository...
  Remove Repository...
─────────────────────────────────────────────
  Start Daemon / Stop Daemon
  Quit
```

### Views

**RepoList** (inline in menu): Shows each monitored repo with last-sync time and status badge.

**Activity Log** (popover or panel window):
- Scrollable list of recent events (last 200)
- Color-coded by type: commit = gray, push = green, error = red
- Live-updating as socket streams new events
- Timestamp on each entry

**Add Repository**: Opens `NSOpenPanel` folder picker → sends `{"cmd":"add","repo":"..."}` over socket → refreshes repo list.

**Remove Repository**: Shows submenu of current repos → sends `{"cmd":"remove","repo":"..."}` → refreshes list.

### Notifications

Uses `UserNotifications` framework. When an `error` event arrives over the socket, fires a native macOS notification with the repo path and error message. User can click to open the Activity Log.

### Launch at login

Optional toggle in menu → registers/unregisters itself via `SMAppService` (macOS 13+).

### DaemonClient (Swift)

A Swift actor/class that:
1. Connects to the Unix socket path
2. Reads newline-delimited JSON, decodes into typed event structs, publishes via `@Published` / Combine / AsyncStream
3. Writes command JSON on request
4. Auto-reconnects with backoff if daemon not running or socket disappears

---

## Tech Decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| IPC method | Unix Domain Socket | POSIX (works on Linux too), no port conflicts, fast |
| App framework | SwiftUI + MenuBarExtra | Native, first-class macOS menu bar support |
| Socket protocol | Newline-delimited JSON | Simple, debuggable with `nc`, no extra deps |
| Notifications | UserNotifications | Native macOS, no third-party needed |
| Launch at login | SMAppService | Modern macOS 13+ API |
| App location | `macos/` subdirectory | Companion, not a replacement — stays alongside Go code |

---

## Out of Scope (for now)

- Windows/Linux GUI (daemon socket works cross-platform; GUI is macOS only)
- Drag-and-drop to add repos (nice to have, post-MVP)
- Merge conflict resolution UI
- Prometheus/metrics export
- Per-repo sync interval configuration in the app (use `git config` directly for now)

---

## Next Steps

When ready to implement, invoke the `superpowers:writing-plans` skill to create a step-by-step implementation plan from this design.
