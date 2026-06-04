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
        VStack(spacing: 0) {
            RepoHeader(repo: repo)
                .frame(height: 88, alignment: .top)
                .padding(.horizontal, 24)
                .padding(.top, 18)
                .padding(.bottom, 12)

            Picker("", selection: $tab) {
                ForEach(DetailTab.allCases) { t in
                    Text(t.rawValue).tag(t)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(maxWidth: 420, alignment: .leading)
            .padding(.horizontal, 24)
            .padding(.bottom, 12)

            Divider()

            Group {
                switch tab {
                case .overview:
                    OverviewTab(appState: appState, repo: repo)
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
        HStack(alignment: .center, spacing: 14) {
            StatusBadge(status: repo.status, paused: repo.paused, conflict: repo.conflict)
                .frame(width: 38, height: 38)

            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 8) {
                    Text(repo.name)
                        .font(.system(size: 20, weight: .semibold))
                    if repo.paused {
                        Pill(text: "paused", color: .gray)
                    } else if repo.conflict {
                        Pill(text: "conflict", color: .orange)
                    } else if repo.status == "preflight" {
                        Pill(text: "blocked", color: .yellow)
                    } else if repo.status == "syncing" {
                        Pill(text: "syncing", color: .blue)
                    } else if repo.status == "ok" {
                        Pill(text: "synced", color: .green)
                    } else if repo.status == "error" {
                        Pill(text: "error", color: .red)
                    }
                }
                HStack(spacing: 10) {
                    if let branch = repo.branch, !branch.isEmpty {
                        Label(branch, systemImage: "arrow.triangle.branch")
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(.secondary)
                    }
                    if let upstream = repo.upstream, !upstream.isEmpty {
                        Text("↦ \(upstream)")
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(.tertiary)
                    }
                    if repo.ahead > 0 {
                        Label("\(repo.ahead) ahead", systemImage: "arrow.up")
                            .font(.system(size: 11, weight: .medium))
                            .foregroundStyle(.blue)
                    }
                    if repo.behind > 0 {
                        Label("\(repo.behind) behind", systemImage: "arrow.down")
                            .font(.system(size: 11, weight: .medium))
                            .foregroundStyle(.orange)
                    }
                }
                Text(abbreviatePath(repo.path))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(.quaternary)
                    .lineLimit(1)
                    .textSelection(.enabled)
            }
            Spacer()
        }
    }

    private func abbreviatePath(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path.hasPrefix(home) { return "~" + path.dropFirst(home.count) }
        return path
    }
}

private struct Pill: View {
    let text: String
    let color: Color
    var body: some View {
        Text(text.uppercased())
            .font(.system(size: 9, weight: .bold, design: .monospaced))
            .tracking(0.5)
            .foregroundStyle(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(
                Capsule().fill(color.opacity(0.15))
            )
            .overlay(
                Capsule().strokeBorder(color.opacity(0.35), lineWidth: 0.5)
            )
    }
}

private struct OverviewTab: View {
    let appState: AppState
    let repo: RepoState

    var body: some View {
        TabScroll {
            if repo.conflict {
                ConflictBanner(repo: repo)
            } else if let pf = repo.preflight, !pf.isEmpty {
                PreflightBanner(repo: repo, message: pf)
            } else if let err = repo.lastError, !err.isEmpty, repo.status == "error" {
                ErrorBanner(message: err) {
                    Task { await appState.syncNow(repo.path) }
                }
            }

            StatCardsRow(repo: repo)

            if repo.batchPending == true, let due = repo.batchDue {
                BatchPendingCard(due: due)
            }

            RecentActivityCard(events: Array(appState.eventsFor(repo: repo.path).suffix(5).reversed()))
        }
    }
}

private struct StatCardsRow: View {
    let repo: RepoState

    var body: some View {
        HStack(spacing: 12) {
            StatCard(
                title: "Last sync",
                value: repo.lastSync.map { AnyView(LiveRelativeText(date: $0).font(.system(size: 18, weight: .semibold))) } ?? AnyView(Text("—").font(.system(size: 18, weight: .semibold))),
                detail: repo.lastActivity ?? (repo.watching ? "monitoring" : "idle"),
                icon: "clock"
            )
            StatCard(
                title: "Branch",
                value: AnyView(Text(repo.branch ?? "—").font(.system(size: 18, weight: .semibold)).lineLimit(1)),
                detail: repo.upstream ?? "no upstream",
                icon: "arrow.triangle.branch"
            )
            StatCard(
                title: "Divergence",
                value: AnyView(Text("\(repo.ahead) ↑  \(repo.behind) ↓").font(.system(size: 18, weight: .semibold))),
                detail: repo.ahead == 0 && repo.behind == 0 ? "in sync" : "out of sync",
                icon: "arrow.up.arrow.down"
            )
        }
    }
}

private struct StatCard: View {
    let title: String
    let value: AnyView
    let detail: String
    let icon: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: icon)
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(.tertiary)
                Text(title.uppercased())
                    .font(.system(size: 10, weight: .semibold))
                    .tracking(0.6)
                    .foregroundStyle(.tertiary)
            }
            value
                .lineLimit(1)
            Text(detail)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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

private struct ConflictBanner: View {
    let repo: RepoState

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(.orange)
                Text("Merge conflict on rebase")
                    .font(.system(size: 13, weight: .semibold))
                Spacer()
                Button("Open in Finder") {
                    NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: repo.path)
                }
                .controlSize(.small)
                Button("Open in Terminal") {
                    let p = Process()
                    p.launchPath = "/usr/bin/open"
                    p.arguments = ["-a", "Terminal", repo.path]
                    try? p.run()
                }
                .controlSize(.small)
            }
            if let files = repo.conflictFiles, !files.isEmpty {
                Text("Conflicting files")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(files, id: \.self) { f in
                        HStack(spacing: 6) {
                            Image(systemName: "doc.text.fill")
                                .font(.system(size: 9))
                                .foregroundStyle(.orange.opacity(0.7))
                            Text(f)
                                .font(.system(size: 11, design: .monospaced))
                                .textSelection(.enabled)
                        }
                    }
                }
            }
            Text("Rebase aborted to keep your work safe. Resolve in the terminal, commit, and the daemon resumes on the next change.")
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            HStack(spacing: 6) {
                Button("Copy resolve commands") {
                    let cmd = "cd \"\(repo.path)\" && git status\n# fix conflicts, then:\n# git add -A && git rebase --continue"
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(cmd, forType: .string)
                }
                .controlSize(.small)
            }
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.orange.opacity(0.1))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.orange.opacity(0.4), lineWidth: 0.7)
        )
    }
}

