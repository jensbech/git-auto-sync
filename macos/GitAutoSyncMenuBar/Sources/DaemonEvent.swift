import Foundation

struct DaemonEvent: Codable, Identifiable {
    let type: String
    let repo: String
    let message: String?
    let status: String?
    let ts: Int64

    var id: String { "\(ts)-\(type)-\(repo)" }

    var date: Date {
        Date(timeIntervalSince1970: TimeInterval(ts))
    }

    enum CodingKeys: String, CodingKey {
        case type, repo, message, status, ts
    }
}

struct RepoStatus: Identifiable {
    let path: String
    var lastSync: Date?
    var status: SyncStatus
    var lastError: String?
    var lastActivity: String?

    var id: String { path }
}

enum SyncStatus {
    case ok
    case error
    case unknown
}

struct StatusResponse: Codable {
    let type: String?
    let repos: [StatusRepo]?
    let daemon: String?
    let error: String?
    let status: String?
}

struct StatusRepo: Codable {
    let path: String
    let status: String
}

struct SocketCommand: Codable {
    let cmd: String
    var repo: String?
}
