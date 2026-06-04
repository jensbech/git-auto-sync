import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct DashboardView: View {
    @Bindable var appState: AppState
    @State private var hoveredRepo: String? = nil
    @State private var dropTargeted = false

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
        } detail: {
            if let selected = selectedRepo {
                RepoDetailView(appState: appState, repo: selected)
            } else {
                EmptyDashboard(onAdd: addRepository)
            }
        }
        .navigationTitle(navigationTitle)
        .navigationSubtitle(appState.summary)
        .toolbar { toolbarContent }
        .onDrop(of: [.fileURL, .folder], isTargeted: $dropTargeted) { providers in
            handleDrop(providers)
        }
        .overlay {
            if dropTargeted {
                DropOverlay()
                    .allowsHitTesting(false)
                    .transition(.opacity)
            }
        }
        .animation(.easeOut(duration: 0.15), value: dropTargeted)
    }

    private var navigationTitle: String {
        selectedRepo?.name ?? "Git Auto Sync"
    }

    private var selectedRepo: RepoState? {
        guard let path = appState.selectedRepoPath else { return appState.filteredRepos.first }
        return appState.repos.first { $0.path == path }
    }

    private var sidebar: some View {
        VStack(spacing: 0) {
            ConnectionBar(state: appState.connectionState, daemonRunning: appState.daemonRunning, summary: appState.summary)

            Divider()

            SidebarSearchField(text: $appState.sidebarSearch)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)

            if appState.repos.isEmpty {
                emptySidebar
            } else if appState.filteredRepos.isEmpty {
                noResultsSidebar
            } else {
                List(selection: $appState.selectedRepoPath) {
                    ForEach(appState.filteredRepos) { repo in
                        SidebarRepoRow(repo: repo, hovered: hoveredRepo == repo.path)
                            .onHover { hoveredRepo = $0 ? repo.path : (hoveredRepo == repo.path ? nil : hoveredRepo) }
                            .tag(Optional(repo.path))
                            .listRowSeparator(.hidden)
                            .listRowInsets(EdgeInsets(top: 2, leading: 6, bottom: 2, trailing: 6))
                            .contextMenu { repoContextMenu(repo) }
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
            }

            Divider()

            SidebarFooter(appState: appState, selectedPath: selectedRepo?.path, onAdd: addRepository)
        }
    }

    @ViewBuilder
    private var emptySidebar: some View {
        VStack(spacing: 12) {
            Spacer()
            Image(systemName: "folder.badge.plus")
                .font(.system(size: 28, weight: .light))
                .foregroundStyle(.tertiary)
            Text("No repositories")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(.secondary)
            Text("Drop a folder here or click +")
                .font(.system(size: 11))
                .foregroundStyle(.tertiary)
                .multilineTextAlignment(.center)
            Spacer()
        }
        .frame(maxWidth: .infinity)
        .padding(.horizontal, 12)
    }

    @ViewBuilder
    private var noResultsSidebar: some View {
        VStack(spacing: 8) {
            Spacer()
            Image(systemName: "magnifyingglass")
                .font(.system(size: 22, weight: .light))
                .foregroundStyle(.tertiary)
            Text("No matches")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
            Button("Clear search") { appState.sidebarSearch = "" }
                .buttonStyle(.borderless)
                .font(.system(size: 11))
            Spacer()
        }
        .frame(maxWidth: .infinity)
    }

    @ViewBuilder
    private func repoContextMenu(_ repo: RepoState) -> some View {
        Button("Sync Now") {
            Task { await appState.syncNow(repo.path) }
        }
        .disabled(repo.paused || !repo.watching)

        if repo.paused {
            Button("Resume") { Task { await appState.setPaused(repo.path, paused: false) } }
        } else {
            Button("Pause") { Task { await appState.setPaused(repo.path, paused: true) } }
        }

        Divider()

        Button("Open in Finder") { reveal(repo.path) }
        Button("Open in Terminal") { openTerminal(repo.path) }
        Button("Copy Path") {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(repo.path, forType: .string)
        }

        Divider()

        Button("Remove from Git Auto Sync", role: .destructive) {
            Task { await appState.removeRepo(repo.path) }
        }
    }

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            if let repo = selectedRepo {
                Button {
                    Task { await appState.syncNow(repo.path) }
                } label: {
                    Label("Sync Now", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(repo.paused || !repo.watching)
                .keyboardShortcut("r", modifiers: [.command])
                .help("Sync \(repo.name) now (⌘R)")

                Button {
                    Task { await appState.setPaused(repo.path, paused: !repo.paused) }
                } label: {
                    Label(repo.paused ? "Resume" : "Pause", systemImage: repo.paused ? "play.fill" : "pause.fill")
                }
                .help(repo.paused ? "Resume monitoring" : "Pause monitoring")

                Menu {
                    Button("Open in Finder") { reveal(repo.path) }
                    Button("Open in Terminal") { openTerminal(repo.path) }
                    Divider()
                    Button("Copy Path") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(repo.path, forType: .string)
                    }
                    Divider()
                    Button("Remove from Git Auto Sync", role: .destructive) {
                        Task { await appState.removeRepo(repo.path) }
                    }
                } label: {
                    Label("More", systemImage: "ellipsis.circle")
                }
            }
        }
    }

    private func addRepository() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = true
        panel.message = "Select a Git repository to monitor"
        if panel.runModal() == .OK {
            for url in panel.urls {
                Task { await appState.addRepo(url.path) }
            }
        }
    }

    private func reveal(_ path: String) {
        NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: path)
    }

    private func openTerminal(_ path: String) {
        let task = Process()
        task.launchPath = "/usr/bin/open"
        task.arguments = ["-a", "Terminal", path]
        try? task.run()
    }

    private func handleDrop(_ providers: [NSItemProvider]) -> Bool {
        var any = false
        for provider in providers {
            if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
                provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier, options: nil) { item, _ in
                    var url: URL?
                    if let data = item as? Data {
                        url = URL(dataRepresentation: data, relativeTo: nil)
                    } else if let direct = item as? URL {
                        url = direct
                    }
                    guard let u = url else { return }
                    let path = u.path
                    var isDir: ObjCBool = false
                    guard FileManager.default.fileExists(atPath: path, isDirectory: &isDir), isDir.boolValue else { return }
                    Task { @MainActor in await appState.addRepo(path) }
                }
                any = true
            }
        }
        return any
    }
}