private struct PreflightBanner: View {
    let repo: RepoState
    let message: String

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "hand.raised.fill")
                .foregroundStyle(.yellow)
                .padding(.top, 1)
            VStack(alignment: .leading, spacing: 4) {
                Text("Sync paused — repo state needs attention")
                    .font(.system(size: 13, weight: .semibold))
                Text(message)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button("Open in Finder") {
                NSWorkspace.shared.selectFile(nil, inFileViewerRootedAtPath: repo.path)
            }
            .controlSize(.small)
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.yellow.opacity(0.08))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.yellow.opacity(0.35), lineWidth: 0.7)
        )
    }
}

private struct ErrorBanner: View {
    let message: String
    let onRetry: () -> Void
    @State private var expanded = false

    private var shortMessage: String { ErrorClean.short(message) }
    private var hasDetails: Bool { message.count > shortMessage.count }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "exclamationmark.octagon.fill")
                    .foregroundStyle(.red)
                    .padding(.top, 1)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Last sync failed")
                        .font(.system(size: 13, weight: .semibold))
                    Text(shortMessage)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .lineLimit(expanded ? nil : 3)
                }
                Spacer()
                Button("Retry", action: onRetry)
                    .controlSize(.small)
            }
            HStack(spacing: 8) {
                if hasDetails {
                    Button(expanded ? "Hide details" : "Show details") {
                        withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() }
                    }
                    .buttonStyle(.borderless)
                    .controlSize(.small)
                }
                Button("Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(message, forType: .string)
                }
                .buttonStyle(.borderless)
                .controlSize(.small)
                Spacer()
            }
            if expanded {
                ScrollView {
                    Text(message)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(8)
                }
                .frame(maxHeight: 180)
                .background(
                    RoundedRectangle(cornerRadius: 6)
                        .fill(Color.black.opacity(0.06))
                )
            }
        }
        .padding(14)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.red.opacity(0.08))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.red.opacity(0.3), lineWidth: 0.7)
        )
    }
}

