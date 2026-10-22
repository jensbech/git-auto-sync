import Foundation
import os

struct DaemonLauncher: Sendable {
    enum Outcome: Equatable, Sendable {
        case kickstarted
        case started
        case installed
        case failed(String)
    }

    static let serviceLabel = "git-auto-sync-daemon"

    static let defaultCLISearchPaths: [String] = {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return [
            "/usr/local/bin/git-auto-sync",
            "/opt/homebrew/bin/git-auto-sync",
            "\(home)/go/bin/git-auto-sync",
            "\(home)/.local/bin/git-auto-sync",
        ]
    }()

    static let live = DaemonLauncher()

    var launchctlPath: String
    var launchAgentPath: String
    var cliSearchPaths: [String]
    var uid: uid_t
    var disabledReason: String?

    init(
        launchctlPath: String = "/bin/launchctl",
        launchAgentPath: String? = nil,
        cliSearchPaths: [String] = DaemonLauncher.defaultCLISearchPaths,
        uid: uid_t = getuid(),
        disabledReason: String? = nil
    ) {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        self.launchctlPath = launchctlPath
        self.launchAgentPath = launchAgentPath ?? "\(home)/Library/LaunchAgents/\(Self.serviceLabel).plist"
        self.cliSearchPaths = cliSearchPaths
        self.uid = uid
        self.disabledReason = disabledReason
    }

    static func disabled(_ reason: String) -> DaemonLauncher {
        DaemonLauncher(disabledReason: reason)
    }

    var isServiceInstalled: Bool {
        FileManager.default.fileExists(atPath: launchAgentPath)
    }

    var cliPath: String? {
        cliSearchPaths.first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    struct ProcessOutcome: Sendable {
        let status: Int32
        let output: String
    }

    func start() async -> Outcome {
        await withCheckedContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                cont.resume(returning: startBlocking())
            }
        }
    }

    func startBlocking() -> Outcome {
        if let disabledReason { return .failed(disabledReason) }

        var failures: [String] = []
        let cli = cliPath

        if isServiceInstalled {
            let target = "gui/\(uid)/\(Self.serviceLabel)"
            if let outcome = Self.run(launchctlPath, ["kickstart", "-k", target]) {
                if outcome.status == 0 { return .kickstarted }
                failures.append("launchctl: \(Self.describe(outcome))")
            }
            if let cli, let outcome = Self.run(cli, ["daemon", "start"]) {
                if outcome.status == 0 { return .started }
                failures.append("git-auto-sync daemon start: \(Self.describe(outcome))")
            }
        }

        guard let cli else {
            failures.append("git-auto-sync was not found in /usr/local/bin, /opt/homebrew/bin, ~/go/bin or ~/.local/bin. Install it with `just install`")
            return .failed(failures.joined(separator: "; "))
        }

        guard let outcome = Self.run(cli, ["daemon", "enable"]) else {
            failures.append("could not run \(cli)")
            return .failed(failures.joined(separator: "; "))
        }
        if outcome.status == 0 { return .installed }
        failures.append("git-auto-sync daemon enable: \(Self.describe(outcome))")
        return .failed(failures.joined(separator: "; "))
    }

    private static func describe(_ outcome: ProcessOutcome) -> String {
        outcome.output.isEmpty ? "exit \(outcome.status)" : outcome.output
    }

    static func run(_ executable: String, _ arguments: [String], timeout: TimeInterval = 20) -> ProcessOutcome? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: executable)
        process.arguments = arguments
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        process.standardInput = FileHandle.nullDevice

        let finished = DispatchSemaphore(value: 0)
        process.terminationHandler = { _ in finished.signal() }
        do {
            try process.run()
        } catch {
            return nil
        }

        let collected = OSAllocatedUnfairLock(initialState: Data())
        let drained = DispatchSemaphore(value: 0)
        let handle = pipe.fileHandleForReading
        DispatchQueue.global(qos: .utility).async {
            let data = handle.readDataToEndOfFile()
            collected.withLock { $0 = data }
            drained.signal()
        }

        if finished.wait(timeout: .now() + timeout) == .timedOut {
            process.terminate()
            return ProcessOutcome(status: -1, output: "timed out after \(Int(timeout)) s")
        }
        _ = drained.wait(timeout: .now() + 2)
        let output = String(data: collected.withLock { $0 }, encoding: .utf8)?
            .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return ProcessOutcome(status: process.terminationStatus, output: output)
    }
}
