import Foundation

protocol KeyValueStoring: AnyObject {
    func object(forKey defaultName: String) -> Any?
    func set(_ value: Any?, forKey defaultName: String)
    func removeObject(forKey defaultName: String)
}

extension UserDefaults: KeyValueStoring {}

@MainActor
final class EventStore {
    static let keyPrefix = "gas.lastSeenEventId."

    let defaults: any KeyValueStoring
    let lastSeenKey: String
    let fileURL: URL?
    let saveDelay: Duration

    private var saveTask: Task<Void, Never>?
    private var pending: (events: [DaemonEvent], lastSeen: UInt64)?

    init(socketPath: String, defaults: any KeyValueStoring = UserDefaults.standard, directory: URL? = EventStore.defaultDirectory, saveDelay: Duration = .seconds(2)) {
        self.defaults = defaults
        self.lastSeenKey = Self.keyPrefix + socketPath
        self.fileURL = directory?.appendingPathComponent(Self.fileName(socketPath))
        self.saveDelay = saveDelay
    }

    nonisolated static var defaultDirectory: URL? {
        guard let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first else { return nil }
        return caches.appendingPathComponent(Bundle.main.bundleIdentifier ?? "com.gitautosync.menubar", isDirectory: true)
    }

    nonisolated static func fileName(_ socketPath: String) -> String {
        let safe = socketPath.map { $0.isLetter || $0.isNumber || $0 == "." || $0 == "-" ? $0 : "_" }
        return "events-\(String(safe)).json"
    }

    var persistedLastSeen: UInt64 {
        (defaults.object(forKey: lastSeenKey) as? NSNumber)?.uint64Value ?? 0
    }

    func load() -> (events: [DaemonEvent], lastSeen: UInt64) {
        guard let fileURL,
              let data = try? Data(contentsOf: fileURL),
              let events = try? JSONDecoder().decode([Lossy<DaemonEvent>].self, from: data).compactMap(\.value),
              let newest = events.last?.id
        else {
            return ([], 0)
        }
        return (events, min(persistedLastSeen, newest))
    }

    func scheduleSave(events: [DaemonEvent], lastSeen: UInt64) {
        pending = (events, lastSeen)
        guard saveTask == nil else { return }
        let delay = saveDelay
        saveTask = Task { [weak self] in
            try? await Task.sleep(for: delay)
            await self?.flush()
        }
    }

    func flush() async {
        saveTask = nil
        guard let (events, lastSeen) = pending else { return }
        pending = nil
        let url = fileURL
        let written = await Task.detached(priority: .utility) {
            Self.write(events, to: url)
        }.value
        if written { record(lastSeen) }
    }

    func saveNow() {
        saveTask?.cancel()
        saveTask = nil
        guard let (events, lastSeen) = pending else { return }
        pending = nil
        if Self.write(events, to: fileURL) { record(lastSeen) }
    }

    private func record(_ lastSeen: UInt64) {
        if lastSeen == 0 {
            defaults.removeObject(forKey: lastSeenKey)
        } else {
            defaults.set(NSNumber(value: lastSeen), forKey: lastSeenKey)
        }
    }

    nonisolated private static func write(_ events: [DaemonEvent], to url: URL?) -> Bool {
        guard let url else { return false }
        do {
            if events.isEmpty {
                try? FileManager.default.removeItem(at: url)
                return true
            }
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            let data = try JSONEncoder().encode(events)
            try data.write(to: url, options: .atomic)
            return true
        } catch {
            return false
        }
    }
}
