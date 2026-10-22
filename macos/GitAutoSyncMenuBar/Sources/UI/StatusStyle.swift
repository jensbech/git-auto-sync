import SwiftUI
import AppKit

enum Palette {
    static let red = adaptive(light: NSColor(srgbRed: 0.80, green: 0.14, blue: 0.11, alpha: 1), dark: .systemRed)
    static let orange = adaptive(light: NSColor(srgbRed: 0.70, green: 0.33, blue: 0.0, alpha: 1), dark: .systemOrange)
    static let amber = adaptive(light: NSColor(srgbRed: 0.60, green: 0.39, blue: 0.0, alpha: 1), dark: .systemYellow)
    static let green = adaptive(light: NSColor(srgbRed: 0.13, green: 0.55, blue: 0.23, alpha: 1), dark: .systemGreen)
    static let teal = adaptive(light: NSColor(srgbRed: 0.0, green: 0.47, blue: 0.55, alpha: 1), dark: .systemTeal)

    private static func adaptive(light: NSColor, dark: NSColor) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? dark : light
        })
    }
}

struct StatusStyle: Equatable {
    let title: String
    let pill: String
    let symbol: String
    let menuSymbol: String
    let color: Color
    var isSyncing = false
    var needsAttention = false

    static func of(_ repo: RepoState) -> StatusStyle {
        if repo.paused { return of(.paused) }
        if repo.conflict { return of(.conflict) }
        if repo.statusValue == .error, repo.errorCode == "config", RepoIssue.looksMissing((repo.lastError ?? "").lowercased()) {
            return of(.preflight)
        }
        return of(repo.statusValue)
    }

    static func of(_ status: RepoStatus) -> StatusStyle {
        switch status {
        case .ok:
            return StatusStyle(title: "Synced", pill: "synced", symbol: "checkmark", menuSymbol: "checkmark.circle", color: Palette.green)
        case .syncing:
            return StatusStyle(title: "Syncing", pill: "syncing", symbol: "arrow.triangle.2.circlepath", menuSymbol: "arrow.triangle.2.circlepath", color: .blue, isSyncing: true)
        case .error:
            return StatusStyle(title: "Error", pill: "error", symbol: "xmark", menuSymbol: "xmark.octagon", color: Palette.red, needsAttention: true)
        case .conflict:
            return StatusStyle(title: "Conflict", pill: "conflict", symbol: "exclamationmark.triangle.fill", menuSymbol: "exclamationmark.triangle", color: Palette.orange, needsAttention: true)
        case .preflight:
            return StatusStyle(title: "Blocked", pill: "blocked", symbol: "hand.raised.fill", menuSymbol: "hand.raised", color: Palette.amber, needsAttention: true)
        case .paused:
            return StatusStyle(title: "Paused", pill: "paused", symbol: "pause.fill", menuSymbol: "pause.circle", color: .gray)
        case .offline:
            return StatusStyle(title: "Offline", pill: "offline", symbol: "icloud.slash", menuSymbol: "icloud.slash", color: .gray)
        case .idle:
            return StatusStyle(title: "Watching", pill: "watching", symbol: "circle.dotted", menuSymbol: "circle.dotted", color: .secondary)
        case .unknown:
            return StatusStyle(title: "Unknown", pill: "unknown", symbol: "questionmark", menuSymbol: "questionmark.circle", color: .secondary)
        }
    }
}

struct EventStyle {
    let title: String
    let badge: String
    let symbol: String
    let color: Color

    static func of(_ type: String) -> EventStyle {
        switch type {
        case "commit": return EventStyle(title: "Committed", badge: "commit", symbol: "plus.circle", color: .blue)
        case "pull": return EventStyle(title: "Pulled", badge: "pull", symbol: "arrow.down.circle", color: .indigo)
        case "push": return EventStyle(title: "Pushed", badge: "push", symbol: "arrow.up.circle", color: Palette.green)
        case "synced": return EventStyle(title: "Synced", badge: "synced", symbol: "checkmark.circle", color: Palette.teal)
        case "error": return EventStyle(title: "Error", badge: "error", symbol: "xmark.octagon", color: Palette.red)
        case "conflict": return EventStyle(title: "Conflict", badge: "conflict", symbol: "exclamationmark.triangle", color: Palette.orange)
        case "preflight": return EventStyle(title: "Blocked", badge: "blocked", symbol: "hand.raised", color: Palette.amber)
        case "offline": return EventStyle(title: "Offline", badge: "offline", symbol: "icloud.slash", color: .gray)
        case "recovered": return EventStyle(title: "Recovered", badge: "recovered", symbol: "checkmark.seal", color: Palette.green)
        case "paused": return EventStyle(title: "Paused", badge: "paused", symbol: "pause.circle", color: .gray)
        case "resumed": return EventStyle(title: "Resumed", badge: "resumed", symbol: "play.circle", color: .gray)
        default: return EventStyle(title: type.capitalized, badge: type, symbol: "circle", color: .secondary)
        }
    }
}

struct ConnectionStyle {
    let title: String
    let color: Color

    static func of(_ connection: DaemonConnection) -> ConnectionStyle {
        switch connection {
        case .connected: return ConnectionStyle(title: "Daemon connected", color: Palette.green)
        case .connecting: return ConnectionStyle(title: "Connecting to daemon…", color: .gray)
        case .daemonNotRunning: return ConnectionStyle(title: "Daemon not running", color: .gray)
        case .incompatible(let version):
            return ConnectionStyle(title: DaemonMismatch.title(version), color: Palette.orange)
        }
    }
}

extension AppState {
    var displayConnection: DaemonConnection {
        if case .connecting = connection, daemonRunning { return .connected }
        return connection
    }

    var isDaemonUnavailable: Bool {
        switch displayConnection {
        case .daemonNotRunning, .incompatible: return true
        case .connected, .connecting: return false
        }
    }
}

struct SpinningSymbol: View {
    let name: String
    let spinning: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if #available(macOS 15, *) {
            Image(systemName: name)
                .symbolEffect(.rotate, isActive: spinning && !reduceMotion)
        } else if spinning && !reduceMotion {
            TimelineView(.animation(minimumInterval: 1.0 / 30)) { ctx in
                Image(systemName: name)
                    .rotationEffect(.degrees(angle(at: ctx.date)))
            }
        } else {
            Image(systemName: name)
        }
    }

    private func angle(at date: Date) -> Double {
        let period = 1.4
        return date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: period) / period * 360
    }
}

struct StatusBadge: View {
    let style: StatusStyle
    var glyphSize: CGFloat = 10

    init(repo: RepoState, glyphSize: CGFloat = 10) {
        self.style = .of(repo)
        self.glyphSize = glyphSize
    }

    var body: some View {
        ZStack {
            Circle()
                .fill(style.color.opacity(0.18))
            SpinningSymbol(name: style.symbol, spinning: style.isSyncing)
                .font(.system(size: glyphSize, weight: .bold))
                .foregroundStyle(style.color)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Status: \(style.title)")
    }
}

struct StatusPill: View {
    let text: String
    let color: Color
    let accessibilityText: String

    init(text: String, color: Color) {
        self.text = text
        self.color = color
        self.accessibilityText = text
    }

    init(style: StatusStyle) {
        self.text = style.pill
        self.color = style.color
        self.accessibilityText = "Status: \(style.title)"
    }

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
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(accessibilityText)
    }
}
