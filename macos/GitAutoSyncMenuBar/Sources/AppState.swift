import Foundation
import SwiftUI

@Observable
final class AppState {
    var repos: [RepoState] = []
    var events: [DaemonEvent] = []
    var connectionState: ConnectionState = .disconnected
    var daemonRunning = false
    var selectedRepoPath: String? = nil
    var sidebarSearch: String = ""

    private let client = DaemonClient()
    private var pollTask: Task<Void, Never>?
    private var eventTask: Task<Void, Never>?
    private let maxEvents = 500

    init() {
        startPolling()
        startMessageStream()
    }

    var repoCount: Int { repos.count }

    var summary: String {
        if !daemonRunning { return "Daemon not running" }
        if repos.isEmpty { return "No repositories" }
        let errs = repos.filter { $0.status == "error" || $0.status == "conflict" || $0.status == "preflight" }.count
        if errs > 0 { return "\(errs) repo\(errs == 1 ? "" : "s") need attention" }
        return "\(repos.count) repo\(repos.count == 1 ? "" : "s") monitored"
    }

    var hasError: Bool {
        repos.contains { $0.status == "error" || $0.status == "conflict" || $0.status == "preflight" }
    }

    var anySyncing: Bool {
        repos.contains { $0.status == "syncing" }
    }

    private func startPolling() {
        pollTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                await self?.refreshStatus()
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }

    private func startMessageStream() {
        eventTask = Task { @MainActor [weak self] in
            guard let self else { return }
            self.client.connect()
            for await msg in self.client.messages {
                self.handleMessage(msg)
            }
        }
    }

    @MainActor
    func refreshStatus() async {
        let response = await client.sendStatusCommand()
        if let response {
            daemonRunning = response.daemon == "running"
            connectionState = .connected
            if let snap = response.repos {
                applySnapshot(snap)
            }
        } else {
            connectionState = .disconnected
            daemonRunning = false
        }
    }

    @MainActor
    private func handleMessage(_ msg: DaemonMessage) {
        switch msg {
        case .hello(let repos):
            connectionState = .connected
            daemonRunning = true
            applySnapshot(repos)
        case .state(let rs):
            upsert(rs)
        case .event(let e):
            ingestEvent(e)
        }
    }

    @MainActor
    private func applySnapshot(_ snapshot: [RepoState]) {
        repos = snapshot.sorted { $0.path.lowercased() < $1.path.lowercased() }
        if let selected = selectedRepoPath, !repos.contains(where: { $0.path == selected }) {
            selectedRepoPath = repos.first?.path
        }
        if selectedRepoPath == nil {
            selectedRepoPath = repos.first?.path
        }
    }

    @MainActor
    private func upsert(_ rs: RepoState) {
        if let idx = repos.firstIndex(where: { $0.path == rs.path }) {
            repos[idx] = rs
        } else {
            repos.append(rs)
            repos.sort { $0.path.lowercased() < $1.path.lowercased() }
        }
        if selectedRepoPath == nil { selectedRepoPath = rs.path }
    }

    @MainActor
    private func ingestEvent(_ event: DaemonEvent) {
        events.append(event)
        if events.count > maxEvents {
            events.removeFirst(events.count - maxEvents)
        }

        if event.type == "error" || event.type == "conflict" || event.type == "preflight" {
            NotificationManager.shared.sendNotification(
                repo: event.repo,
                message: event.message ?? "Unknown error"
            )
        }
    }

    @MainActor
    func addRepo(_ path: String) async {
        _ = await client.sendCommand(SocketCommand(cmd: "add", repo: path))
        await refreshStatus()
        selectedRepoPath = path
    }

    @MainActor
    func removeRepo(_ path: String) async {
        _ = await client.sendCommand(SocketCommand(cmd: "remove", repo: path))
        if selectedRepoPath == path { selectedRepoPath = nil }
        await refreshStatus()
    }

    @MainActor
    func syncNow(_ path: String) async {
        _ = await client.sendCommand(SocketCommand(cmd: "sync_now", repo: path))
    }

    @MainActor
    func setPaused(_ path: String, paused: Bool) async {
        let cmd = paused ? "pause" : "resume"
        _ = await client.sendCommand(SocketCommand(cmd: cmd, repo: path))
    }

    @MainActor
    func saveSettings(_ path: String, pollInterval: Int?, batchWindow: Int?) async {
        var s: [String: String] = [:]
        if let pi = pollInterval { s["pollInterval"] = String(pi) }
        if let bw = batchWindow { s["batchWindow"] = String(bw) }
        guard !s.isEmpty else { return }
        _ = await client.sendCommand(SocketCommand(cmd: "set_settings", repo: path, settings: s))
    }

    func eventsFor(repo: String) -> [DaemonEvent] {
        events.filter { $0.repo == repo }
    }

    var filteredRepos: [RepoState] {
        let q = sidebarSearch.trimmingCharacters(in: .whitespaces).lowercased()
        if q.isEmpty { return repos }
        return repos.filter {
            $0.name.lowercased().contains(q) ||
            $0.path.lowercased().contains(q) ||
            ($0.branch?.lowercased().contains(q) ?? false)
        }
    }
}
