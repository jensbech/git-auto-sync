import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct DashboardView: View {
    @Bindable var appState: AppState
    @State private var hoveredRepo: String? = nil
    @State private var dropTargeted = false
    @State private var showAllActivity = false
    @State private var pendingRemoval: RepoState? = nil
    @FocusState private var searchFocused: Bool

    private static let allActivityTag = "\u{0}all-activity"

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
        } detail: {
            detail
                .overlay(alignment: .top) { noticeOverlay }
                .animation(.easeOut(duration: 0.2), value: appState.notice)
        }
        .navigationTitle(navigationTitle)
        .navigationSubtitle(navigationSubtitle)
        .toolbar { toolbarContent }
        .onDrop(of: [.fileURL, .folder], isTargeted: $dropTargeted) { providers in
            RepoActions.handleDrop(providers, appState: appState)
        }
        .overlay {
            if dropTargeted {
                DropOverlay()
                    .allowsHitTesting(false)
                    .transition(.opacity)
            }
        }
        .animation(.easeOut(duration: 0.15), value: dropTargeted)
        .confirmRemoval(of: $pendingRemoval, appState: appState)
        .onChange(of: appState.selectedRepoPath) { _, _ in showAllActivity = false }
        .onChange(of: appState.focusSearchRequest) { _, _ in searchFocused = true }
        .onChange(of: appState.addRepoRequest) { _, _ in addRepository() }
        .onChange(of: appState.removeSelectedRequest) { _, _ in
            if let repo = selectedRepo { pendingRemoval = repo }
        }
    }

    private var navigationTitle: String {
        if showAllActivity { return "All Activity" }
        return selectedRepo?.name ?? "Git Auto Sync"
    }

    private var navigationSubtitle: String {
        if !showAllActivity, selectedRepo != nil { return "" }
        return appState.summary
    }

    private var selectedRepo: RepoState? {
        guard let path = appState.selectedRepoPath else { return appState.filteredRepos.first }
        return appState.repos.first { $0.path == path }
    }

    private var listSelection: Binding<String?> {
        Binding(
            get: { showAllActivity ? Self.allActivityTag : appState.selectedRepoPath },
            set: { newValue in
                if newValue == Self.allActivityTag {
                    showAllActivity = true
                } else {
                    showAllActivity = false
                    appState.selectedRepoPath = newValue
                }
            }
        )
    }

    @ViewBuilder
    private var detail: some View {
        if showAllActivity {
            AllActivityView(appState: appState)
        } else if let selected = selectedRepo {
            RepoDetailView(appState: appState, repo: selected)
        } else if appState.isDaemonUnavailable {
            DaemonUnavailableView(appState: appState)
        } else {
            EmptyDashboard(onAdd: addRepository)
        }
    }

    @ViewBuilder
    private var noticeOverlay: some View {
        if let notice = appState.notice {
            NoticeToast(notice: notice) {
                if appState.notice?.id == notice.id { appState.notice = nil }
            }
            .padding(.horizontal, 24)
            .padding(.top, 10)
            .transition(.move(edge: .top).combined(with: .opacity))
        }
    }

    private var sidebar: some View {
        VStack(spacing: 0) {
            if appState.isDaemonUnavailable {
                if !appState.repos.isEmpty {
                    DaemonBanner(appState: appState)
                }
            } else {
                ConnectionBar(
                    connection: appState.displayConnection,
                    summary: appState.summary,
                    needsAttention: appState.hasError
                )
            }

            Divider()

            SidebarSearchField(text: $appState.sidebarSearch, focused: $searchFocused)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)

            if appState.repos.isEmpty {
                emptySidebar
            } else if appState.filteredRepos.isEmpty {
                noResultsSidebar
            } else {
                List(selection: listSelection) {
                    Label("All Activity", systemImage: "list.bullet.rectangle")
                        .font(.system(size: 13, weight: .medium))
                        .padding(.vertical, 4)
                        .padding(.horizontal, 4)
                        .tag(Optional(Self.allActivityTag))
                        .listRowSeparator(.hidden)
                        .listRowInsets(EdgeInsets(top: 2, leading: 6, bottom: 2, trailing: 6))

                    Section("Repositories") {
                        ForEach(appState.filteredRepos) { repo in
                            SidebarRepoRow(repo: repo, hovered: hoveredRepo == repo.path)
                                .onHover { hoveredRepo = $0 ? repo.path : (hoveredRepo == repo.path ? nil : hoveredRepo) }
                                .tag(Optional(repo.path))
                                .listRowSeparator(.hidden)
                                .listRowInsets(EdgeInsets(top: 2, leading: 6, bottom: 2, trailing: 6))
                                .contextMenu { repoContextMenu(repo) }
                        }
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
            }

            Divider()

            SidebarFooter(
                repoCount: appState.repos.count,
                selected: showAllActivity ? nil : selectedRepo,
                onAdd: addRepository,
                onRemove: { pendingRemoval = $0 }
            )
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
            Text("Drop a folder here or click + to add one.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
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
            Button("Clear Filter") { appState.sidebarSearch = "" }
                .buttonStyle(.link)
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
        .disabled(repo.paused || !repo.watching || appState.isDaemonUnavailable)

        Group {
            if repo.paused {
                Button("Resume") { Task { await appState.setPaused(repo.path, paused: false) } }
            } else {
                Button("Pause") { Task { await appState.setPaused(repo.path, paused: true) } }
            }
        }
        .disabled(appState.isDaemonUnavailable)

        Divider()

        Button("Open in Finder") { RepoActions.reveal(repo.path) }
        Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
        Button("Copy Path") { RepoActions.copy(repo.path) }

        Divider()

        Button("Remove from Git Auto Sync…", role: .destructive) {
            pendingRemoval = repo
        }
    }

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            if !showAllActivity, let repo = selectedRepo {
                Button {
                    Task { await appState.syncNow(repo.path) }
                } label: {
                    Label("Sync Now", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(repo.paused || !repo.watching || appState.isDaemonUnavailable)
                .help("Sync \(repo.name) now (⌘R)")

                Button {
                    Task { await appState.setPaused(repo.path, paused: !repo.paused) }
                } label: {
                    Label(repo.paused ? "Resume" : "Pause", systemImage: repo.paused ? "play.fill" : "pause.fill")
                }
                .disabled(appState.isDaemonUnavailable)
                .help(repo.paused ? "Resume monitoring (⌘.)" : "Pause monitoring (⌘.)")

                Menu {
                    Button("Open in Finder") { RepoActions.reveal(repo.path) }
                    Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
                    Divider()
                    Button("Copy Path") { RepoActions.copy(repo.path) }
                    Divider()
                    Button("Sync All Repositories") { Task { await appState.syncAll() } }
                        .disabled(appState.isDaemonUnavailable)
                    Divider()
                    Button("Remove from Git Auto Sync…", role: .destructive) {
                        pendingRemoval = repo
                    }
                } label: {
                    Label("More", systemImage: "ellipsis.circle")
                }
                .help("More actions")
            } else if showAllActivity {
                Button {
                    Task { await appState.syncAll() }
                } label: {
                    Label("Sync All", systemImage: "arrow.triangle.2.circlepath")
                }
                .disabled(appState.repos.isEmpty || appState.isDaemonUnavailable)
                .help("Sync all repositories now (⇧⌘R)")
            }
        }
    }


    private func addRepository() {
        RepoActions.addRepositories(appState)
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
    var focused: FocusState<Bool>.Binding

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(.tertiary)
                .accessibilityHidden(true)
            TextField("Filter", text: $text)
                .textFieldStyle(.plain)
                .font(.system(size: 12))
                .focused(focused)
                .accessibilityLabel("Filter repositories")
                .onExitCommand { focused.wrappedValue = false }
            if !text.isEmpty {
                Button { text = "" } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 11))
                        .foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
                .help("Clear filter")
                .accessibilityLabel("Clear filter")
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
                .strokeBorder(focused.wrappedValue ? Color.accentColor.opacity(0.6) : Color.primary.opacity(0.06), lineWidth: 0.5)
        )
        .help("Filter repositories (⌘F)")
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
            .accessibilityHidden(true)
            Text("Git Auto Sync")
                .font(.system(size: 24, weight: .semibold))
            Text("Add a Git repository and it stays committed, pulled, and pushed in the background.")
                .font(.system(size: 13))
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 380)
            Button("Add Repository…", action: onAdd)
                .controlSize(.large)
                .buttonStyle(.borderedProminent)
            Text("You can also drop a folder anywhere in this window, or press ⌘N.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .padding(.top, 8)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

struct ConnectionBar: View {
    let connection: DaemonConnection
    let summary: String
    var needsAttention = false

    var body: some View {
        let style = ConnectionStyle.of(connection)
        let dot = connection == .connected ? (needsAttention ? StatusStyle.of(.conflict).color : style.color) : style.color
        HStack(spacing: 9) {
            ZStack {
                Circle()
                    .fill(dot.opacity(0.25))
                    .frame(width: 14, height: 14)
                Circle()
                    .fill(dot)
                    .frame(width: 8, height: 8)
            }
            .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 1) {
                Text(summary)
                    .font(.system(size: 12, weight: .semibold))
                    .lineLimit(1)
                Text(style.title)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .accessibilityElement(children: .combine)
    }
}

private struct SidebarFooter: View {
    let repoCount: Int
    let selected: RepoState?
    let onAdd: () -> Void
    let onRemove: (RepoState) -> Void

    var body: some View {
        HStack(spacing: 6) {
            Button(action: onAdd) {
                Image(systemName: "plus")
                    .frame(width: 22, height: 22)
            }
            .buttonStyle(.borderless)
            .help("Add Repository (⌘N)")
            .accessibilityLabel("Add Repository")

            if let repo = selected {
                Button(action: { onRemove(repo) }) {
                    Image(systemName: "minus")
                        .frame(width: 22, height: 22)
                }
                .buttonStyle(.borderless)
                .help("Remove \(repo.name) from Git Auto Sync (⌘⌫)")
                .accessibilityLabel("Remove \(repo.name)")
            }
            Spacer()
            Text("\(repoCount) \(repoCount == 1 ? "repository" : "repositories")")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
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
        let style = StatusStyle.of(repo)
        HStack(spacing: 10) {
            StatusBadge(repo: repo)
                .frame(width: 22, height: 22)

            VStack(alignment: .leading, spacing: 2) {
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: 6) {
                        nameText.fixedSize()
                        if let b = repo.branch, !b.isEmpty {
                            Text(b)
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .frame(minWidth: 44, idealWidth: 44, maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    nameText
                }
                HStack(spacing: 6) {
                    if repo.ahead > 0 {
                        Divergence(symbol: "arrow.up", count: repo.ahead)
                            .accessibilityLabel("\(repo.ahead) ahead")
                    }
                    if repo.behind > 0 {
                        Divergence(symbol: "arrow.down", count: repo.behind)
                            .accessibilityLabel("\(repo.behind) behind")
                    }
                    if style.needsAttention || repo.paused || repo.statusValue == .offline {
                        Text(style.title)
                            .font(.system(size: 11, weight: .medium))
                            .foregroundStyle(style.needsAttention ? style.color : Color.secondary)
                    } else if let changed = repo.lastChange ?? repo.lastSync {
                        LiveRelativeText(date: changed)
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                    } else if repo.watching {
                        Text("Watching")
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                    }
                }
                .lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .padding(.vertical, 4)
        .padding(.horizontal, 4)
        .help(PathFormat.abbreviate(repo.path))
        .accessibilityElement(children: .combine)
    }

    private var nameText: some View {
        Text(repo.name)
            .font(.system(size: 13, weight: .semibold))
            .lineLimit(1)
    }
}

private struct Divergence: View {
    let symbol: String
    let count: Int

    var body: some View {
        HStack(spacing: 1) {
            Image(systemName: symbol)
                .font(.system(size: 8, weight: .bold))
            Text("\(count)")
                .font(.system(size: 11, weight: .medium))
                .monospacedDigit()
        }
        .foregroundStyle(.secondary)
        .fixedSize()
    }
}
