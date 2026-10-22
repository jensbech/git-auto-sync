import SwiftUI

enum EventFilter: String, CaseIterable {
    case all = "All"
    case changes = "Changes"
    case problems = "Problems"

    func matches(_ event: DaemonEvent) -> Bool {
        switch self {
        case .all: return true
        case .changes: return ["commit", "pull", "push"].contains(event.type)
        case .problems: return ["error", "conflict", "preflight", "offline"].contains(event.type)
        }
    }
}

struct FilterTab: View {
    let label: String
    let count: Int
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Text(label)
                    .font(.system(size: 11, weight: isSelected ? .semibold : .regular))

                if count > 0 {
                    Text("\(count)")
                        .font(.system(size: 10, weight: .medium))
                        .monospacedDigit()
                        .foregroundStyle(isSelected ? .white : .secondary)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(
                            Capsule()
                                .fill(isSelected ? Color.accentColor : Color.secondary.opacity(0.2))
                        )
                }
            }
            .foregroundStyle(isSelected ? .primary : .secondary)
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(isSelected ? Color(NSColor.controlBackgroundColor) : .clear)
            )
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(label), \(count) events")
        .accessibilityAddTraits(isSelected ? .isSelected : [])
    }
}

struct EventFilterBar: View {
    let counts: [EventFilter: Int]
    @Binding var filter: EventFilter

    var body: some View {
        HStack(spacing: 6) {
            ForEach(EventFilter.allCases, id: \.self) { f in
                FilterTab(
                    label: f.rawValue,
                    count: counts[f] ?? 0,
                    isSelected: filter == f
                ) { filter = f }
            }
        }
    }
}

struct EventTypeBadge: View {
    let type: String

    var body: some View {
        let style = EventStyle.of(type)
        Text(style.badge.uppercased())
            .font(.system(size: 9, weight: .bold, design: .monospaced))
            .foregroundStyle(style.color)
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, 5)
            .padding(.vertical, 2)
            .frame(minWidth: 66)
            .background(
                RoundedRectangle(cornerRadius: 3)
                    .fill(style.color.opacity(0.12))
            )
            .accessibilityLabel(style.title)
    }
}

struct EventRow: View {
    let event: DaemonEvent
    var showRepo = true

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            EventTypeBadge(type: event.type)

            VStack(alignment: .leading, spacing: 2) {
                if showRepo {
                    Text(repoName)
                        .font(.system(size: 12, weight: .semibold))
                        .lineLimit(1)
                        .help(PathFormat.abbreviate(event.repo))
                }
                if let message = event.message, !message.isEmpty {
                    Text(message)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(4)
                        .textSelection(.enabled)
                }
            }

            Spacer(minLength: 8)

            Text(event.date, style: .time)
                .font(.system(size: 11))
                .monospacedDigit()
                .foregroundStyle(.secondary)
        }
        .padding(.vertical, 8)
        .padding(.horizontal, 12)
        .accessibilityElement(children: .combine)
    }

    private var repoName: String {
        URL(fileURLWithPath: event.repo).lastPathComponent
    }
}

struct EventDayGroup: Identifiable {
    let id: Date
    let label: String
    let events: [DaemonEvent]
}

struct TimelineSnapshot {
    var counts: [EventFilter: Int] = [:]
    var groups: [EventDayGroup] = []
}

@MainActor
final class TimelineCache {
    private struct Key: Equatable {
        let count: Int
        let firstID: UInt64
        let lastID: UInt64
        let repo: String?
        let filter: EventFilter
        let today: Date
    }

    private var key: Key?
    private var cached = TimelineSnapshot()

    private static let dayFormatter: DateFormatter = {
        let df = DateFormatter()
        df.setLocalizedDateFormatFromTemplate("EEEEMMMd")
        return df
    }()

    func snapshot(for source: [DaemonEvent], repo: String? = nil, filter: EventFilter) -> TimelineSnapshot {
        let cal = Calendar.current
        let today = cal.startOfDay(for: Date())
        let next = Key(
            count: source.count,
            firstID: source.first?.id ?? 0,
            lastID: source.last?.id ?? 0,
            repo: repo,
            filter: filter,
            today: today
        )
        if next == key { return cached }

        var counts: [EventFilter: Int] = [:]
        var groups: [EventDayGroup] = []
        var currentDay: Date?
        var bucket: [DaemonEvent] = []

        func flush() {
            guard let day = currentDay, !bucket.isEmpty else { return }
            groups.append(EventDayGroup(id: day, label: Self.label(for: day, today: today, calendar: cal), events: bucket))
            bucket = []
        }

        for event in source.reversed() {
            if let repo, event.repo != repo { continue }
            counts[.all, default: 0] += 1
            if EventFilter.changes.matches(event) { counts[.changes, default: 0] += 1 }
            if EventFilter.problems.matches(event) { counts[.problems, default: 0] += 1 }
            guard filter.matches(event) else { continue }
            let day = cal.startOfDay(for: event.date)
            if day != currentDay {
                flush()
                currentDay = day
            }
            bucket.append(event)
        }
        flush()

        cached = TimelineSnapshot(counts: counts, groups: groups)
        key = next
        return cached
    }

