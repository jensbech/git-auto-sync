import SwiftUI

enum EventFilter: String, CaseIterable {
    case all = "All"
    case commits = "Commits"
    case pushes = "Pushes"
    case errors = "Errors"

    func matches(_ event: DaemonEvent) -> Bool {
        switch self {
        case .all: true
        case .commits: event.type == "commit"
        case .pushes: event.type == "push"
        case .errors: event.type == "error"
        }
    }
}

struct ActivityLogView: View {
    let appState: AppState
    @State private var filter: EventFilter = .all

    private var filteredEvents: [DaemonEvent] {
        appState.events.filter { filter.matches($0) }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                Text("Activity Log")
                    .font(.system(size: 14, weight: .semibold))

                Spacer()

                HStack(spacing: 2) {
                    ForEach(EventFilter.allCases, id: \.self) { tab in
                        FilterTab(
                            label: tab.rawValue,
                            count: appState.events.filter { tab.matches($0) }.count,
                            isSelected: filter == tab
                        ) {
                            withAnimation(.easeInOut(duration: 0.15)) {
                                filter = tab
                            }
                        }
                    }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 44)
            .padding(.bottom, 12)
            .background(.bar)

            Divider()

            if filteredEvents.isEmpty {
                if appState.events.isEmpty {
                    ContentUnavailableView(
                        "No Events Yet",
                        systemImage: "arrow.triangle.2.circlepath",
                        description: Text("Events will appear here as repositories sync.")
                    )
                } else {
                    ContentUnavailableView(
                        "No \(filter.rawValue)",
                        systemImage: "line.3.horizontal.decrease.circle",
                        description: Text("No \(filter.rawValue.lowercased()) events recorded.")
                    )
                }
            } else {
                ScrollViewReader { proxy in
                    List(filteredEvents) { event in
                        EventRow(event: event)
                            .id(event.id)
                            .listRowInsets(EdgeInsets(top: 0, leading: 0, bottom: 0, trailing: 0))
                            .listRowSeparator(.hidden)
                    }
                    .listStyle(.plain)
                    .onChange(of: appState.events.count) { _, _ in
                        if let last = filteredEvents.last {
                            withAnimation {
                                proxy.scrollTo(last.id, anchor: .bottom)
                            }
                        }
                    }
                }
            }
        }
        .frame(minWidth: 480, minHeight: 320)
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
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(isSelected ? Color(NSColor.controlBackgroundColor) : .clear)
                    .shadow(color: .black.opacity(isSelected ? 0.08 : 0), radius: 1, y: 1)
            )
        }
        .buttonStyle(.plain)
    }
}

struct EventRow: View {
    let event: DaemonEvent
    @State private var appeared = false

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            Rectangle()
                .fill(eventColor)
                .frame(width: 3)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    EventTypeBadge(type: event.type, color: eventColor)

                    Text(repoName)
                        .font(.system(size: 12, weight: .medium))
                        .foregroundStyle(.primary)

                    Text(abbreviatePath(event.repo))
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundStyle(.tertiary)
                        .lineLimit(1)

                    Spacer()

                    Text(event.date, style: .time)
                        .font(.system(size: 11))
                        .foregroundStyle(.quaternary)
                }

                if let message = event.message, !message.isEmpty {
                    Text(message)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(3)
                        .textSelection(.enabled)
                }
            }
            .padding(.vertical, 8)
            .padding(.horizontal, 12)
        }
        .opacity(appeared ? 1 : 0)
        .onAppear {
            withAnimation(.easeOut(duration: 0.2)) {
                appeared = true
            }
        }
    }

    private var repoName: String {
        URL(fileURLWithPath: event.repo).lastPathComponent
    }

    private var eventColor: Color {
        switch event.type {
        case "commit": .blue
        case "push": .green
        case "synced": .mint
        case "error": .red
        default: .secondary
        }
    }

    private func abbreviatePath(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path.hasPrefix(home) {
            return "~" + path.dropFirst(home.count)
        }
        return path
    }
}

struct EventTypeBadge: View {
    let type: String
    let color: Color

    var body: some View {
        Text(type.uppercased())
            .font(.system(size: 9, weight: .bold, design: .monospaced))
            .foregroundStyle(color)
            .padding(.horizontal, 5)
            .padding(.vertical, 2)
            .background(
                RoundedRectangle(cornerRadius: 3)
                    .fill(color.opacity(0.12))
            )
    }
}
