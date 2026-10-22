import SwiftUI
import AppKit

enum DetailTab: String, CaseIterable, Identifiable {
    case overview = "Overview"
    case timeline = "Timeline"
    case settings = "Settings"
    var id: String { rawValue }
}

struct RepoDetailView: View {
    let appState: AppState
    let repo: RepoState
    @State private var tab: DetailTab = .overview

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            RepoHeader(repo: repo)
                .padding(.horizontal, 24)
                .padding(.top, 18)
                .padding(.bottom, 14)

            Picker("Section", selection: $tab) {
                ForEach(DetailTab.allCases) { t in
                    Text(t.rawValue).tag(t)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            .padding(.horizontal, 24)
            .padding(.bottom, 12)

            Divider()

            Group {
                switch tab {
                case .overview:
                    OverviewTab(appState: appState, repo: repo) { tab = .timeline }
                case .timeline:
                    TimelineTab(appState: appState, repo: repo)
                case .settings:
                    SettingsTab(appState: appState, repo: repo)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(Color(NSColor.windowBackgroundColor))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

private struct TabScroll<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                content
            }
            .padding(.horizontal, 24)
            .padding(.top, 18)
            .padding(.bottom, 24)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

private struct RepoHeader: View {
    let repo: RepoState

    var body: some View {
        let style = StatusStyle.of(repo)
        HStack(alignment: .center, spacing: 14) {
            StatusBadge(repo: repo, glyphSize: 15)
                .frame(width: 38, height: 38)
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    Text(repo.name)
                        .font(.system(size: 20, weight: .semibold))
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .help(repo.name)
                    StatusPill(style: style)
                        .fixedSize()
                }
                HStack(spacing: 10) {
                    if let branch = repo.branch, !branch.isEmpty {
                        Label(branch, systemImage: "arrow.triangle.branch")
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .help(branch)
                            .accessibilityLabel("Branch \(branch)")
                    } else {
                        Label("No branch", systemImage: "arrow.triangle.branch")
                    }
                    if let upstream = repo.upstream, !upstream.isEmpty {
                        Text("→ \(upstream)")
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .help("Tracking \(upstream)")
                            .accessibilityLabel("Tracking \(upstream)")
                    }
                    if repo.ahead > 0 {
                        Label("\(repo.ahead) ahead", systemImage: "arrow.up")
                            .foregroundStyle(.blue)
                            .fixedSize()
                    }
                    if repo.behind > 0 {
                        Label("\(repo.behind) behind", systemImage: "arrow.down")
                            .foregroundStyle(StatusStyle.of(.conflict).color)
                            .fixedSize()
                    }
                }
                .font(.system(size: 11, design: .monospaced))
                .foregroundStyle(.secondary)
                Text(PathFormat.abbreviate(repo.path))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .help(repo.path)
            }
            Spacer(minLength: 0)
        }
    }
}

private struct OverviewTab: View {
    let appState: AppState
    let repo: RepoState
    let onShowTimeline: () -> Void

    var body: some View {
        let events = appState.eventsFor(repo: repo.path)
        TabScroll {
            if let issue = RepoIssue.of(repo, events: events) {
                RepoIssueBanner(appState: appState, repo: repo, issue: issue)
            }

            StatCardsGrid(repo: repo)

            if repo.batchPending == true, let due = repo.batchDue {
                BatchPendingCard(due: due)
            }

            RecentActivityCard(
                events: Array(events.suffix(5).reversed()),
                hasMore: events.count > 5,
                onShowAll: onShowTimeline
            )
        }
    }
}

private struct StatCardsGrid: View {
    let repo: RepoState

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 12) {
                lastChangeCard
                lastCheckCard
                branchCard
                divergenceCard
            }
            Grid(horizontalSpacing: 12, verticalSpacing: 12) {
                GridRow {
                    lastChangeCard
                    lastCheckCard
                }
                GridRow {
                    branchCard
                    divergenceCard
                }
            }
        }
    }

    private var lastChangeCard: some View {
        StatCard(title: "Last change", detail: changeDetail, icon: "pencil.and.outline") {
            RelativeValue(date: repo.lastChange)
        }
    }

    private var lastCheckCard: some View {
        StatCard(title: "Last check", detail: checkDetail, icon: "clock") {
            RelativeValue(date: repo.lastCheck ?? repo.lastSync)
        }
    }

    private var branchCard: some View {
        StatCard(title: "Branch", detail: repo.upstream.flatMap { $0.isEmpty ? nil : $0 } ?? "No upstream", icon: "arrow.triangle.branch") {
            Text(repo.branch.flatMap { $0.isEmpty ? nil : $0 } ?? "—")
                .truncationMode(.middle)
        }
    }

    private var divergenceCard: some View {
        StatCard(title: "Divergence", detail: divergenceDetail, icon: "arrow.up.arrow.down") {
            Text("\(repo.ahead) ↑").foregroundStyle(repo.ahead > 0 ? Color.blue : Color.primary)
            + Text("  ")
            + Text("\(repo.behind) ↓").foregroundStyle(repo.behind > 0 ? StatusStyle.of(.conflict).color : Color.primary)
        }
        .accessibilityLabel("Divergence: \(repo.ahead) ahead, \(repo.behind) behind")
    }

    private var changeDetail: String {
        guard repo.lastChange != nil else { return "No changes yet" }
        return (repo.lastActivity.flatMap { $0.isEmpty ? nil : $0 } ?? "Committed or synced").sentenceCased
    }

    private var checkDetail: String {
        if repo.paused { return "Paused" }
        if !repo.watching { return "Not watching" }
        if let poll = repo.pollInterval, poll > 0 { return "Polls every \(Durations.human(poll))" }
        return "Watching for changes"
    }

    private var divergenceDetail: String {
        if repo.ahead == 0 && repo.behind == 0 { return "In sync" }
        var parts: [String] = []
        if repo.ahead > 0 { parts.append("\(repo.ahead.formatted()) to push") }
        if repo.behind > 0 { parts.append("\(repo.behind.formatted()) to pull") }
        return parts.joined(separator: ", ").sentenceCased
    }
}

