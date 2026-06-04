import SwiftUI

@main
struct GitAutoSyncApp: App {
    @State private var appState = AppState()
    @State private var didFirstLaunchOpen = false

    init() {
        NotificationManager.shared.requestPermission()
    }

    var body: some Scene {
        MenuBarExtra {
            MenuBarLauncher(appState: appState, didFirstLaunchOpen: $didFirstLaunchOpen)
        } label: {
            MenuBarIcon(appState: appState)
        }
        .menuBarExtraStyle(.menu)

        Window("Git Auto Sync", id: "dashboard") {
            DashboardView(appState: appState)
                .frame(minWidth: 760, minHeight: 460)
        }
        .defaultSize(width: 900, height: 560)
        .windowToolbarStyle(.unified)
        .commands {
            CommandGroup(replacing: .newItem) {}
        }
    }
}

private struct MenuBarLauncher: View {
    let appState: AppState
    @Binding var didFirstLaunchOpen: Bool
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        MenuBarView(appState: appState)
            .task {
                if !didFirstLaunchOpen {
                    didFirstLaunchOpen = true
                    let seen = UserDefaults.standard.bool(forKey: "gas.didOnboarding")
                    if !seen {
                        UserDefaults.standard.set(true, forKey: "gas.didOnboarding")
                        openWindow(id: "dashboard")
                        NSApp.activate(ignoringOtherApps: true)
                    }
                }
            }
    }
}
