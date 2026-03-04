import AppKit
import Foundation
import UserNotifications

final class NotificationManager: NSObject, UNUserNotificationCenterDelegate {
    static let shared = NotificationManager()

    private var lastNotificationTime: [String: Date] = [:]
    private let debounceInterval: TimeInterval = 60

    private override init() {
        super.init()
        UNUserNotificationCenter.current().delegate = self
    }

    func requestPermission() {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    func sendNotification(repo: String, message: String) {
        if let last = lastNotificationTime[repo],
           Date().timeIntervalSince(last) < debounceInterval {
            return
        }
        lastNotificationTime[repo] = Date()

        let content = UNMutableNotificationContent()
        content.title = "git-auto-sync Error"
        content.body = "\(abbreviatePath(repo)): \(message)"
        content.sound = .default
        content.categoryIdentifier = "SYNC_ERROR"

        let request = UNNotificationRequest(
            identifier: "error-\(repo)-\(Date().timeIntervalSince1970)",
            content: content,
            trigger: nil
        )

        UNUserNotificationCenter.current().add(request)
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        NSApplication.shared.activate(ignoringOtherApps: true)
        completionHandler()
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

    private func abbreviatePath(_ path: String) -> String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        if path.hasPrefix(home) {
            return "~" + path.dropFirst(home.count)
        }
        return path
    }
}