private struct RelativeValue: View {
    let date: Date?

    var body: some View {
        if let date {
            LiveRelativeText(date: date)
        } else {
            Text("—")
        }
    }
}

private struct StatCard<Value: View>: View {
    let title: String
    let detail: String
    let icon: String
    @ViewBuilder let value: Value

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: icon)
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                Text(title)
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.secondary)
            }
            value
                .font(.system(size: 18, weight: .semibold))
                .lineLimit(1)
            Text(detail)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
                .help(detail)
        }
        .frame(minWidth: 130, maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color(NSColor.controlBackgroundColor))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.primary.opacity(0.06), lineWidth: 0.5)
        )
        .accessibilityElement(children: .combine)
    }
}

private struct BatchPendingCard: View {
    let due: Date

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "hourglass")
                .foregroundStyle(.blue)
                .accessibilityHidden(true)
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                Text("Batching changes: next commit in \(Durations.countdown(to: due, now: ctx.date) ?? "a moment")")
                    .font(.system(size: 12))
                    .monospacedDigit()
            }
            Spacer()
        }
        .padding(12)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.blue.opacity(0.08))
        )
    }
}

private struct RecentActivityCard: View {
    let events: [DaemonEvent]
    let hasMore: Bool
    let onShowAll: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Text("Recent activity")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .accessibilityAddTraits(.isHeader)
                Spacer()
                if hasMore {
                    Button("Show All", action: onShowAll)
                        .buttonStyle(.link)
                        .font(.system(size: 11))
                }
            }

            if events.isEmpty {
                Text("Nothing yet. Commits, pulls, and pushes for this repository show up here.")
                    .font(.system(size: 12))
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 8)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(events.enumerated()), id: \.element.id) { idx, ev in
                        if idx > 0 { Divider() }
                        InlineEventRow(event: ev)
                    }
                }
            }
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color(NSColor.controlBackgroundColor))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.primary.opacity(0.06), lineWidth: 0.5)
        )
    }
}

private struct InlineEventRow: View {
    let event: DaemonEvent

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            EventTypeBadge(type: event.type)
            if let msg = event.message, !msg.isEmpty {
                Text(firstLine(ErrorClean.short(msg)))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            Spacer(minLength: 8)
            LiveRelativeText(date: event.date)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 6)
        .accessibilityElement(children: .combine)
    }

    private func firstLine(_ s: String) -> String {
        let lines = s.split(separator: "\n")
        guard let first = lines.first else { return s }
        return lines.count > 1 ? "\(first) (+\(lines.count - 1) more)" : String(first)
    }
}

private struct TimelineTab: View {
    let appState: AppState
    let repo: RepoState
    @State private var filter: EventFilter = .all
    @State private var cache = TimelineCache()