private struct DropOverlay: View {
    var body: some View {
        ZStack {
            Color.accentColor.opacity(0.08)
            RoundedRectangle(cornerRadius: 14)
                .strokeBorder(Color.accentColor.opacity(0.8), style: StrokeStyle(lineWidth: 2, dash: [8, 6]))
                .padding(14)
            VStack(spacing: 8) {
                Image(systemName: "plus.circle.fill")
                    .font(.system(size: 38))
                    .foregroundStyle(.tint)
                Text("Drop folders to add")
                    .font(.system(size: 14, weight: .semibold))
            }
        }
    }
}

struct SidebarSearchField: View {
    @Binding var text: String
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(.tertiary)
            TextField("Filter", text: $text)
                .textFieldStyle(.plain)
                .font(.system(size: 12))
                .focused($focused)
            if !text.isEmpty {
                Button { text = "" } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 11))
                        .foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(
            RoundedRectangle(cornerRadius: 6)
                .fill(Color(NSColor.controlBackgroundColor).opacity(0.6))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .strokeBorder(focused ? Color.accentColor.opacity(0.6) : Color.primary.opacity(0.06), lineWidth: 0.5)
        )
    }
}

private struct EmptyDashboard: View {
    let onAdd: () -> Void

    var body: some View {
        VStack(spacing: 18) {
            ZStack {
                Circle()
                    .fill(LinearGradient(colors: [.accentColor.opacity(0.25), .accentColor.opacity(0.05)], startPoint: .top, endPoint: .bottom))
                    .frame(width: 84, height: 84)
                Image(systemName: "arrow.triangle.2.circlepath")
                    .font(.system(size: 36, weight: .light))
                    .foregroundStyle(.tint)
            }
            Text("Git Auto Sync")
                .font(.system(size: 24, weight: .semibold))
            Text("Pick a Git repository and the daemon will quietly keep it committed, rebased, and pushed.")
                .font(.system(size: 13))
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 380)
            HStack(spacing: 10) {
                Button("Add Repository", action: onAdd)
                    .controlSize(.large)
                    .buttonStyle(.borderedProminent)
            }
            Text("Tip — drop a folder anywhere in this window to add it.")
                .font(.system(size: 11))
                .foregroundStyle(.tertiary)
                .padding(.top, 8)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

struct ConnectionBar: View {
    let state: ConnectionState
    let daemonRunning: Bool
    let summary: String
    @State private var pulse = false

    var body: some View {
        HStack(spacing: 9) {
            ZStack {
                Circle()
                    .fill(dotColor.opacity(0.25))
                    .frame(width: 14, height: 14)
                    .scaleEffect(pulse && state == .connected ? 1.6 : 1)
                    .opacity(pulse && state == .connected ? 0 : 0.6)
                    .animation(.easeOut(duration: 2).repeatForever(autoreverses: false), value: pulse)
                Circle()
                    .fill(dotColor)
                    .frame(width: 8, height: 8)
            }

            VStack(alignment: .leading, spacing: 1) {
                Text(state == .connected ? "Connected" : (daemonRunning ? "Connecting…" : "Daemon offline"))
                    .font(.system(size: 12, weight: .semibold))
                Text(summary)
                    .font(.system(size: 10))
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .onAppear { pulse = true }
    }

    private var dotColor: Color {
        switch state {
        case .connected: return daemonRunning ? .green : .orange
        case .connecting: return .orange
        case .disconnected: return .red
        }
    }
}

private struct SidebarFooter: View {
    let appState: AppState
    let selectedPath: String?
    let onAdd: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            Button(action: onAdd) {
                Image(systemName: "plus")
                    .frame(width: 22, height: 22)
            }
            .buttonStyle(.borderless)
            .help("Add Repository")

            if let path = selectedPath {
                Button(action: { Task { await appState.removeRepo(path) } }) {
                    Image(systemName: "minus")
                        .frame(width: 22, height: 22)
                }
                .buttonStyle(.borderless)
                .help("Remove selected repository")
            }
            Spacer()
            Text("\(appState.repos.count) repo\(appState.repos.count == 1 ? "" : "s")")
                .font(.system(size: 10))
                .foregroundStyle(.tertiary)
        }
        .font(.system(size: 12, weight: .medium))
        .padding(.horizontal, 10)
        .padding(.vertical, 6)
    }
}

struct SidebarRepoRow: View {
    let repo: RepoState
    let hovered: Bool

    var body: some View {
        HStack(spacing: 10) {
            StatusBadge(status: repo.status, paused: repo.paused, conflict: repo.conflict)
                .frame(width: 22, height: 22)

            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(repo.name)
                        .font(.system(size: 13, weight: .semibold))
                        .lineLimit(1)
                    if let b = repo.branch, !b.isEmpty {
                        Text(b)
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundStyle(.tertiary)
                            .lineLimit(1)
                    }
                }
                HStack(spacing: 6) {
                    if repo.ahead > 0 {
                        Label("\(repo.ahead)", systemImage: "arrow.up")
                            .labelStyle(.titleAndIcon)
                            .font(.system(size: 10, weight: .medium))
                            .foregroundStyle(.secondary)
                    }
                    if repo.behind > 0 {
                        Label("\(repo.behind)", systemImage: "arrow.down")
                            .labelStyle(.titleAndIcon)
                            .font(.system(size: 10, weight: .medium))
                            .foregroundStyle(.secondary)
                    }
                    if let sync = repo.lastSync {
                        LiveRelativeText(date: sync)
                            .font(.system(size: 10))
                            .foregroundStyle(.tertiary)
                    } else if repo.watching {
                        Text("monitoring")
                            .font(.system(size: 10))
                            .foregroundStyle(.tertiary)
                            .italic()
                    }
                }
            }
            Spacer()
        }
        .padding(.vertical, 4)
        .padding(.horizontal, 4)
    }
}

