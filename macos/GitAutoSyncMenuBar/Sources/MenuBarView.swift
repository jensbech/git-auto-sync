import SwiftUI
import AppKit
import ServiceManagement

struct MenuBarIcon: View {
    let appState: AppState

    var body: some View {
        let hasError = appState.repos.contains { $0.status == .error }

        if hasError {
            Image(systemName: "exclamationmark.triangle.fill")
        } else {
            GSyncShape()
                .stroke(style: StrokeStyle(lineWidth: 1.6, lineCap: .round))
                .opacity(appState.connectionState == .disconnected ? 0.4 : 1.0)
                .frame(width: 17, height: 17)
        }
    }
}

private struct GSyncShape: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        let cx = rect.midX
        let cy = rect.midY
        let r = min(rect.width, rect.height) * 0.40
        let aw = r * 0.38

        // Arrow 1: clockwise from 30° to 200°
        p.addArc(center: CGPoint(x: cx, y: cy), radius: r,
                 startAngle: .degrees(30), endAngle: .degrees(200), clockwise: false)
        // Arrowhead 1 at 200°: tip, then two base points
        let a1tip = CGPoint(x: cx + r * cos200, y: cy + r * sin200)
        p.move(to: a1tip)
        p.addLine(to: CGPoint(x: a1tip.x + aw * 0.40, y: a1tip.y + aw * 1.10))
        p.move(to: a1tip)
        p.addLine(to: CGPoint(x: a1tip.x - aw * 0.85, y: a1tip.y + aw * 0.55))

        // Arrow 2: clockwise from 210° to 20°
        p.move(to: CGPoint(x: cx + r * cos210, y: cy + r * sin210))
        p.addArc(center: CGPoint(x: cx, y: cy), radius: r,
                 startAngle: .degrees(210), endAngle: .degrees(20), clockwise: false)
        // Arrowhead 2 at 20°
        let a2tip = CGPoint(x: cx + r * cos20, y: cy + r * sin20)
        p.move(to: a2tip)
        p.addLine(to: CGPoint(x: a2tip.x - aw * 0.40, y: a2tip.y - aw * 1.10))
        p.move(to: a2tip)
        p.addLine(to: CGPoint(x: a2tip.x + aw * 0.85, y: a2tip.y - aw * 0.55))

        return p
    }

    private let cos200 = CGFloat(cos(200.0 * .pi / 180))
    private let sin200 = CGFloat(sin(200.0 * .pi / 180))
    private let cos210 = CGFloat(cos(210.0 * .pi / 180))
    private let sin210 = CGFloat(sin(210.0 * .pi / 180))
    private let cos20  = CGFloat(cos(20.0  * .pi / 180))
    private let sin20  = CGFloat(sin(20.0  * .pi / 180))
}

struct MenuBarView: View {
    let appState: AppState
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        VStack(spacing: 0) {
            PopoverHeader(state: appState.connectionState)

            ScrollView {
                VStack(spacing: 6) {
                    if appState.repos.isEmpty {
                        PopoverSection {
                            HStack(spacing: 8) {
                                Image(systemName: "tray")
                                    .foregroundStyle(.tertiary)
                                Text("No repositories")
                                    .font(.system(size: 12))
                                    .foregroundStyle(.secondary)
                            }
                            .frame(maxWidth: .infinity)
                            .padding(.vertical, 10)
                        }
                    } else {
                        PopoverSection {
                            ForEach(Array(appState.repos.enumerated()), id: \.element.id) { idx, repo in
                                if idx > 0 {
                                    Divider().padding(.leading, 36)
                                }
                                RepoRow(repo: repo) {
                                    Task { await appState.removeRepo(repo.path) }
                                }
                            }
                        }
                    }

                    PopoverSection {
                        PopoverButton(icon: "list.bullet.rectangle", label: "Activity Log") {
                            openActivityLog()
                        }
                        Divider().padding(.leading, 36)
                        PopoverButton(icon: "plus.circle", label: "Add Repository…") {
                            addRepository()
                        }
                    }

                    PopoverSection {
                        HStack {
                            Image(systemName: "arrow.right.circle")
                                .foregroundStyle(.secondary)
                                .frame(width: 16)
                            Text("Launch at Login")
                                .font(.system(size: 13))
                                .foregroundStyle(.primary)
                            Spacer()
                            Toggle("", isOn: $launchAtLogin)
                                .toggleStyle(.switch)
                                .labelsHidden()
                                .controlSize(.small)
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 9)
                        .onChange(of: launchAtLogin) { _, newValue in
                            do {
                                if newValue { try SMAppService.mainApp.register() }
                                else { try SMAppService.mainApp.unregister() }
                            } catch {
                                launchAtLogin = !newValue
                            }
                        }

                        Divider().padding(.leading, 36)

                        PopoverButton(icon: "power", label: "Quit Git Auto Sync", destructive: true) {
                            NSApplication.shared.terminate(nil)
                        }
                    }
                }
                .padding(8)
            }
            .scrollDisabled(appState.repos.count <= 6)
        }
        .frame(width: 300)
    }

    private func openActivityLog() {
        ActivityLogWindowController.shared.show(appState: appState)
    }

    private func addRepository() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.message = "Select a Git repository to monitor"
        if panel.runModal() == .OK, let url = panel.url {
            Task { await appState.addRepo(url.path) }
        }
    }
}

struct PopoverHeader: View {
    let state: ConnectionState
    @State private var pulsing = false