    var body: some View {
        let snapshot = cache.snapshot(for: appState.eventsFor(repo: repo.path), filter: filter)
        EventTimelineList(
            snapshot: snapshot,
            emptyMessage: filter == .all ? "Commits, pulls, pushes, and problems for \(repo.name) show up here." : "No \(filter.rawValue.lowercased()) to show.",
            showRepo: false
        ) {
            EventFilterBar(counts: snapshot.counts, filter: $filter)
        }
    }
}

private struct SettingsTab: View {
    let appState: AppState
    let repo: RepoState

    @State private var pollInterval: Int = 600
    @State private var batchWindow: Int = 0
    @State private var saving = false
    @State private var savedAt: Date? = nil
    @State private var saveError: String? = nil
    @AppStorage(TerminalApp.storageKey) private var terminalApp: TerminalApp = .terminal

    private let pollPresets: [(label: String, seconds: Int)] = [
        ("1m", 60), ("5m", 300), ("10m", 600), ("30m", 1800), ("1h", 3600)
    ]
    private let batchPresets: [(label: String, seconds: Int)] = [
        ("Off", 0), ("30s", 30), ("2m", 120), ("5m", 300), ("15m", 900)
    ]

    var body: some View {
        TabScroll {
            VStack(alignment: .leading, spacing: 18) {
                SettingsCard {
                    SettingsRow(
                        label: "Poll interval",
                        help: "How often the daemon syncs even without file changes.",
                        valueLabel: Durations.human(pollInterval)
                    ) {
                        VStack(alignment: .leading, spacing: 8) {
                            PresetChips(
                                items: pollPresets.map { ($0.label, $0.seconds) },
                                selected: pollInterval
                            ) { pollInterval = $0 }
                            SecondsStepper(
                                label: "Poll interval in seconds",
                                value: $pollInterval,
                                range: 30...86400,
                                step: 30
                            )
                        }
                    }
                    Divider()
                    SettingsRow(
                        label: "Batch window",
                        help: "Wait this long after a change before committing. Off commits every change.",
                        valueLabel: batchWindow == 0 ? "Off" : Durations.human(batchWindow)
                    ) {
                        VStack(alignment: .leading, spacing: 8) {
                            PresetChips(
                                items: batchPresets.map { ($0.label, $0.seconds) },
                                selected: batchWindow
                            ) { batchWindow = $0 }
                            SecondsStepper(
                                label: "Batch window in seconds",
                                value: $batchWindow,
                                range: 0...86400,
                                step: 15,
                                zeroText: "Off"
                            )
                        }
                    }
                }

                HStack(spacing: 8) {
                    Button {
                        Task { await save() }
                    } label: {
                        HStack(spacing: 6) {
                            if saving {
                                ProgressView().controlSize(.small)
                            }
                            Text(saving ? "Saving…" : "Save Settings")
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(saving || !hasChanges)
                    .keyboardShortcut("s", modifiers: [.command])

                    Button("Revert") { syncFromRepo() }
                        .disabled(saving || !hasChanges)

                    if let saveError {
                        Label(saveError, systemImage: "exclamationmark.triangle.fill")
                            .font(.system(size: 11))
                            .foregroundStyle(.red)
                            .lineLimit(2)
                            .textSelection(.enabled)
                    } else if let savedAt {
                        LiveRelativeText(date: savedAt, prefix: "Saved ")
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                            .transition(.opacity)
                    }
                    Spacer()
                }

                Text("Saved to this repository's git config under [auto-sync]. Syncing restarts with the new values right away.")
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)

                GitConfigCard(repo: repo)

                SettingsCard {
                    HStack(alignment: .center) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Open in Terminal uses").font(.system(size: 13, weight: .semibold))
                            Text("Applies to every repository.").font(.system(size: 11)).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Picker("Terminal app", selection: $terminalApp) {
                            ForEach(TerminalApp.allCases) { app in
                                Text(app.isInstalled ? app.title : "\(app.title) (not installed)").tag(app)
                            }
                        }
                        .labelsHidden()
                        .fixedSize()
                    }
                    .padding(14)
                }
            }
            .frame(maxWidth: 620, alignment: .leading)
        }
        .onAppear { syncFromRepo() }
        .onChange(of: repo.path) { _, _ in
            syncFromRepo()
            savedAt = nil
            saveError = nil
        }
        .onChange(of: repo.pollInterval) { _, _ in syncFromRepo() }
        .onChange(of: repo.batchWindow) { _, _ in syncFromRepo() }
        .onChange(of: pollInterval) { _, _ in saveError = nil }
        .onChange(of: batchWindow) { _, _ in saveError = nil }
    }

    private var hasChanges: Bool {
        pollInterval != (repo.pollInterval ?? 600) || batchWindow != (repo.batchWindow ?? 0)
    }

