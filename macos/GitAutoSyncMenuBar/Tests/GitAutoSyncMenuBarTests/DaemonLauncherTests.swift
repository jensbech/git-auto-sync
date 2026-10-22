import XCTest
@testable import GitAutoSyncMenuBar

final class FakeBinaries {
    let directory: URL
    let log: URL

    init() throws {
        directory = URL(fileURLWithPath: "/tmp").appendingPathComponent("gas-a4-bin-\(UUID().uuidString.prefix(8))")
        log = directory.appendingPathComponent("calls.log")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    func make(_ name: String, exit status: Int32, output: String = "") throws -> String {
        let url = directory.appendingPathComponent(name)
        let script = """
        #!/bin/sh
        echo "\(name) $*" >> "\(log.path)"
        printf '%s' "\(output)"
        exit \(status)
        """
        try script.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url.path
    }

    func touch(_ name: String) throws -> String {
        let url = directory.appendingPathComponent(name)
        try Data().write(to: url)
        return url.path
    }

    var calls: [String] {
        ((try? String(contentsOf: log, encoding: .utf8)) ?? "").split(separator: "\n").map(String.init)
    }

    deinit {
        try? FileManager.default.removeItem(at: directory)
    }
}

final class DaemonLauncherTests: XCTestCase {
    func testInstalledServiceIsKickstarted() throws {
        let bins = try FakeBinaries()
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: try bins.touch("agent.plist"),
            cliSearchPaths: [try bins.make("cli", exit: 0)],
            uid: 501
        )
        XCTAssertEqual(launcher.startBlocking(), .kickstarted)
        XCTAssertEqual(bins.calls, ["launchctl kickstart -k gui/501/git-auto-sync-daemon"])
    }

    func testKickstartFailureFallsBackToDaemonStart() throws {
        let bins = try FakeBinaries()
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 113, output: "Could not find service"),
            launchAgentPath: try bins.touch("agent.plist"),
            cliSearchPaths: ["/nonexistent/cli", try bins.make("cli", exit: 0)],
            uid: 501
        )
        XCTAssertEqual(launcher.startBlocking(), .started)
        XCTAssertEqual(bins.calls, ["launchctl kickstart -k gui/501/git-auto-sync-daemon", "cli daemon start"])
    }

    func testMissingLaunchAgentRunsDaemonEnable() throws {
        let bins = try FakeBinaries()
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: bins.directory.appendingPathComponent("missing.plist").path,
            cliSearchPaths: [try bins.make("cli", exit: 0)],
            uid: 501
        )
        XCTAssertFalse(launcher.isServiceInstalled)
        XCTAssertEqual(launcher.startBlocking(), .installed)
        XCTAssertEqual(bins.calls, ["cli daemon enable"], "launchctl must not be touched when nothing is installed")
    }

    func testEnableFailureIsReported() throws {
        let bins = try FakeBinaries()
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: bins.directory.appendingPathComponent("missing.plist").path,
            cliSearchPaths: [try bins.make("cli", exit: 1, output: "permission denied")],
            uid: 501
        )
        XCTAssertEqual(launcher.startBlocking(), .failed("git-auto-sync daemon enable: permission denied"))
    }

    func testMissingCLIExplainsHowToInstall() throws {
        let bins = try FakeBinaries()
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: bins.directory.appendingPathComponent("missing.plist").path,
            cliSearchPaths: ["/nonexistent/cli"],
            uid: 501
        )
        guard case .failed(let message) = launcher.startBlocking() else { return XCTFail("expected failure") }
        XCTAssertTrue(message.contains("just install"), message)
        XCTAssertTrue(bins.calls.isEmpty)
    }

    func testDisabledLauncherRunsNothing() throws {
        let bins = try FakeBinaries()
        var launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: try bins.touch("agent.plist"),
            cliSearchPaths: [try bins.make("cli", exit: 0)],
            uid: 501
        )
        launcher.disabledReason = "custom socket"
        XCTAssertEqual(launcher.startBlocking(), .failed("custom socket"))
        XCTAssertTrue(bins.calls.isEmpty)
    }
}

@MainActor
final class StartDaemonTests: XCTestCase {
    func testStartDaemonInstallsServiceAndReportsSuccess() async throws {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let bins = try FakeBinaries()
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")]))
        let cli = try bins.make("cli", exit: 0)
        let launcher = DaemonLauncher(
            launchctlPath: try bins.make("launchctl", exit: 0),
            launchAgentPath: bins.directory.appendingPathComponent("missing.plist").path,
            cliSearchPaths: [cli],
            uid: 501
        )
        let state = env.makeState(socketPath: fake.path, launcher: launcher)
        let down = await eventually { state.connection == .daemonNotRunning }
        XCTAssertTrue(down)

        var notices: [String] = []
        let watcher = Task { @MainActor in
            var last: UUID?
            while !Task.isCancelled {
                if let notice = state.notice, notice.id != last {
                    last = notice.id
                    notices.append(notice.text)
                }
                try? await Task.sleep(for: .milliseconds(5))
            }
        }
        let starting = Task { @MainActor in await state.startDaemon() }
        let installing = await eventually { notices.contains { $0.contains("not installed") } }
        XCTAssertTrue(installing)
        fake.daemon.start()
        defer { fake.daemon.stop() }
        await starting.value
        watcher.cancel()

        XCTAssertEqual(bins.calls, ["cli daemon enable"])
        XCTAssertEqual(state.connection, .connected)
        XCTAssertEqual(state.notice?.text, "Background service installed. The daemon now starts at login.")
        XCTAssertEqual(state.notice?.isError, false)
        XCTAssertFalse(state.isStartingDaemon)
    }

    func testStartDaemonFailureIsAnErrorNotice() async throws {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let state = env.makeState(socketPath: "/tmp/gas-a4-none/x.sock", launcher: DaemonLauncher(
            launchAgentPath: "/nonexistent/agent.plist",
            cliSearchPaths: ["/nonexistent/cli"],
            uid: 501
        ))
        await state.startDaemon()
        XCTAssertEqual(state.notice?.isError, true)
        XCTAssertTrue(state.notice?.text.hasPrefix("Could not start the daemon: git-auto-sync was not found") == true, state.notice?.text ?? "")
    }

    func testCustomSocketNeverStartsTheRealService() {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let client = DaemonClient(socketPath: "/tmp/gas-a4-none/custom.sock")
        let state = AppState(client: client, notifier: RecordingNotifier(), eventStore: env.eventStore(socketPath: client.socketPath))
        let mirror = Mirror(reflecting: state)
        let launcher = mirror.children.first { $0.label == "launcher" }?.value as? DaemonLauncher
        XCTAssertNotNil(launcher?.disabledReason)
    }
}
