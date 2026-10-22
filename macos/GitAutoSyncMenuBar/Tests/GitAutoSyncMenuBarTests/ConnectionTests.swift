import XCTest
@testable import GitAutoSyncMenuBar

@MainActor
final class ConnectionTests: XCTestCase {
    private var env: TestEnvironment!

    override func setUp() async throws {
        env = TestEnvironment()
    }

    override func tearDown() async throws {
        env.cleanUp()
    }

    func testMissingSocketMeansDaemonNotRunning() async {
        let state = env.makeState(socketPath: "/tmp/gas-a4-none/\(UUID().uuidString.prefix(8)).sock")
        let settled = await eventually { state.connection == .daemonNotRunning }
        XCTAssertTrue(settled)
        XCTAssertFalse(state.daemonRunning)
        XCTAssertEqual(state.summary, "Daemon not running")
    }

    func testProtocolOneHelloIsIncompatible() async {
        let fake = ScriptedDaemon(DaemonScript(protocolVersion: 1))
        fake.daemon.start()
        defer { fake.daemon.stop() }
        let state = env.makeState(socketPath: fake.path)
        let incompatible = await eventually { state.connection == .incompatible(daemonProtocol: 1) }
        XCTAssertTrue(incompatible)
        XCTAssertEqual(state.summary, "Daemon is outdated")
        XCTAssertTrue(state.repos.isEmpty)
    }

    func testFutureProtocolIsIncompatible() async {
        let fake = ScriptedDaemon(DaemonScript(protocolVersion: 3, repos: [repoJSON("/r/a", status: "ok")]))
        fake.daemon.start()
        defer { fake.daemon.stop() }
        let state = env.makeState(socketPath: fake.path)
        let incompatible = await eventually { state.connection == .incompatible(daemonProtocol: 3) }
        XCTAssertTrue(incompatible)
        XCTAssertTrue(state.repos.isEmpty)
    }

    func testLegacyDaemonRejectingSubscribeIsIncompatible() async {
        let fake = FakeDaemon { daemon, connection in
            connection.send(["type": "hello", "daemon": "running", "repos": []])
            while let request = connection.readRequest() {
                daemon.record(request)
                connection.send(["error": "unknown command"])
            }
        }
        fake.start()
        defer { fake.stop() }
        let state = env.makeState(socketPath: fake.path)
        let incompatible = await eventually { state.connection == .incompatible(daemonProtocol: 1) }
        XCTAssertTrue(incompatible)
    }

    func testReconnectsAfterDaemonRestart() async throws {
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], events: [eventJSON(1, repo: "/r/a")], lastEventId: 1))
        fake.daemon.start()
        defer { fake.daemon.stop() }
        let state = env.makeState(socketPath: fake.path)
        let connected = await eventually { state.connection == .connected && state.allEvents.count == 1 }
        XCTAssertTrue(connected)

        fake.daemon.stop()
        let lost = await eventually { state.connection != .connected }
        XCTAssertTrue(lost)

        fake.update {
            $0.repos = [repoJSON("/r/a", status: "ok"), repoJSON("/r/b", status: "idle")]
            $0.events = [eventJSON(1, repo: "/r/a"), eventJSON(2, repo: "/r/b")]
            $0.lastEventId = 2
        }
        fake.daemon.start()
        let back = await eventually(timeout: 8) { state.connection == .connected && state.repos.count == 2 && state.allEvents.count == 2 }
        XCTAssertTrue(back, "connection: \(state.connection), repos: \(state.repos.count), events: \(state.allEvents.count)")
        XCTAssertEqual(state.allEvents.map(\.id), [1, 2])
        XCTAssertEqual(fake.daemon.requests("subscribe").count, 2)
    }

    func testRequestTimeoutWhileSubscriberIsHealthy() async {
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], silentCommands: ["abort_rebase", "sync_now"]))
        fake.daemon.start()
        defer { fake.daemon.stop() }
        let state = env.makeState(socketPath: fake.path, requestTimeout: 0.5)
        let connected = await eventually { state.connection == .connected }
        XCTAssertTrue(connected)

        await state.syncNow("/r/a")
        XCTAssertEqual(state.notice?.text, "The daemon is busy and did not answer in time. The command may still finish.")
        XCTAssertEqual(state.notice?.isError, true)
        XCTAssertEqual(state.connection, .connected)
        XCTAssertEqual(fake.daemon.requests("sync_now").count, 1, "mutating commands are never retried")
    }

    func testDaemonRestartMidRequest() async {
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], dropOnceCommands: ["status", "pause"]))
        fake.daemon.start()
        defer { fake.daemon.stop() }
        let state = env.makeState(socketPath: fake.path)
        let connected = await eventually { state.connection == .connected }
        XCTAssertTrue(connected)

        await state.setPaused("/r/a", paused: true)
        XCTAssertEqual(state.notice?.text, "The daemon closed the connection before answering. It may have restarted; try again.")
        XCTAssertEqual(fake.daemon.requests("pause").count, 1)

        state.dismissNotice()
        await state.refreshStatus()
        XCTAssertEqual(fake.daemon.requests("status").count, 2, "read-only commands retry once")
        XCTAssertNil(state.notice)
        XCTAssertEqual(state.repos.map(\.path), ["/r/a"])
    }

    func testReconnectStormBacksOff() async {
        let fake = FakeDaemon { daemon, connection in
            guard let request = connection.readRequest() else { return }
            daemon.record(request)
            let id = request["id"] as? String ?? ""
            connection.send(["id": id, "ok": true])
            connection.send(["type": "hello", "protocol": 2, "version": "flaky", "repos": [], "lastEventId": 0])
        }
        fake.start()
        defer { fake.stop() }
        let state = env.makeState(socketPath: fake.path)
        try? await Task.sleep(for: .seconds(4))
        let connects = fake.accepted
        print("reconnect storm: \(connects) subscriber connections in 4 s")
        XCTAssertGreaterThanOrEqual(connects, 2)
        XCTAssertLessThanOrEqual(connects, 5)
        _ = state
    }

    func testRepeatedReconnectNowCallsAreCoalesced() async {
        let fake = FakeDaemon { daemon, connection in
            guard let request = connection.readRequest() else { return }
            daemon.record(request)
        }
        fake.start()
        defer { fake.stop() }
        let client = DaemonClient(socketPath: fake.path, requestTimeout: 1)
        client.start()
        defer { client.stop() }
        let first = await eventually { fake.accepted >= 1 }
        XCTAssertTrue(first)
        for _ in 0..<25 { client.reconnectNow() }
        try? await Task.sleep(for: .milliseconds(1500))
        let connects = fake.accepted
        print("reconnectNow x25: \(connects) connections in 1.5 s")
        XCTAssertLessThanOrEqual(connects, 4)
    }

    func testReconnectDelaySchedule() {
        XCTAssertEqual(DaemonClient.reconnectDelay(quickDrops: 0), 0.5)
        XCTAssertEqual(DaemonClient.reconnectDelay(quickDrops: 1), 1)
        XCTAssertEqual(DaemonClient.reconnectDelay(quickDrops: 3), 4)
        XCTAssertEqual(DaemonClient.reconnectDelay(quickDrops: 50), DaemonClient.maxRetryDelay)
    }
}
