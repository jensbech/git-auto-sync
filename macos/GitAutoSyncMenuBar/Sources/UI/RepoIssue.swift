import Foundation

enum PreflightKind: Equatable {
    case rebaseInProgress, mergeInProgress, noUpstream, upstreamGone, detachedHead, repoMissing, untrustedRepo, other

    static func detect(message: String, code: String?) -> PreflightKind {
        switch code {
        case "rebase_in_progress": return .rebaseInProgress
        case "merge_in_progress": return .mergeInProgress
        case "no_upstream": return .noUpstream
        case "upstream_gone": return .upstreamGone
        case "detached_head": return .detachedHead
        case "repo_missing": return .repoMissing
        case "untrusted_repo": return .untrustedRepo
        default: break
        }
        let haystack = message.lowercased()
        if haystack.contains("rebase in progress") { return .rebaseInProgress }
        if haystack.contains("merge in progress") { return .mergeInProgress }
        if haystack.contains("dubious ownership") || haystack.contains("safe.directory") { return .untrustedRepo }
        if haystack.contains("upstream is gone") || haystack.contains("upstream branch is gone") || haystack.contains("remote branch was deleted") { return .upstreamGone }
        if haystack.contains("no upstream") { return .noUpstream }
        if haystack.contains("detached head") { return .detachedHead }
        if RepoIssue.looksMissing(haystack) { return .repoMissing }
        return .other
    }
}

enum RepoIssue: Equatable {
    case conflict(files: [String])
    case preflight(PreflightKind, message: String)
    case offline(message: String?, nextRetry: Date?)
    case error(code: String?, message: String)

    static func of(_ repo: RepoState, events: [DaemonEvent]) -> RepoIssue? {
        if repo.conflict || repo.statusValue == .conflict {
            return .conflict(files: repo.conflictFiles ?? [])
        }
        if let pf = repo.preflight, !pf.isEmpty {
            return .preflight(.detect(message: pf, code: preflightCode(repo, events: events)), message: pf)
        }
        switch repo.statusValue {
        case .preflight:
            let last = events.last(where: { $0.type == "preflight" })
            let message = last?.message ?? repo.lastError ?? "The repository needs attention before it can sync."
            return .preflight(.detect(message: message, code: preflightCode(repo, events: events)), message: message)
        case .offline:
            return .offline(message: repo.lastError, nextRetry: repo.nextRetry)
        case .error:
            let message = repo.lastError ?? ""
            if repo.errorCode == "config", looksMissing(message.lowercased()) {
                return .preflight(.repoMissing, message: message)
            }
            return .error(code: repo.errorCode, message: message)
        default:
            return nil
        }
    }

    static func looksMissing(_ lowercased: String) -> Bool {
        lowercased.contains("no such file or directory") || lowercased.contains("folder is missing") || lowercased.contains("repository folder")
    }

    private static func preflightCode(_ repo: RepoState, events: [DaemonEvent]) -> String? {
        if let code = repo.errorCode, !code.isEmpty { return code }
        return events.last(where: { $0.type == "preflight" })?.code
    }
}

enum Durations {
    static func human(_ sec: Int) -> String {
        if sec <= 0 { return "—" }
        if sec < 60 { return "\(sec)s" }
        if sec < 3600 {
            let m = sec / 60
            let s = sec % 60
            return s == 0 ? "\(m)m" : "\(m)m \(s)s"
        }
        let h = sec / 3600
        let m = (sec % 3600) / 60
        return m == 0 ? "\(h)h" : "\(h)h \(m)m"
    }

    static func countdown(to date: Date, now: Date) -> String? {
        let remaining = Int(date.timeIntervalSince(now).rounded(.up))
        guard remaining > 0 else { return nil }
        return human(remaining)
    }

    static func coarseCountdown(to date: Date, now: Date) -> String? {
        let remaining = Int(date.timeIntervalSince(now).rounded(.up))
        guard remaining > 0 else { return nil }
        if remaining < 60 { return "\(remaining)s" }
        if remaining < 3600 { return "\((remaining + 59) / 60) min" }
        return human(remaining)
    }
}

extension String {
    var sentenceCased: String {
        guard let first = first else { return self }
        return first.uppercased() + dropFirst()
    }
}
