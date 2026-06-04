import Foundation

struct RepoState: Codable, Identifiable, Equatable {
    let path: String
    let status: String
    let branch: String?
    let upstream: String?
    let ahead: Int
    let behind: Int
    let lastSyncTs: Int64?
    let lastActivity: String?
    let lastError: String?
    let preflight: String?
    let paused: Bool
    let conflict: Bool
    let conflictFiles: [String]?
    let watching: Bool
    let batchPending: Bool?
    let batchDueTs: Int64?
    let pollInterval: Int?
    let batchWindow: Int?

    var id: String { path }

    var lastSync: Date? {
        guard let ts = lastSyncTs, ts > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(ts))
    }

    var batchDue: Date? {
        guard let ts = batchDueTs, ts > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(ts))
    }

    var name: String {
        URL(fileURLWithPath: path).lastPathComponent
    }

    enum CodingKeys: String, CodingKey {
        case path, status, branch, upstream, ahead, behind
        case lastSyncTs, lastActivity, lastError, preflight
        case paused, conflict, conflictFiles, watching
        case batchPending, batchDueTs, pollInterval, batchWindow
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        status = try c.decodeIfPresent(String.self, forKey: .status) ?? "idle"
        branch = try c.decodeIfPresent(String.self, forKey: .branch)
        upstream = try c.decodeIfPresent(String.self, forKey: .upstream)
        ahead = try c.decodeIfPresent(Int.self, forKey: .ahead) ?? 0
        behind = try c.decodeIfPresent(Int.self, forKey: .behind) ?? 0
        lastSyncTs = try c.decodeIfPresent(Int64.self, forKey: .lastSyncTs)
        lastActivity = try c.decodeIfPresent(String.self, forKey: .lastActivity)
        lastError = try c.decodeIfPresent(String.self, forKey: .lastError)
        preflight = try c.decodeIfPresent(String.self, forKey: .preflight)
        paused = try c.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        conflict = try c.decodeIfPresent(Bool.self, forKey: .conflict) ?? false
        conflictFiles = try c.decodeIfPresent([String].self, forKey: .conflictFiles)
        watching = try c.decodeIfPresent(Bool.self, forKey: .watching) ?? false
        batchPending = try c.decodeIfPresent(Bool.self, forKey: .batchPending)
        batchDueTs = try c.decodeIfPresent(Int64.self, forKey: .batchDueTs)
        pollInterval = try c.decodeIfPresent(Int.self, forKey: .pollInterval)
        batchWindow = try c.decodeIfPresent(Int.self, forKey: .batchWindow)
    }
}

struct DaemonEvent: Codable, Identifiable {
    let type: String
    let repo: String
    let message: String?
    let status: String?
    let ts: Int64

    var id: String { "\(ts)-\(type)-\(repo)" }
    var date: Date { Date(timeIntervalSince1970: TimeInterval(ts)) }
}

struct SocketCommand: Codable {
    let cmd: String
    var repo: String?
    var settings: [String: String]?
}

struct StatusResponse: Codable {
    let type: String?
    let daemon: String?
    let repos: [RepoState]?
    let error: String?
    let status: String?
}

struct RepoSettingsResponse: Codable {
    let repo: String?
    let pollInterval: IntOrNull?
    let batchWindow: IntOrNull?
    let exec: StringOrNull?
}

struct IntOrNull: Codable {
    let value: Int?
    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        value = try? c.decode(Int.self)
    }
    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(value)
    }
}

struct StringOrNull: Codable {
    let value: String?
    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        value = try? c.decode(String.self)
    }
    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(value)
    }
}

enum DaemonMessage {
    case hello([RepoState])
    case state(RepoState)
    case event(DaemonEvent)
}

enum ConnectionState {
    case disconnected
    case connecting
    case connected
}
