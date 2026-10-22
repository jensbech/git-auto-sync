import AppKit
import Foundation
import Observation
import ServiceManagement
import SwiftUI

enum DaemonConnection: Equatable {
    case connecting
    case connected
    case daemonNotRunning
    case incompatible(daemonProtocol: Int)
}

struct AppNotice: Identifiable, Equatable {
    let id = UUID()
    let text: String
    let isError: Bool
}

struct RepoSettings: Equatable {
    var pollInterval: Int
    var batchWindow: Int
}

@MainActor
@Observable
final class AppState {
    var repos: [RepoState] = []
    var connection: DaemonConnection = .connecting
    var notice: AppNotice?
    var daemonRunning = false
    var selectedRepoPath: String? = nil
    var sidebarSearch: String = ""
    private(set) var allEvents: [DaemonEvent] = []
    private(set) var daemonVersion: String?
    private(set) var isStartingDaemon = false
    private(set) var launchAtLogin = false
    private(set) var addRepoRequest = 0
    private(set) var focusSearchRequest = 0
    private(set) var removeSelectedRequest = 0

    private var eventsByRepo: [String: [DaemonEvent]] = [:]

    @ObservationIgnored var openDashboardWindow: (() -> Void)?
    @ObservationIgnored private let client: DaemonClient
    @ObservationIgnored private let notifier: any RepoNotifying
    @ObservationIgnored private let launcher: DaemonLauncher
    @ObservationIgnored private let eventStore: EventStore
    @ObservationIgnored private var messageTask: Task<Void, Never>?
    @ObservationIgnored private var backfillTask: Task<Void, Never>?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private var seenEventIDs: Set<UInt64> = []
    @ObservationIgnored private(set) var lastSeenEventID: UInt64 = 0
    @ObservationIgnored private var problemKeys: [String: String] = [:]
    @ObservationIgnored private var hasSnapshot = false

    let maxEvents = 2000
    let initialBackfill = 500

    static let readOnlyCommands: Set<String> = [
        DaemonCommand.status, DaemonCommand.list, DaemonCommand.events,
        DaemonCommand.getSettings, DaemonCommand.version,
    ]
    static let longCommandTimeout: TimeInterval = 150

    private enum CommandOutcome {
        case ok(DaemonResponse)
        case failed(DaemonResponse)
        case unreachable(DaemonRequestError)
    }