struct StatusBadge: View {
    let status: String
    let paused: Bool
    let conflict: Bool
    @State private var spin = 0.0

    var body: some View {
        ZStack {
            Circle()
                .fill(color.opacity(0.18))
            Image(systemName: symbol)
                .font(.system(size: 10, weight: .bold))
                .foregroundStyle(color)
                .rotationEffect(.degrees(status == "syncing" ? spin : 0))
                .onAppear {
                    if status == "syncing" {
                        withAnimation(.linear(duration: 1.4).repeatForever(autoreverses: false)) {
                            spin = 360
                        }
                    }
                }
        }
    }

    private var color: Color {
        if paused { return .gray }
        if conflict { return .orange }
        switch status {
        case "ok": return .green
        case "syncing": return .blue
        case "error": return .red
        case "conflict": return .orange
        case "preflight": return .yellow
        case "paused": return .gray
        default: return .secondary
        }
    }

    private var symbol: String {
        if paused { return "pause.fill" }
        if conflict { return "exclamationmark.triangle.fill" }
        switch status {
        case "ok": return "checkmark"
        case "syncing": return "arrow.triangle.2.circlepath"
        case "error": return "xmark"
        case "conflict": return "exclamationmark.triangle.fill"
        case "preflight": return "hand.raised.fill"
        case "paused": return "pause.fill"
        default: return "circle.dotted"
        }
    }
}