    private static func label(for day: Date, today: Date, calendar: Calendar) -> String {
        if day == today { return "Today" }
        if calendar.isDate(day, inSameDayAs: calendar.date(byAdding: .day, value: -1, to: today) ?? today) { return "Yesterday" }
        return dayFormatter.string(from: day)
    }
}

private struct EventDayHeader: View {
    let label: String
    let count: Int
    let isFirst: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(label)
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(.secondary)
            Spacer()
            Text("\(count) \(count == 1 ? "event" : "events")")
                .font(.system(size: 11))
                .monospacedDigit()
                .foregroundStyle(.secondary)
        }
        .padding(.top, isFirst ? 0 : 16)
        .padding(.bottom, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color(NSColor.windowBackgroundColor))
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

private struct EventCardRow: View {
    let event: DaemonEvent
    let showRepo: Bool
    let isFirst: Bool
    let isLast: Bool

    var body: some View {
        let shape = UnevenRoundedRectangle(
            topLeadingRadius: isFirst ? 10 : 0,
            bottomLeadingRadius: isLast ? 10 : 0,
            bottomTrailingRadius: isLast ? 10 : 0,
            topTrailingRadius: isFirst ? 10 : 0
        )
        EventRow(event: event, showRepo: showRepo)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(shape.fill(Color(NSColor.controlBackgroundColor)))
            .overlay(shape.strokeBorder(Color.primary.opacity(0.07), lineWidth: 0.5))
    }
}

struct EventTimelineList<Header: View>: View {
    let snapshot: TimelineSnapshot
    let emptyMessage: String
    var showRepo = true
    @ViewBuilder let header: Header

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0, pinnedViews: [.sectionHeaders]) {
                header
                    .padding(.bottom, 14)

                if snapshot.groups.isEmpty {
                    EventTimelineEmpty(message: emptyMessage)
                }

                ForEach(Array(snapshot.groups.enumerated()), id: \.element.id) { groupIndex, group in
                    Section {
                        ForEach(Array(group.events.enumerated()), id: \.element.id) { idx, event in
                            EventCardRow(
                                event: event,
                                showRepo: showRepo,
                                isFirst: idx == 0,
                                isLast: idx == group.events.count - 1
                            )
                        }
                    } header: {
                        EventDayHeader(label: group.label, count: group.events.count, isFirst: groupIndex == 0)
                    }
                }
            }
            .padding(.horizontal, 24)
            .padding(.top, 18)
            .padding(.bottom, 24)
            .frame(maxWidth: .infinity, alignment: .topLeading)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

private struct EventTimelineEmpty: View {
    let message: String

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: "tray")
                .font(.system(size: 28, weight: .light))
                .foregroundStyle(.tertiary)
                .accessibilityHidden(true)
            Text("No events")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(.secondary)
            Text(message)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 60)
    }
}

struct AllActivityView: View {
    let appState: AppState
    @State private var filter: EventFilter = .all
    @State private var repoFilter: String? = nil
    @State private var cache = TimelineCache()

    var body: some View {
        let snapshot = cache.snapshot(for: appState.allEvents, repo: repoFilter, filter: filter)
        EventTimelineList(
            snapshot: snapshot,
            emptyMessage: emptyMessage,
            showRepo: repoFilter == nil
        ) {
            HStack(spacing: 6) {
                EventFilterBar(counts: snapshot.counts, filter: $filter)
                Spacer()
                Picker("Repository", selection: $repoFilter) {
                    Text("All repositories").tag(String?.none)
                    Divider()
                    ForEach(appState.repos) { repo in
                        Text(repo.name).tag(String?.some(repo.path))
                    }
                }
                .labelsHidden()
                .frame(maxWidth: 200)
                .help("Show events for one repository")
            }
        }
        .background(Color(NSColor.windowBackgroundColor))
        .onChange(of: appState.repos.map(\.path)) { _, paths in
            if let repoFilter, !paths.contains(repoFilter) { self.repoFilter = nil }
        }
    }

    private var emptyMessage: String {
        if filter != .all { return "No \(filter.rawValue.lowercased()) to show." }
        return repoFilter == nil ? "Commits, pulls, pushes, and problems from every repository show up here." : "Nothing has happened in this repository yet."
    }
}