    private var draft: RepoSettings {
        RepoSettings(
            pollInterval: pollInterval,
            batchWindow: batchWindow
        )
    }

    private func syncFromRepo() {
        guard !saving else { return }
        pollInterval = repo.pollInterval ?? 600
        batchWindow = repo.batchWindow ?? 0
    }

    private func save() async {
        saving = true
        savedAt = nil
        saveError = nil
        let ok = await appState.saveSettings(repo.path, draft)
        saving = false
        if ok {
            savedAt = Date()
        } else if let notice = appState.notice, notice.isError {
            saveError = notice.text
        } else {
            saveError = "The daemon couldn't save these settings."
        }
    }
}

private struct GitConfigCard: View {
    let repo: RepoState

    private var configText: String {
        var lines = ["[auto-sync]"]
        lines.append(repo.pollInterval.map { "\tsyncInterval = \($0)" } ?? "\t# syncInterval not set (default)")
        lines.append(repo.batchWindow.map { "\tbatchWindow = \($0)" } ?? "\t# batchWindow not set (default)")
        return lines.joined(separator: "\n")
    }

    var body: some View {
        SettingsCard {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 8) {
                    Text("Git config").font(.system(size: 13, weight: .semibold))
                    StatusPill(text: "read-only", color: .secondary)
                    Spacer()
                    Button {
                        RepoActions.copy(configText)
                    } label: {
                        Image(systemName: "doc.on.doc")
                            .font(.system(size: 11))
                    }
                    .buttonStyle(.borderless)
                    .help("Copy git config section")
                    .accessibilityLabel("Copy git config section")
                }
                Text(configText)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(10)
                    .background(
                        RoundedRectangle(cornerRadius: 6)
                            .fill(Color.primary.opacity(0.05))
                    )
                Text("Effective values reported by the daemon for \(PathFormat.abbreviate(repo.path)).")
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
            }
            .padding(14)
        }
    }
}

private struct PresetChips: View {
    let items: [(String, Int)]
    let selected: Int
    let onSelect: (Int) -> Void

    var body: some View {
        HStack(spacing: 5) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                Button {
                    onSelect(item.1)
                } label: {
                    Text(item.0)
                        .font(.system(size: 11, weight: selected == item.1 ? .semibold : .regular))
                        .padding(.horizontal, 10)
                        .padding(.vertical, 4)
                        .background(
                            Capsule()
                                .fill(selected == item.1 ? Color.accentColor : Color(NSColor.controlBackgroundColor))
                        )
                        .overlay(
                            Capsule()
                                .strokeBorder(Color.primary.opacity(selected == item.1 ? 0 : 0.08), lineWidth: 0.5)
                        )
                        .foregroundStyle(selected == item.1 ? .white : .primary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(item.1 == 0 ? "Off" : Self.spoken(item.1))
                .accessibilityAddTraits(selected == item.1 ? .isSelected : [])
            }
        }
    }

    private static func spoken(_ seconds: Int) -> String {
        let f = DateComponentsFormatter()
        f.unitsStyle = .full
        f.allowedUnits = [.hour, .minute, .second]
        return f.string(from: TimeInterval(seconds)) ?? "\(seconds) seconds"
    }
}

private struct SecondsStepper: View {
    let label: String
    @Binding var value: Int
    let range: ClosedRange<Int>
    let step: Int
    var zeroText: String? = nil

    var body: some View {
        HStack(spacing: 6) {
            Stepper(label, value: $value, in: range, step: step)
                .labelsHidden()
            Text(value == 0 ? (zeroText ?? "0 seconds") : "\(value) seconds")
                .font(.system(size: 11))
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
        }
    }
}

private struct SettingsCard<Content: View>: View {
    @ViewBuilder let content: Content
    var body: some View {
        VStack(spacing: 0) { content }
            .background(
                RoundedRectangle(cornerRadius: 10)
                    .fill(Color(NSColor.controlBackgroundColor))
            )
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .strokeBorder(Color.primary.opacity(0.06), lineWidth: 0.5)
            )
    }
}

private struct SettingsRow<Right: View>: View {
    let label: String
    let help: String
    let valueLabel: String
    @ViewBuilder let right: Right

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(label).font(.system(size: 13, weight: .semibold))
                    Text(help).font(.system(size: 11)).foregroundStyle(.secondary)
                }
                Spacer()
                Text(valueLabel)
                    .font(.system(size: 13, weight: .semibold, design: .monospaced))
                    .foregroundStyle(.tint)
            }
            right
        }
        .padding(14)
    }
}
