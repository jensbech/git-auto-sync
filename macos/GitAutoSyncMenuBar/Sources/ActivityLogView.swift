import SwiftUI

enum EventFilter: String, CaseIterable {
    case all = "All"
    case commits = "Commits"
    case pushes = "Pushes"
    case errors = "Errors"

    func matches(_ event: DaemonEvent) -> Bool {
        switch self {
        case .all: return true
        case .commits: return event.type == "commit"
        case .pushes: return event.type == "push" || event.type == "synced"
        case .errors: return event.type == "error" || event.type == "conflict" || event.type == "preflight"
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
        }
        .buttonStyle(.plain)
    }
}

struct EventRow: View {
    let event: DaemonEvent

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
                        .lineLimit(4)
                        .textSelection(.enabled)
                }
            }
            .padding(.vertical, 8)
            .padding(.horizontal, 12)
        }
    }

    private var repoName: String {
        URL(fileURLWithPath: event.repo).lastPathComponent
    }

    private var eventColor: Color {
        switch event.type {
        case "commit": return .blue
        case "push": return .green
        case "synced": return .mint
        case "error": return .red
        case "conflict": return .orange
        case "preflight": return .yellow
        default: return .secondary
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
