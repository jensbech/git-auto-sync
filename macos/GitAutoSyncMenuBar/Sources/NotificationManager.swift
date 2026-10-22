import AppKit
import Foundation
import UserNotifications

@MainActor
protocol RepoNotifying: AnyObject {
    func notify(repo: String, body: String)
    func clear(repo: String)
}

@MainActor
final class NotificationManager: NSObject, UNUserNotificationCenterDelegate, RepoNotifying {
    static let shared = NotificationManager()

    nonisolated static let repoKey = "repo"

    var onOpenRepo: ((String) -> Void)?

    private let center: UNUserNotificationCenter?

    private override init() {
        center = Bundle.main.bundleIdentifier == nil ? nil : UNUserNotificationCenter.current()
        super.init()
        center?.delegate = self
    }

    func requestPermission() {
        center?.requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    func notify(repo: String, body: String) {
        guard let center else { return }
        let content = UNMutableNotificationContent()
        content.title = "Git Auto Sync"
        content.subtitle = URL(fileURLWithPath: repo).lastPathComponent
        content.body = body
        content.sound = .default
        content.threadIdentifier = repo
        content.userInfo = [Self.repoKey: repo]

        let request = UNNotificationRequest(
            identifier: "gas-\(repo)",
            content: content,
            trigger: nil
        )
        center.add(request)
    }

    func clear(repo: String) {
        center?.removeDeliveredNotifications(withIdentifiers: ["gas-\(repo)"])
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let repo = response.notification.request.content.userInfo[Self.repoKey] as? String
        Task { @MainActor in
            if let repo {
                self.onOpenRepo?(repo)
            } else {
                NSApplication.shared.activate()
            }
        }
        completionHandler()
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}