    init(
        client: DaemonClient = DaemonClient(),
        notifier: (any RepoNotifying)? = nil,
        launcher: DaemonLauncher? = nil,
        eventStore: EventStore? = nil
    ) {
        self.client = client
        self.notifier = notifier ?? NotificationManager.shared
        self.launcher = launcher ?? Self.defaultLauncher(for: client)
        self.eventStore = eventStore ?? EventStore(socketPath: client.socketPath)
        launchAtLogin = Self.currentLaunchAtLogin()
        restoreEvents()
        startMessageStream()
        client.start()
        let center = NotificationCenter.default
        observers.append(center.addObserver(
            forName: NSApplication.didBecomeActiveNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.refreshLaunchAtLogin()
            }
        })
        observers.append(center.addObserver(
            forName: NSApplication.willTerminateNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.eventStore.saveNow()
            }
        })
    }

    deinit {
        messageTask?.cancel()
        backfillTask?.cancel()
        client.stop()
        for observer in observers {
            NotificationCenter.default.removeObserver(observer)
        }
    }

    private static func defaultLauncher(for client: DaemonClient) -> DaemonLauncher {
        if client.socketPath != DaemonClient.standardSocketPath {
            return .disabled("the app is using a custom daemon socket (\(client.socketPath)), so start that daemon yourself")
        }
        return .live
    }

    var repoCount: Int { repos.count }

    var selectedRepo: RepoState? {
        guard let path = selectedRepoPath else { return nil }
        return repos.first { $0.path == path }
    }

    func requestAddRepository() {
        addRepoRequest &+= 1
    }

    func requestFocusSearch() {
        focusSearchRequest &+= 1
    }

    func requestRemoveSelected() {
        guard selectedRepo != nil else { return }
        removeSelectedRequest &+= 1
    }

    func syncSelected() async {
        guard let repo = selectedRepo else { return }
        await syncNow(repo.path)
    }

    func togglePauseSelected() async {
        guard let repo = selectedRepo else { return }
        await setPaused(repo.path, paused: !repo.paused)
    }

    var summary: String {
        switch connection {
        case .connecting: return "Connecting…"
        case .daemonNotRunning: return "Daemon not running"
        case .incompatible: return "Daemon is outdated"
        case .connected: break
        }
        if repos.isEmpty { return "No repositories" }
        let attention = repos.filter(Self.needsAttention).count
        if attention > 0 {
            return attention == 1 ? "1 repo needs attention" : "\(attention) repos need attention"
        }
        let offline = repos.filter { $0.statusValue == .offline }.count
        if offline > 0 {
            return "\(offline) repo\(offline == 1 ? "" : "s") offline"
        }
        return "\(repos.count) repo\(repos.count == 1 ? "" : "s") monitored"
    }

    var hasError: Bool {
        repos.contains(where: Self.needsAttention)
    }

    var anySyncing: Bool {
        repos.contains { $0.statusValue == .syncing }
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

    func eventsFor(repo: String) -> [DaemonEvent] {
        eventsByRepo[repo] ?? []
    }

    func dismissNotice() {
        notice = nil
    }

    func showDashboard(repo: String? = nil) {
        if let repo { selectedRepoPath = repo }
        if let open = openDashboardWindow {
            open()
        } else if let window = NSApp.windows.first(where: { $0.identifier?.rawValue.hasPrefix("dashboard") == true }) {
            window.makeKeyAndOrderFront(nil)
        } else {
            Self.openDashboardFromWindowMenu()
        }
        NSApp.activate()
    }

    @discardableResult
    private static func openDashboardFromWindowMenu() -> Bool {
        guard let menu = NSApp.windowsMenu ?? NSApp.mainMenu else { return false }
        guard let item = findItem(titled: "Git Auto Sync", in: menu), let action = item.action else { return false }
        return NSApp.sendAction(action, to: item.target, from: item)
    }

    private static func findItem(titled title: String, in menu: NSMenu) -> NSMenuItem? {
        for item in menu.items {
            if item.title == title, item.action != nil, item.submenu == nil { return item }
            if let submenu = item.submenu, let found = findItem(titled: title, in: submenu) { return found }
        }
        return nil
    }

    func refreshStatus() async {
        guard case .ok(let response) = await perform(SocketCommand(cmd: DaemonCommand.status)),
              let status = response.result(StatusResult.self) else { return }
        applySnapshot(status.repos)
    }

    func addRepos(_ urls: [URL]) async {
        var added: [String] = []
        var existing: [String] = []
        var failures: [String] = []

        for url in urls {
            let path = url.standardizedFileURL.path
            let outcome = await perform(SocketCommand(cmd: DaemonCommand.add, repo: path), timeout: 60)
            switch outcome {
            case .ok(let response):
                let fields = response.resultStrings
                let root = fields["repo"] ?? path
                if fields["status"] == "already_exists" {
                    existing.append(root)
                } else {
                    added.append(root)
                }
            case .failed(let response) where response.code == "already_exists":
                existing.append(response.resultStrings["repo"] ?? path)
            case .failed(let response):
                failures.append(Self.failureMessage(response, repo: path))
            case .unreachable(let error):
                failures.append(failureMessage(error))
            }
            if case .unreachable = outcome { break }
        }

        if !added.isEmpty || !existing.isEmpty {
            await refreshStatus()
            selectedRepoPath = added.last ?? existing.last
        }

        if !failures.isEmpty {
            post(failures.joined(separator: "\n"), isError: true)
        } else if added.count == 1 {
            post("Now monitoring \(Self.displayName(added[0]))", isError: false)
        } else if added.count > 1 {
            post("Now monitoring \(added.count) repositories", isError: false)
        } else if let path = existing.last {
            post("Already monitoring \(Self.displayName(path))", isError: false)
        }
    }

    func removeRepo(_ path: String) async {
        let outcome = await perform(SocketCommand(cmd: DaemonCommand.remove, repo: path), timeout: 60)
        let removed: Bool
        switch outcome {
        case .ok: removed = true
        case .failed(let response): removed = response.code == "not_found"
        case .unreachable: removed = false
        }
        guard removed else {
            report(outcome, repo: path)
            return
        }
        forgetRepo(path)
        post("Stopped monitoring \(Self.displayName(path)). Files were not touched.", isError: false)
        await refreshStatus()
    }

    func syncNow(_ path: String, force: Bool = false) async {
        var cmd = SocketCommand(cmd: DaemonCommand.syncNow, repo: path)
        if force { cmd.force = true }
        report(await perform(cmd), repo: path)
    }

    func syncAll() async {
        report(await perform(SocketCommand(cmd: DaemonCommand.syncAll)), repo: nil)
    }

    func setPaused(_ path: String, paused: Bool) async {
        let cmd = SocketCommand(cmd: paused ? DaemonCommand.pause : DaemonCommand.resume, repo: path)
        report(await perform(cmd), repo: path)
    }

    func setAllPaused(_ paused: Bool) async {
        let cmd = SocketCommand(cmd: paused ? DaemonCommand.pauseAll : DaemonCommand.resumeAll)
        report(await perform(cmd), repo: nil)
    }

    func abortRebase(_ path: String) async {
        let outcome = await perform(SocketCommand(cmd: DaemonCommand.abortRebase, repo: path), timeout: Self.longCommandTimeout)
        if report(outcome, repo: path) {
            post("Rebase aborted in \(Self.displayName(path))", isError: false)
        }
    }

    func saveSettings(_ path: String, _ settings: RepoSettings) async -> Bool {
        let values = [
            "pollInterval": String(settings.pollInterval),
            "batchWindow": String(settings.batchWindow),
        ]
        let outcome = await perform(SocketCommand(cmd: DaemonCommand.setSettings, repo: path, settings: values))
        return report(outcome, repo: path)
    }

    func repoSettings(_ path: String) async -> [String: String]? {
        guard case .ok(let response) = await perform(SocketCommand(cmd: DaemonCommand.getSettings, repo: path)) else {
            return nil
        }
        return response.resultStrings
    }

    func fetchSettings(_ path: String) async -> [String: String]? {
        await repoSettings(path)
    }

    func startDaemon() async {
        guard !isStartingDaemon else { return }
        isStartingDaemon = true
        defer { isStartingDaemon = false }

        if launcher.disabledReason == nil, !launcher.isServiceInstalled, launcher.cliPath != nil {
            post("The background service is not installed. Installing it with `git-auto-sync daemon enable`…", isError: false)
        }

        let outcome = await launcher.start()
        if case .failed(let failure) = outcome {
            post("Could not start the daemon: \(failure)", isError: true)
            return
        }

        client.reconnectNow()
        for attempt in 0..<40 {
            switch connection {
            case .connected:
                if outcome == .installed {
                    post("Background service installed. The daemon now starts at login.", isError: false)
                } else {
                    post("Daemon started", isError: false)
                }
                return
            case .incompatible:
                post(Self.outdatedDaemonMessage, isError: true)
                return
            default:
                break
            }
            if attempt % 4 == 3 { client.reconnectNow() }
            try? await Task.sleep(for: .milliseconds(250))
        }
        post("The daemon did not start. Check ~/Library/Logs/git-auto-sync/daemon.log", isError: true)
    }

    func refreshLaunchAtLogin() {
        let current = Self.currentLaunchAtLogin()
        if current != launchAtLogin { launchAtLogin = current }
    }

    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            if enabled {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
        } catch {
            post("Could not change Launch at Login: \(error.localizedDescription)", isError: true)
        }
        refreshLaunchAtLogin()
        if enabled, SMAppService.mainApp.status == .requiresApproval {
            post("Allow Git Auto Sync in System Settings > General > Login Items", isError: false)
        }
    }

    private static func currentLaunchAtLogin() -> Bool {
        let status = SMAppService.mainApp.status
        return status == .enabled || status == .requiresApproval
    }

    private func startMessageStream() {
        let stream = client.messages
        messageTask = Task { [weak self] in
            for await message in stream {
                self?.handle(message)
            }
        }
    }

    func handle(_ message: DaemonMessage) {
        switch message {
        case .hello(let hello):
            setConnection(.connected, running: true)
            if daemonVersion != hello.version { daemonVersion = hello.version }
            applySnapshot(hello.repos)
            backfill(lastEventId: hello.lastEventId)
        case .state(let repoState):
            upsert(repoState)
        case .event(let event):
            ingest([event])
        case .disconnected:
            setConnection(.connecting, running: daemonRunning)
        case .unavailable:
            setConnection(.daemonNotRunning, running: false)
        case .incompatible(let version):
            setConnection(.incompatible(daemonProtocol: version), running: true)
        }
    }

    private func setConnection(_ value: DaemonConnection, running: Bool) {
        if connection != value { connection = value }
        if daemonRunning != running { daemonRunning = running }
    }

    private func applySnapshot(_ snapshot: [RepoState]) {
        let sorted = snapshot.sorted { $0.path.lowercased() < $1.path.lowercased() }
        let notify = hasSnapshot
        for repoState in sorted {
            trackTransition(repoState, notify: notify)
        }
        let newPaths = Set(sorted.map(\.path))
        let oldPaths = Set(repos.map(\.path))
        problemKeys = problemKeys.filter { newPaths.contains($0.key) }

        if repos != sorted { repos = sorted }

        if let selected = selectedRepoPath, oldPaths.contains(selected), !newPaths.contains(selected) {
            selectedRepoPath = repos.first?.path
        } else if selectedRepoPath == nil, !hasSnapshot {
            selectedRepoPath = repos.first?.path
        }
        hasSnapshot = true
    }

    private func upsert(_ repoState: RepoState) {
        trackTransition(repoState, notify: hasSnapshot)
        if let idx = repos.firstIndex(where: { $0.path == repoState.path }) {
            if repos[idx] != repoState { repos[idx] = repoState }
        } else {
            repos.append(repoState)
            repos.sort { $0.path.lowercased() < $1.path.lowercased() }
            if selectedRepoPath == nil { selectedRepoPath = repoState.path }
        }
    }

    private func forgetRepo(_ path: String) {
        repos.removeAll { $0.path == path }
        problemKeys[path] = nil
        notifier.clear(repo: path)
        if selectedRepoPath == path { selectedRepoPath = repos.first?.path }
    }

    private func trackTransition(_ repoState: RepoState, notify: Bool) {
        let key = Self.problemKey(repoState)
        if key == nil, Self.isTransient(repoState) { return }
        let before = problemKeys[repoState.path] ?? ""
        problemKeys[repoState.path] = key ?? ""

        guard notify else { return }
        if let key {
            if !Self.isSameProblem(before, key) {
                notifier.notify(repo: repoState.path, body: Self.notificationBody(repoState))
            }
        } else if !before.isEmpty {
            if before == "conflict", repoState.statusValue == .ok {
                notifier.notify(repo: repoState.path, body: "Conflict resolved. Syncing again.")
            } else {
                notifier.clear(repo: repoState.path)
            }
        }
    }

    static func isTransient(_ repoState: RepoState) -> Bool {
        switch repoState.statusValue {
        case .syncing, .unknown, .idle, .paused, .offline:
            return true
        case .error:
            let code = repoState.errorCode ?? ""
            return code == "network" || code == "timeout"
        case .ok, .conflict, .preflight:
            return false
        }
    }

    static func isSameProblem(_ before: String, _ after: String) -> Bool {
        if before == after { return true }
        guard !before.isEmpty, !after.isEmpty else { return false }
        let a = before.split(separator: ":", maxSplits: 1, omittingEmptySubsequences: false)
        let b = after.split(separator: ":", maxSplits: 1, omittingEmptySubsequences: false)
        guard a.first == b.first else { return false }
        let codeA = a.count > 1 ? a[1] : ""
        let codeB = b.count > 1 ? b[1] : ""
        return codeA.isEmpty || codeB.isEmpty
    }

    private func backfill(lastEventId: UInt64) {
        if lastEventId < lastSeenEventID {
            resetEvents()
        }
        guard lastEventId > lastSeenEventID else { return }

        var cmd = SocketCommand(cmd: DaemonCommand.events)
        if lastSeenEventID > 0 {
            cmd.since = lastSeenEventID
            cmd.limit = maxEvents
        } else {
            cmd.limit = initialBackfill
        }

        backfillTask?.cancel()
        backfillTask = Task { [weak self] in
            guard let self else { return }
            let outcome = await self.perform(cmd)
            guard !Task.isCancelled, case .ok(let response) = outcome else { return }
            let events = await Self.decodeEvents(response)
            guard !Task.isCancelled else { return }
            self.ingest(events)
        }
    }

    nonisolated static func decodeEvents(_ response: DaemonResponse) async -> [DaemonEvent] {
        await Task.detached(priority: .userInitiated) {
            response.events
        }.value
    }

    private func restoreEvents() {
        let cached = eventStore.load()
        guard !cached.events.isEmpty else { return }
        ingest(cached.events, persist: false)
        lastSeenEventID = cached.lastSeen
    }

    private func resetEvents() {
        backfillTask?.cancel()
        allEvents = []
        eventsByRepo = [:]
        seenEventIDs = []
        lastSeenEventID = 0
        eventStore.scheduleSave(events: [], lastSeen: 0)
    }

    func ingest(_ batch: [DaemonEvent], persist: Bool = true) {
        var fresh: [DaemonEvent] = []
        for event in batch where !seenEventIDs.contains(event.id) {
            seenEventIDs.insert(event.id)
            fresh.append(event)
        }
        guard let firstFresh = fresh.first else { return }

        let inOrder = (allEvents.last?.id ?? 0) < firstFresh.id
            && zip(fresh, fresh.dropFirst()).allSatisfy { $0.id < $1.id }

        var events = allEvents
        events.append(contentsOf: fresh)
        if !inOrder {
            events.sort { $0.id < $1.id }
        }

        var trimmed = false
        if events.count > maxEvents {
            let overflow = events.count - maxEvents
            for event in events.prefix(overflow) {
                seenEventIDs.remove(event.id)
            }
            events.removeFirst(overflow)
            trimmed = true
        }

        lastSeenEventID = max(lastSeenEventID, events.last?.id ?? 0)
        allEvents = events

        if inOrder && !trimmed {
            var byRepo = eventsByRepo
            for event in fresh {
                byRepo[event.repo, default: []].append(event)
            }
            eventsByRepo = byRepo
        } else {
            eventsByRepo = Dictionary(grouping: events, by: \.repo)
        }

        if persist {
            eventStore.scheduleSave(events: events, lastSeen: lastSeenEventID)
        }
    }

    private func perform(_ command: SocketCommand, timeout: TimeInterval? = nil) async -> CommandOutcome {
        var result = await client.request(command, timeout: timeout)
        if case .failure(let error) = result, error != .timeout, error != .incompatible, Self.readOnlyCommands.contains(command.cmd) {
            try? await Task.sleep(for: .milliseconds(300))
            result = await client.request(command, timeout: timeout)
        }
        if case .failure(.incompatible) = result {
            setConnection(.incompatible(daemonProtocol: 1), running: true)
        }
        switch result {
        case .success(let response):
            return response.ok ? .ok(response) : .failed(response)
        case .failure(let error):
            return .unreachable(error)
        }
    }

    @discardableResult
    private func report(_ outcome: CommandOutcome, repo: String?) -> Bool {
        switch outcome {
        case .ok:
            return true
        case .failed(let response):
            post(Self.failureMessage(response, repo: repo), isError: true)
        case .unreachable(let error):
            post(failureMessage(error), isError: true)
        }
        return false
    }

    private func failureMessage(_ error: DaemonRequestError) -> String {
        if error == .timeout, connection == .connected {
            return "The daemon is busy and did not answer in time. The command may still finish."
        }
        return Self.failureMessage(error)
    }

    private func post(_ text: String, isError: Bool) {
        notice = AppNotice(text: text, isError: isError)
    }

    static func needsAttention(_ repoState: RepoState) -> Bool {
        switch repoState.statusValue {
        case .error, .conflict, .preflight: return true
        default: return false
        }
    }

    static func problemKey(_ repoState: RepoState) -> String? {
        let code = repoState.errorCode ?? ""
        switch repoState.statusValue {
        case .conflict:
            return "conflict"
        case .preflight:
            return "preflight:\(code)"
        case .error:
            if code == "network" || code == "timeout" { return nil }
            return "error:\(code)"
        default:
            if code == "xcode_license" || code == "auth" { return "error:\(code)" }
            return nil
        }
    }

    static func notificationBody(_ repoState: RepoState) -> String {
        switch repoState.statusValue {
        case .conflict:
            let count = repoState.conflictFiles?.count ?? 0
            let files = count == 0 ? "" : " in \(count) file\(count == 1 ? "" : "s")"
            return "Rebase conflict\(files). Local commits are kept; resolve it to resume syncing."
        case .preflight:
            return repoState.preflight ?? repoState.lastError ?? "Syncing is blocked until the repository is fixed."
        default:
            return errorDescription(code: repoState.errorCode, fallback: repoState.lastError)
        }
    }

    static func errorDescription(code: String?, fallback: String?) -> String {
        switch code {
        case "xcode_license":
            return "The Xcode license has not been accepted. Run `sudo xcodebuild -license accept`."
        case "auth":
            return "Authentication with the remote failed. Check your git credentials."
        case "git_missing":
            return "git was not found. Install the Xcode command line tools."
        case "no_author":
            return "No git author is configured. Set user.name and user.email."
        case "push_rejected":
            return fallback ?? "The remote rejected the push."
        case "lock":
            return fallback ?? "Another git process is holding the repository lock."
        default:
            return fallback ?? "Sync failed."
        }
    }

    static func failureMessage(_ response: DaemonResponse, repo: String?) -> String {
        let target = repo.map(abbreviate) ?? "the repository"
        switch response.code {
        case "not_a_repo":
            return "Not a git repository: \(target)"
        case "not_watching":
            return "\(target) is not being watched"
        case "not_found":
            return "Not monitoring \(target)"
        case "already_exists":
            return "Already monitoring \(target)"
        case "unknown_command":
            return "The daemon does not support this command. Update it with `just install`."
        case "bad_request":
            guard let error = response.error, let first = error.first else { return "The daemon rejected the request" }
            return first.uppercased() + error.dropFirst()
        case "missing_repo":
            return "The daemon rejected the request: \(response.error ?? "bad request")"
        default:
            return response.error ?? "The command failed"
        }
    }

    static func failureMessage(_ error: DaemonRequestError) -> String {
        switch error {
        case .unreachable: return "The daemon is not running"
        case .timeout: return "The daemon did not respond in time"
        case .incompatible: return Self.outdatedDaemonMessage
        case .io(let message): return message
        }
    }

    static let outdatedDaemonMessage = "The running daemon is outdated (protocol 1). Run `just install`, then `git-auto-sync daemon enable` to replace it."

    static func displayName(_ path: String) -> String {
        URL(fileURLWithPath: path).lastPathComponent
    }

    static func abbreviate(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path == home { return "~" }
        if path.hasPrefix(home + "/") { return "~" + path.dropFirst(home.count) }
        return path
    }
}
