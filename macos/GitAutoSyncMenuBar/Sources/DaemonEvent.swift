import Foundation

enum RepoStatus: String, Codable, Sendable {
    case idle, syncing, ok, error, conflict, preflight, paused, offline, unknown

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = RepoStatus(rawValue: raw) ?? .unknown
    }
}

struct RepoState: Codable, Identifiable, Equatable, Sendable {
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
    let errorCode: String?
    let lastChangeTs: Int64?
    let lastCheckTs: Int64?
    let nextRetryTs: Int64?
    let failures: Int

    var id: String { path }

    var statusValue: RepoStatus { RepoStatus(rawValue: status) ?? .unknown }

    var lastChange: Date? {
        guard let ts = lastChangeTs, ts > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(ts))
    }

    var lastCheck: Date? {
        guard let ts = lastCheckTs, ts > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(ts))
    }

    var nextRetry: Date? {
        guard let ts = nextRetryTs, ts > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(ts))
    }

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
        case errorCode, lastChangeTs, lastCheckTs, nextRetryTs, failures
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
        errorCode = try c.decodeIfPresent(String.self, forKey: .errorCode)
        lastChangeTs = try c.decodeIfPresent(Int64.self, forKey: .lastChangeTs)
        lastCheckTs = try c.decodeIfPresent(Int64.self, forKey: .lastCheckTs)
        nextRetryTs = try c.decodeIfPresent(Int64.self, forKey: .nextRetryTs)
        failures = try c.decodeIfPresent(Int.self, forKey: .failures) ?? 0
    }
}

struct DaemonEvent: Codable, Identifiable, Equatable, Sendable {
    let id: UInt64
    let type: String
    let repo: String
    let message: String?
    let status: String?
    let code: String?
    let ts: Int64

    var date: Date { Date(timeIntervalSince1970: TimeInterval(ts)) }

    init(id: UInt64, type: String, repo: String, message: String? = nil, status: String? = nil, code: String? = nil, ts: Int64) {
        self.id = id
        self.type = type
        self.repo = repo
        self.message = message
        self.status = status
        self.code = code
        self.ts = ts
    }

    enum CodingKeys: String, CodingKey {
        case id, type, repo, message, status, code, ts
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(UInt64.self, forKey: .id)
        type = try c.decode(String.self, forKey: .type)
        repo = try c.decodeIfPresent(String.self, forKey: .repo) ?? ""
        message = try c.decodeIfPresent(String.self, forKey: .message)
        status = try c.decodeIfPresent(String.self, forKey: .status)
        code = try c.decodeIfPresent(String.self, forKey: .code)
        ts = try c.decodeIfPresent(Int64.self, forKey: .ts) ?? Int64(Date().timeIntervalSince1970)
    }
}

enum DaemonCommand {
    static let protocolVersion = 2

    static let status = "status"
    static let list = "list"
    static let add = "add"
    static let remove = "remove"
    static let syncNow = "sync_now"
    static let syncAll = "sync_all"
    static let pause = "pause"
    static let resume = "resume"
    static let pauseAll = "pause_all"
    static let resumeAll = "resume_all"
    static let getSettings = "get_settings"
    static let setSettings = "set_settings"
    static let abortRebase = "abort_rebase"
    static let events = "events"
    static let subscribe = "subscribe"
    static let version = "version"
}

struct SocketCommand: Codable, Sendable {
    let cmd: String
    var repo: String?
    var settings: [String: String]?
    var id: String?
    var force: Bool?
    var since: UInt64?
    var limit: Int?
}

struct DaemonResponse: Sendable {
    let id: String?
    let ok: Bool
    let error: String?
    let code: String?
    let raw: Data

    static func decode(_ line: Data) -> DaemonResponse? {
        guard let json = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else { return nil }
        if json["ok"] == nil, json["type"] != nil { return nil }
        let error = (json["error"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        let ok = (json["ok"] as? Bool) ?? (error == nil)
        return DaemonResponse(
            id: json["id"] as? String,
            ok: ok,
            error: error,
            code: (json["code"] as? String).flatMap { $0.isEmpty ? nil : $0 },
            raw: line
        )
    }

    func result<T: Decodable>(_ type: T.Type) -> T? {
        (try? JSONDecoder().decode(ResultEnvelope<T>.self, from: raw))?.result
    }

    var resultStrings: [String: String] {
        guard let dict = result([String: JSONScalar].self) else { return [:] }
        return dict.compactMapValues(\.stringValue)
    }

    var events: [DaemonEvent] {
        if let wrapped = result(EventsResult.self) {
            return wrapped.events.compactMap(\.value)
        }
        if let bare = result([Lossy<DaemonEvent>].self) {
            return bare.compactMap(\.value)
        }
        return []
    }
}

private struct ResultEnvelope<T: Decodable>: Decodable {
    let result: T?
}

private struct EventsResult: Decodable {
    let events: [Lossy<DaemonEvent>]
}

struct Lossy<T: Decodable>: Decodable {
    let value: T?

    init(from decoder: Decoder) throws {
        value = try? decoder.singleValueContainer().decode(T.self)
    }
}

struct JSONScalar: Decodable, Sendable {
    let stringValue: String?

    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            stringValue = nil
        } else if let b = try? c.decode(Bool.self) {
            stringValue = b ? "true" : "false"
        } else if let i = try? c.decode(Int64.self) {
            stringValue = String(i)
        } else if let d = try? c.decode(Double.self) {
            stringValue = String(d)
        } else if let s = try? c.decode(String.self) {
            stringValue = s
        } else {
            stringValue = nil
        }
    }
}

struct StatusResult: Decodable, Sendable {
    let daemon: String?
    let protocolVersion: Int?
    let version: String?
    let repos: [RepoState]

    enum CodingKeys: String, CodingKey {
        case daemon, version, repos
        case protocolVersion = "protocol"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        daemon = try? c.decodeIfPresent(String.self, forKey: .daemon)
        protocolVersion = try? c.decodeIfPresent(Int.self, forKey: .protocolVersion)
        version = try? c.decodeIfPresent(String.self, forKey: .version)
        repos = ((try? c.decodeIfPresent([Lossy<RepoState>].self, forKey: .repos)) ?? []).compactMap(\.value)
    }
}

struct DaemonHello: Decodable, Sendable {
    let protocolVersion: Int?
    let version: String?
    let repos: [RepoState]
    let lastEventId: UInt64

    enum CodingKeys: String, CodingKey {
        case version, repos, lastEventId
        case protocolVersion = "protocol"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        protocolVersion = try? c.decodeIfPresent(Int.self, forKey: .protocolVersion)
        version = try? c.decodeIfPresent(String.self, forKey: .version)
        repos = ((try? c.decodeIfPresent([Lossy<RepoState>].self, forKey: .repos)) ?? []).compactMap(\.value)
        lastEventId = (try? c.decodeIfPresent(UInt64.self, forKey: .lastEventId)) ?? 0
    }
}

enum DaemonMessage: Sendable {
    case hello(DaemonHello)
    case state(RepoState)
    case event(DaemonEvent)
    case disconnected
    case unavailable
    case incompatible(daemonProtocol: Int)
}
