import AppKit
import SwiftUI

@main
struct GitAutoSyncApp: App {
    @State private var appState: AppState
    @State private var didFirstLaunchOpen = false

    init() {
        let state = AppState()
        _appState = State(initialValue: state)
        NotificationManager.shared.onOpenRepo = { path in
            state.showDashboard(repo: path)
        }
        NotificationManager.shared.requestPermission()
    }

    var body: some Scene {
        MenuBarExtra {
            MenuBarLauncher(appState: appState)
        } label: {
            MenuBarLabel(appState: appState, didFirstLaunchOpen: $didFirstLaunchOpen)
        }
        .menuBarExtraStyle(.menu)

        Window("Git Auto Sync", id: "dashboard") {
            DashboardView(appState: appState)
                .frame(minWidth: 760, minHeight: 460)
        }
        .defaultSize(width: 900, height: 560)
        .windowToolbarStyle(.unified)
        .commands {
            AppCommands(appState: appState)
        }
    }
}

private struct MenuBarLabel: View {
    let appState: AppState
    @Binding var didFirstLaunchOpen: Bool
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        MenuBarIcon(appState: appState)
            .onAppear(perform: register)
            .task {
                register()
                guard !didFirstLaunchOpen else { return }
                didFirstLaunchOpen = true
                if !UserDefaults.standard.bool(forKey: "gas.didOnboarding") {
                    UserDefaults.standard.set(true, forKey: "gas.didOnboarding")
                    appState.showDashboard()
                }
            }
    }

    private func register() {
        appState.openDashboardWindow = { openWindow(id: "dashboard") }
    }
}

private struct MenuBarLauncher: View {
    let appState: AppState
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        MenuBarView(appState: appState)
            .task {
                appState.openDashboardWindow = { openWindow(id: "dashboard") }
                appState.refreshLaunchAtLogin()
            }
    }
}

private struct AppCommands: Commands {
    let appState: AppState

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            AddRepositoryCommand(appState: appState)
        }
        CommandGroup(replacing: .textEditing) {
            Button("Find Repository") { appState.requestFocusSearch() }
                .keyboardShortcut("f", modifiers: [.command])
        }
        CommandMenu("Repository") {
            RepositoryCommands(appState: appState)
        }
    }
}

private struct AddRepositoryCommand: View {
    let appState: AppState

    var body: some View {
        Button("Add Repository…") { appState.requestAddRepository() }
            .keyboardShortcut("n", modifiers: [.command])
            .disabled(appState.connection != .connected)
    }
}

private struct RepositoryCommands: View {
    let appState: AppState

    var body: some View {
        let repo = appState.selectedRepo
        let connected = appState.connection == .connected

        Button("Sync Now") { Task { await appState.syncSelected() } }
            .keyboardShortcut("r", modifiers: [.command])
            .disabled(!connected || repo == nil || repo?.paused == true || repo?.watching == false)

        Button("Sync All") { Task { await appState.syncAll() } }
            .keyboardShortcut("r", modifiers: [.command, .shift])
            .disabled(!connected || appState.repos.isEmpty)

        Divider()

        Button(repo?.paused == true ? "Resume" : "Pause") { Task { await appState.togglePauseSelected() } }
            .keyboardShortcut(".", modifiers: [.command])
            .disabled(!connected || repo == nil)

        Divider()

        Button("Remove from Git Auto Sync…") { removeSelected() }
            .keyboardShortcut(.delete, modifiers: [.command])
            .disabled(repo == nil)
    }

    private func removeSelected() {
        if NSApp.keyWindow?.firstResponder is NSText {
            NSApp.sendAction(#selector(NSResponder.deleteToBeginningOfLine(_:)), to: nil, from: nil)
            return
        }
        appState.requestRemoveSelected()
    }
}