    var body: some View {
        HStack(spacing: 8) {
            ZStack {
                if state == .connected {
                    Circle()
                        .fill(Color.green.opacity(0.3))
                        .frame(width: 14, height: 14)
                        .scaleEffect(pulsing ? 1.6 : 1.0)
                        .opacity(pulsing ? 0 : 0.6)
                        .animation(.easeOut(duration: 2).repeatForever(autoreverses: false), value: pulsing)
                }
                Circle()
                    .fill(state == .connected ? Color.green : Color.red.opacity(0.8))
                    .frame(width: 7, height: 7)
                    .opacity(state == .disconnected ? (pulsing ? 0.3 : 1.0) : 1.0)
                    .animation(
                        state == .disconnected
                            ? .easeInOut(duration: 1.1).repeatForever(autoreverses: true)
                            : .default,
                        value: pulsing
                    )
            }

            Text(state == .connected ? "Connected" : "Not running")
                .font(.system(size: 12, weight: .semibold))
                .foregroundStyle(state == .connected ? .primary : .secondary)

            Spacer()

            Text("Git Auto Sync")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(.quaternary)
                .textCase(.uppercase)
                .tracking(0.5)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .background(.bar)
        .onAppear { pulsing = true }

        Divider()
    }
}

struct PopoverSection<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        VStack(spacing: 0) {
            content
        }
        .background(
            RoundedRectangle(cornerRadius: 9)
                .fill(Color(NSColor.controlBackgroundColor).opacity(0.7))
        )
        .clipShape(RoundedRectangle(cornerRadius: 9))
        .overlay(
            RoundedRectangle(cornerRadius: 9)
                .strokeBorder(Color.white.opacity(0.06), lineWidth: 0.5)
        )
    }
}

struct PopoverButton: View {
    let icon: String
    let label: String
    var destructive: Bool = false
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: icon)
                    .foregroundStyle(destructive ? Color.red.opacity(0.85) : Color.secondary)
                    .frame(width: 16)
                Text(label)
                    .font(.system(size: 13))
                    .foregroundStyle(destructive ? Color.red.opacity(0.85) : Color.primary)
                Spacer()
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 9)
            .background(hovering ? Color.accentColor.opacity(0.15) : Color.clear)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { hovering = $0 }
        .animation(.easeOut(duration: 0.1), value: hovering)
    }
}

struct RepoRow: View {
    let repo: RepoStatus
    let onRemove: () -> Void
    @State private var hovering = false

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            ZStack {
                Circle()
                    .fill(statusColor.opacity(0.15))
                    .frame(width: 26, height: 26)
                Image(systemName: statusIcon)
                    .foregroundStyle(statusColor)
                    .imageScale(.small)
                    .fontWeight(.semibold)
            }

            VStack(alignment: .leading, spacing: 2) {
                Text(repoName)
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(.primary)
                Text(abbreviatePath(repo.path))
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundStyle(.tertiary)
            }

            Spacer()

            if hovering {
                Button(action: onRemove) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.tertiary)
                        .imageScale(.medium)
                }
                .buttonStyle(.plain)
                .transition(.opacity.combined(with: .scale(scale: 0.85)))
            } else if repo.status == .error, let err = repo.lastError {
                Text(err)
                    .font(.system(size: 10))
                    .foregroundStyle(.red.opacity(0.85))
                    .lineLimit(1)
                    .frame(maxWidth: 110, alignment: .trailing)
            } else if let lastSync = repo.lastSync, let activity = repo.lastActivity {
                VStack(alignment: .trailing, spacing: 1) {
                    Text(activity)
                        .font(.system(size: 11, weight: .medium))
                        .foregroundStyle(.secondary)
                    Text(relativeTime(lastSync))
                        .font(.system(size: 10))
                        .foregroundStyle(.tertiary)
                }
            } else if repo.status == .ok {
                Text("monitoring")
                    .font(.system(size: 10, weight: .medium))
                    .foregroundStyle(.tertiary)
                    .italic()
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .background(hovering ? Color.accentColor.opacity(0.1) : Color.clear)
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .animation(.easeOut(duration: 0.12), value: hovering)
    }

    private var repoName: String {
        URL(fileURLWithPath: repo.path).lastPathComponent
    }

    private var statusIcon: String {
        switch repo.status {
        case .ok: "checkmark"
        case .error: "exclamationmark"
        case .unknown: "minus"
        }
    }

    private var statusColor: Color {
        switch repo.status {
        case .ok: .green
        case .error: .red
        case .unknown: .secondary
        }
    }

    private func abbreviatePath(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path.hasPrefix(home) { return "~" + path.dropFirst(home.count) }
        return path
    }

    private func relativeTime(_ date: Date) -> String {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .abbreviated
        return f.localizedString(for: date, relativeTo: .now)
    }
}

final class ActivityLogWindowController {
    static let shared = ActivityLogWindowController()
    private var window: NSWindow?

    func show(appState: AppState) {
        if let window, window.isVisible {
            window.makeKeyAndOrderFront(nil)
            NSApp.activate(ignoringOtherApps: true)
            return
        }

        let view = ActivityLogView(appState: appState)
        let hostingView = NSHostingView(rootView: view)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 640, height: 480),
            styleMask: [.titled, .closable, .resizable, .miniaturizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "Activity Log"
        window.titlebarAppearsTransparent = true
        window.contentView = hostingView
        window.center()
        window.isReleasedWhenClosed = false
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        self.window = window
    }
}
