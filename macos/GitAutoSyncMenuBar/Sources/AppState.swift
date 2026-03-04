import Foundation
import SwiftUI

@Observable
final class AppState {
    var repos: [RepoStatus] = []
    var events: [DaemonEvent] = []
    var connectionState: ConnectionState = .disconnected
    var daemonRunning = false

    private let client = DaemonClient()
    private var pollTask: Task<Void, Never>?
    private var eventTask: Task<Void, Never>?
    private let maxEvents = 200

    init() {
        startPolling()
        startEventStream()
    }

    private func startPolling() {
        pollTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                await self?.refreshStatus()
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }

    private func startEventStream() {
        eventTask = Task { @MainActor [weak self] in
            guard let self else { return }
            self.client.connect()
            for await event in self.client.events {
                self.handleEvent(event)
            }
        }
    }

    @MainActor
    func refreshStatus() async {
        let response = await client.sendCommandOneShot(SocketCommand(cmd: "status"))
        if let response {
            daemonRunning = response.daemon == "running"
            connectionState = .connected
            if let statusRepos = response.repos {
                var updated: [RepoStatus] = []
                for sr in statusRepos {
                    if let existing = repos.first(where: { $0.path == sr.path }) {
                        updated.append(existing)
                    } else {
                        updated.append(RepoStatus(
                            path: sr.path,
                            lastSync: nil,
                            status: sr.status == "ok" ? .ok : .error
                        ))
                    }
                }
                repos = updated
            }
        } else {
            connectionState = .disconnected
            daemonRunning = false
        }
    }

    @MainActor
    private func handleEvent(_ event: DaemonEvent) {
        events.append(event)
        if events.count > maxEvents {
            events.removeFirst(events.count - maxEvents)
        }

        if let idx = repos.firstIndex(where: { $0.path == event.repo }) {
            switch event.type {
            case "commit":
                repos[idx].lastSync = event.date
                repos[idx].status = .ok
                repos[idx].lastError = nil
                repos[idx].lastActivity = "committed"
            case "push":
                repos[idx].lastSync = event.date
                repos[idx].status = .ok
                repos[idx].lastError = nil
                repos[idx].lastActivity = "pushed"
            case "synced":
                repos[idx].lastSync = event.date
                repos[idx].status = .ok
                repos[idx].lastError = nil
                repos[idx].lastActivity = "synced"
            case "error":
                repos[idx].status = .error
                repos[idx].lastError = event.message
                repos[idx].lastActivity = nil
            default:
                break
            }
        }

        if event.type == "error" {
            NotificationManager.shared.sendNotification(
                repo: event.repo,
                message: event.message ?? "Unknown error"
            )
        }
    }

    @MainActor
    func addRepo(_ path: String) async {
        _ = await client.sendCommandOneShot(SocketCommand(cmd: "add", repo: path))
        await refreshStatus()
    }

    @MainActor
    func removeRepo(_ path: String) async {
        _ = await client.sendCommandOneShot(SocketCommand(cmd: "remove", repo: path))
        await refreshStatus()
    }
}