private struct BatchPendingCard: View {
    let due: Date
    @State private var now = Date()

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "hourglass")
                .foregroundStyle(.blue)
            Text("Batching changes — next commit in \(timeRemaining)")
                .font(.system(size: 12))
                .foregroundStyle(.primary)
            Spacer()
        }
        .padding(12)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.blue.opacity(0.08))
        )
        .onReceive(Timer.publish(every: 1, on: .main, in: .common).autoconnect()) { t in now = t }
    }

    private var timeRemaining: String {
        let remaining = max(0, Int(due.timeIntervalSince(now)))
        if remaining <= 0 { return "now" }
        if remaining < 60 { return "\(remaining)s" }
        let m = remaining / 60
        let s = remaining % 60
        return "\(m)m \(s)s"
    }
}

private struct RecentActivityCard: View {
    let events: [DaemonEvent]

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Recent activity".uppercased())
                .font(.system(size: 10, weight: .semibold))
                .tracking(0.6)
                .foregroundStyle(.tertiary)

            if events.isEmpty {
                Text("No recent activity")
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
        HStack(alignment: .top, spacing: 10) {
            EventTypeBadge(type: event.type, color: eventColor(event.type))
            VStack(alignment: .leading, spacing: 2) {
                if let msg = event.message, !msg.isEmpty {
                    Text(firstLine(ErrorClean.short(msg)))
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
            }
            Spacer()
            LiveRelativeText(date: event.date)
                .font(.system(size: 10))
                .foregroundStyle(.quaternary)
        }
        .padding(.vertical, 6)
    }

    private func firstLine(_ s: String) -> String {
        s.split(separator: "\n").first.map(String.init) ?? s
    }
}

private func eventColor(_ type: String) -> Color {
    switch type {
    case "commit": return .blue
    case "push": return .green
    case "synced": return .mint
    case "error": return .red
    case "conflict": return .orange
    case "preflight": return .yellow
    case "state": return .secondary
    default: return .secondary
    }
}

private struct TimelineTab: View {
    let appState: AppState
    let repo: RepoState
    @State private var filter: EventFilter = .all

    private var events: [DaemonEvent] {
        appState.eventsFor(repo: repo.path).filter { filter.matches($0) }.reversed()
    }

    private var grouped: [(label: String, events: [DaemonEvent])] {
        let cal = Calendar.current
        var todayList: [DaemonEvent] = []
        var yesterdayList: [DaemonEvent] = []
        var earlierGroups: [Date: [DaemonEvent]] = [:]
        for e in events {
            if cal.isDateInToday(e.date) {
                todayList.append(e)
            } else if cal.isDateInYesterday(e.date) {
                yesterdayList.append(e)
            } else {
                let day = cal.startOfDay(for: e.date)
                earlierGroups[day, default: []].append(e)
            }
        }
        var out: [(String, [DaemonEvent])] = []
        if !todayList.isEmpty { out.append(("Today", todayList)) }
        if !yesterdayList.isEmpty { out.append(("Yesterday", yesterdayList)) }
        let df = DateFormatter()
        df.dateFormat = "EEEE, MMM d"
        for day in earlierGroups.keys.sorted(by: >) {
            out.append((df.string(from: day), earlierGroups[day] ?? []))
        }
        return out
    }

    var body: some View {
        TabScroll {
            HStack(spacing: 6) {
                ForEach(EventFilter.allCases, id: \.self) { f in
                    FilterTab(
                        label: f.rawValue,
                        count: appState.eventsFor(repo: repo.path).filter { f.matches($0) }.count,
                        isSelected: filter == f
                    ) { filter = f }
                }
                Spacer()
            }

            if events.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "tray")
                        .font(.system(size: 28, weight: .light))
                        .foregroundStyle(.tertiary)
                    Text("No events")
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(.secondary)
                    Text("Activity for \(repo.name) will appear here.")
                        .font(.system(size: 11))
                        .foregroundStyle(.tertiary)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 60)
            } else {
                ForEach(Array(grouped.enumerated()), id: \.offset) { _, group in
                    TimelineGroup(label: group.label, count: group.events.count, events: group.events)
                }
            }
        }
    }
}

