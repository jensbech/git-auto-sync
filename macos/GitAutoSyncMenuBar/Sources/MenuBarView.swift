import SwiftUI
import AppKit

@MainActor
enum MenuBarGlyph {
    static let spinFrames = 12
    static let spinStep = 360.0 / Double(spinFrames)

    private struct Key: Hashable {
        let angle: Double
        let slashed: Bool
        let alert: Bool
    }

    private static var cache: [Key: NSImage] = [:]

    static func image(angle: Double, slashed: Bool, alert: Bool) -> NSImage {
        let key = Key(angle: angle, slashed: slashed, alert: alert)
        if let cached = cache[key] { return cached }
        let image = render(angle: angle, slashed: slashed, alert: alert)
        cache[key] = image
        return image
    }

    private static func render(angle: Double, slashed: Bool, alert: Bool) -> NSImage {
        let name = alert ? "exclamationmark.arrow.triangle.2.circlepath" : "arrow.triangle.2.circlepath"
        let config = NSImage.SymbolConfiguration(pointSize: 14, weight: .medium)
        guard let base = NSImage(systemSymbolName: name, accessibilityDescription: nil)?.withSymbolConfiguration(config) else {
            return NSImage()
        }
        if angle == 0 && !slashed {
            base.isTemplate = true
            return base
        }
        let side = ceil(max(base.size.width, base.size.height))
        let image = NSImage(size: NSSize(width: side, height: side), flipped: false) { rect in
            guard let ctx = NSGraphicsContext.current?.cgContext else { return false }
            ctx.saveGState()
            ctx.translateBy(x: rect.midX, y: rect.midY)
            ctx.rotate(by: -angle * .pi / 180)
            base.draw(in: NSRect(x: -base.size.width / 2, y: -base.size.height / 2, width: base.size.width, height: base.size.height))
            ctx.restoreGState()
            if slashed {
                let slash = NSBezierPath()
                slash.move(to: NSPoint(x: rect.minX + 2, y: rect.maxY - 2))
                slash.line(to: NSPoint(x: rect.maxX - 2, y: rect.minY + 2))
                slash.lineCapStyle = .round
                ctx.setBlendMode(.clear)
                slash.lineWidth = 4
                slash.stroke()
                ctx.setBlendMode(.normal)
                NSColor.black.setStroke()
                slash.lineWidth = 1.6
                slash.stroke()
            }
            return true
        }
        image.isTemplate = true
        return image
    }
}

struct MenuBarIcon: View {
    let appState: AppState

    var body: some View {
        let unavailable = appState.isDaemonUnavailable
        MenuBarGlyphView(
            spinning: appState.anySyncing && !unavailable,
            slashed: unavailable,
            alert: appState.hasError && !unavailable
        )
        .accessibilityLabel("Git Auto Sync: \(appState.summary)")
    }
}

private struct MenuBarGlyphView: View {
    let spinning: Bool
    let slashed: Bool
    let alert: Bool
    @State private var frame = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Image(nsImage: MenuBarGlyph.image(angle: Double(frame) * MenuBarGlyph.spinStep, slashed: slashed, alert: alert))
            .task(id: spinning && !reduceMotion) {
                frame = 0
                guard spinning && !reduceMotion else { return }
                while !Task.isCancelled {
                    try? await Task.sleep(for: .milliseconds(100))
                    guard !Task.isCancelled else { break }
                    frame = (frame + 1) % MenuBarGlyph.spinFrames
                }
            }
    }
}

struct MenuBarView: View {
    let appState: AppState
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Button("Open Dashboard") { showDashboard() }
            .keyboardShortcut("d", modifiers: [.command, .shift])

        Divider()

        Text(appState.summary)
        connectionItems
        if let notice = appState.notice, notice.isError {
            Label(notice.text.count > 80 ? String(notice.text.prefix(80)) + "…" : notice.text, systemImage: "exclamationmark.triangle")
        }

        Divider()

        if !appState.repos.isEmpty {
            ForEach(appState.repos) { repo in
                repoMenu(repo)
            }

            Divider()

            Button("Sync All") { Task { await appState.syncAll() } }
                .keyboardShortcut("r", modifiers: [.command, .shift])
                .disabled(appState.isDaemonUnavailable)
            if appState.repos.contains(where: { !$0.paused }) {
                Button("Pause All") { Task { await appState.setAllPaused(true) } }
                    .disabled(appState.isDaemonUnavailable)
            }
            if appState.repos.contains(where: { $0.paused }) {
                Button("Resume All") { Task { await appState.setAllPaused(false) } }
                    .disabled(appState.isDaemonUnavailable)
            }
        }

        Button("Add Repository…") { addRepository() }

        Divider()

        Toggle("Launch at Login", isOn: Binding(
            get: { appState.launchAtLogin },
            set: { appState.setLaunchAtLogin($0) }
        ))
        .onAppear { appState.refreshLaunchAtLogin() }

        Divider()

        Button("Quit Git Auto Sync") {
            NSApplication.shared.terminate(nil)
        }
        .keyboardShortcut("q")
    }

    @ViewBuilder
    private var connectionItems: some View {
        switch appState.displayConnection {
        case .daemonNotRunning:
            Button("Start Daemon") { Task { await appState.startDaemon() } }
        case .incompatible(let version):
            Text("\(DaemonMismatch.title(version)): run just install")
        case .connecting:
            Text(ConnectionStyle.of(.connecting).title)
        case .connected:
            EmptyView()
        }
    }

    private func repoMenu(_ repo: RepoState) -> some View {
        let style = StatusStyle.of(repo)
        return Menu {
            Text(statusLine(repo, style: style))
            Divider()
            Button("Sync Now") { Task { await appState.syncNow(repo.path) } }
                .disabled(repo.paused || !repo.watching || appState.isDaemonUnavailable)
            Button(repo.paused ? "Resume" : "Pause") {
                Task { await appState.setPaused(repo.path, paused: !repo.paused) }
            }
            .disabled(appState.isDaemonUnavailable)
            Divider()
            Button("Open in Finder") { RepoActions.reveal(repo.path) }
            Button("Open in Terminal") { RepoActions.openInTerminal(repo.path) }
            Button("Show in Dashboard") {
                appState.selectedRepoPath = repo.path
                showDashboard()
            }
        } label: {
            Label(menuTitle(repo, style: style), systemImage: style.menuSymbol)
        }
    }

    private func menuTitle(_ repo: RepoState, style: StatusStyle) -> String {
        let name = Self.shortName(repo.name)
        switch repo.statusValue {
        case .ok, .idle, .unknown:
            if !repo.paused && !repo.conflict, let changed = repo.lastChange {
                return "\(name) — \(RelativeTime.format(changed))"
            }
        default:
            break
        }
        return "\(name) — \(style.title)"
    }

    private func statusLine(_ repo: RepoState, style: StatusStyle) -> String {
        var parts = [style.title]
        if repo.statusValue == .offline, let retry = repo.nextRetry, let left = Durations.coarseCountdown(to: retry, now: .now) {
            parts.append("next try in \(left)")
        }
        if let changed = repo.lastChange {
            parts.append("last change \(RelativeTime.format(changed))")
        } else {
            parts.append("no changes yet")
        }
        return parts.joined(separator: ", ")
    }

    private static func shortName(_ name: String) -> String {
        name.count > 36 ? String(name.prefix(35)) + "…" : name
    }

    private func showDashboard() {
        openWindow(id: "dashboard")
        NSApp.activate()
    }

    private func addRepository() {
        if RepoActions.addRepositories(appState) {
            showDashboard()
        }
    }
}
