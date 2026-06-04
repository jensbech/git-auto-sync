import SwiftUI
import AppKit
import ServiceManagement

struct MenuBarIcon: View {
    let appState: AppState
    @State private var spin = 0.0
    @State private var animateSpin = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            iconImage

            if appState.hasError {
                Circle()
                    .fill(Color.red)
                    .frame(width: 5, height: 5)
                    .overlay(Circle().strokeBorder(Color.white.opacity(0.9), lineWidth: 0.5))
                    .offset(x: 3, y: -2)
            }
        }
        .font(.system(size: 14, weight: .medium))
    }

    @ViewBuilder
    private var iconImage: some View {
        if appState.anySyncing {
            Image(systemName: "arrow.triangle.2.circlepath")
                .rotationEffect(.degrees(spin))
                .onAppear {
                    animateSpin = true
                    withAnimation(.linear(duration: 1.6).repeatForever(autoreverses: false)) {
                        spin = 360
                    }
                }
                .onDisappear { animateSpin = false }
        } else {
            Image(systemName: "arrow.triangle.2.circlepath")
                .opacity(appState.connectionState == .connected ? 1.0 : 0.45)
        }
    }
}

struct MenuBarView: View {
    let appState: AppState
    @Environment(\.openWindow) private var openWindow
    @State private var launchAtLogin = SMAppService.mainApp.status == .enabled

    var body: some View {
        Button("Open Dashboard") {
            openWindow(id: "dashboard")
            NSApp.activate(ignoringOtherApps: true)
        }
        .keyboardShortcut("d", modifiers: [.command, .shift])

        Divider()

        Text(appState.summary)
        if appState.daemonRunning {
            Text(appState.connectionState == .connected ? "Connected" : "Connecting…")
        } else {
            Text("Daemon not running")
        }

        Divider()

        Button("Add Repository…") {
            addRepository()
        }

        if !appState.repos.isEmpty {
            Menu("Sync Now") {
                ForEach(appState.repos) { repo in
                    Button(repo.name) {
                        Task { await appState.syncNow(repo.path) }
                    }
                    .disabled(repo.paused || !repo.watching)
                }
            }
        }

        Divider()

        Toggle("Launch at Login", isOn: $launchAtLogin)
            .onChange(of: launchAtLogin) { _, newValue in
                do {
                    if newValue { try SMAppService.mainApp.register() }
                    else { try SMAppService.mainApp.unregister() }
                } catch {
                    launchAtLogin = !newValue
                }
            }

        Divider()

        Button("Quit Git Auto Sync") {
            NSApplication.shared.terminate(nil)
        }
        .keyboardShortcut("q")
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
            openWindow(id: "dashboard")
            NSApp.activate(ignoringOtherApps: true)
        }
    }
}