private struct TimelineGroup: View {
    let label: String
    let count: Int
    let events: [DaemonEvent]

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(label.uppercased())
                    .font(.system(size: 10, weight: .semibold))
                    .tracking(0.7)
                    .foregroundStyle(.tertiary)
                Spacer()
                Text("\(count)")
                    .font(.system(size: 10))
                    .foregroundStyle(.quaternary)
            }
            VStack(spacing: 0) {
                ForEach(Array(events.enumerated()), id: \.element.id) { idx, ev in
                    if idx > 0 {
                        Divider()
                    }
                    EventRow(event: ev)
                }
            }
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
}

private struct SettingsTab: View {
    let appState: AppState
    let repo: RepoState

    @State private var pollInterval: Int = 600
    @State private var batchWindow: Int = 0
    @State private var saving = false
    @State private var savedAt: Date? = nil

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
                        valueLabel: humanDuration(pollInterval)
                    ) {
                        VStack(alignment: .leading, spacing: 8) {
                            PresetChips(
                                items: pollPresets.map { ($0.label, $0.seconds) },
                                selected: pollInterval
                            ) { pollInterval = $0 }
                            Stepper(value: $pollInterval, in: 30...86400, step: 30) {
                                Text("\(pollInterval) sec")
                                    .font(.system(size: 11, design: .monospaced))
                                    .foregroundStyle(.secondary)
                            }
                            .labelsHidden()
                        }
                    }
                    Divider()
                    SettingsRow(
                        label: "Batch window",
                        help: "Wait this long after a change before committing. 0 = commit every change.",
                        valueLabel: batchWindow == 0 ? "Off" : humanDuration(batchWindow)
                    ) {
                        VStack(alignment: .leading, spacing: 8) {
                            PresetChips(
                                items: batchPresets.map { ($0.label, $0.seconds) },
                                selected: batchWindow
                            ) { batchWindow = $0 }
                            Stepper(value: $batchWindow, in: 0...86400, step: 15) {
                                Text(batchWindow == 0 ? "off" : "\(batchWindow) sec")
                                    .font(.system(size: 11, design: .monospaced))
                                    .foregroundStyle(.secondary)
                            }
                            .labelsHidden()
                        }
                    }
                }

                HStack {
                    Button(saving ? "Saving…" : "Save Settings") {
                        Task { await save() }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(saving || !hasChanges)

                    Button("Revert") { syncFromRepo() }
                        .disabled(!hasChanges)

                    if let savedAt {
                        LiveRelativeText(date: savedAt, prefix: "Saved ")
                            .font(.system(size: 11))
                            .foregroundStyle(.secondary)
                            .transition(.opacity)
                    }
                    Spacer()
                }

                Text("Stored in this repo's git config under [auto-sync]. The watcher restarts after saving.")
                    .font(.system(size: 11))
                    .foregroundStyle(.tertiary)
            }
            .frame(maxWidth: 620, alignment: .leading)
        }
        .onAppear { syncFromRepo() }
        .onChange(of: repo.path) { _, _ in syncFromRepo() }
        .onChange(of: repo.pollInterval) { _, _ in syncFromRepo() }
        .onChange(of: repo.batchWindow) { _, _ in syncFromRepo() }
    }

    private var hasChanges: Bool {
        pollInterval != (repo.pollInterval ?? 600) || batchWindow != (repo.batchWindow ?? 0)
    }

    private func syncFromRepo() {
        pollInterval = repo.pollInterval ?? 600
        batchWindow = repo.batchWindow ?? 0
    }

    private func save() async {
        saving = true
        defer { saving = false }
        await appState.saveSettings(repo.path, pollInterval: pollInterval, batchWindow: batchWindow)
        savedAt = Date()
    }

    private func humanDuration(_ sec: Int) -> String {
        if sec <= 0 { return "—" }
        if sec < 60 { return "\(sec)s" }
        if sec < 3600 { return "\(sec / 60)m" }
        let h = sec / 3600
        let m = (sec % 3600) / 60
        return m == 0 ? "\(h)h" : "\(h)h \(m)m"
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
            }
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
