import XCTest
@testable import GitAutoSyncMenuBar

@MainActor
final class NotificationTransitionTests: XCTestCase {
    private var env: TestEnvironment!
    private var notifier: RecordingNotifier!
    private var state: AppState!

    override func setUp() async throws {
        env = TestEnvironment()
        notifier = RecordingNotifier()
        state = env.makeState(socketPath: "/tmp/gas-a4-none/\(UUID().uuidString.prefix(8)).sock", notifier: notifier)
    }

    override func tearDown() async throws {
        state = nil
        env.cleanUp()
    }

    private func repo(_ status: String, code: String? = nil, path: String = "/r/notes", extra: [String: Any] = [:]) throws -> RepoState {
        let data = try JSONSerialization.data(withJSONObject: repoJSON(path, status: status, code: code, extra: extra))
        return try JSONDecoder().decode(RepoState.self, from: data)
    }

    private func hello(_ repos: [RepoState]) throws -> DaemonHello {
        let encoded = try repos.map { try JSONSerialization.jsonObject(with: JSONEncoder().encode($0)) }
        let data = try JSONSerialization.data(withJSONObject: ["type": "hello", "protocol": 2, "repos": encoded, "lastEventId": 0])
        return try JSONDecoder().decode(DaemonHello.self, from: data)
    }

    private func connect(_ repos: [RepoState] = []) throws {
        state.handle(.hello(try hello(repos)))
    }

    private func send(_ states: RepoState...) {
        for s in states { state.handle(.state(s)) }
    }

    func testErrorSyncingErrorNotifiesOnce() throws {
        try connect([repo("ok")])
        send(try repo("error", code: "push_rejected"), try repo("syncing", code: "push_rejected"), try repo("error", code: "push_rejected"))
        send(try repo("syncing"), try repo("error", code: "push_rejected"))
        XCTAssertEqual(notifier.delivered.count, 1)
        XCTAssertEqual(notifier.delivered.first?.repo, "/r/notes")
    }

    func testConflictToOkGivesExactlyOneResolved() throws {
        try connect([repo("ok")])
        send(try repo("conflict", code: "conflict"), try repo("syncing", code: "conflict"), try repo("ok"), try repo("syncing"), try repo("ok"))
        XCTAssertEqual(notifier.delivered.map(\.body), [
            "Rebase conflict. Local commits are kept; resolve it to resume syncing.",
            "Conflict resolved. Syncing again.",
        ])
    }

    func testConflictSurvivingPauseIdleAndOfflineDoesNotRenotify() throws {
        try connect([repo("ok")])
        send(
            try repo("conflict", code: "conflict"),
            try repo("paused", code: "conflict"),
            try repo("idle", code: "conflict"),
            try repo("syncing", code: "conflict"),
            try repo("conflict", code: "conflict"),
            try repo("offline", code: "network"),
            try repo("conflict", code: "conflict")
        )
        XCTAssertEqual(notifier.delivered.count, 1)
        XCTAssertTrue(notifier.cleared.isEmpty)
    }

    func testOfflineNeverNotifies() throws {
        try connect([repo("ok")])
        send(
            try repo("syncing"), try repo("offline", code: "network"), try repo("offline", code: "network"),
            try repo("error", code: "timeout"), try repo("error", code: "network"), try repo("ok"),
            try repo("offline", code: "timeout"), try repo("ok")
        )
        XCTAssertTrue(notifier.delivered.isEmpty)
        XCTAssertTrue(notifier.cleared.isEmpty)
    }

    func testErrorToOkClearsWithoutResolvedNotification() throws {
        try connect([repo("ok")])
        send(try repo("error", code: "auth"), try repo("ok"))
        XCTAssertEqual(notifier.delivered.count, 1)
        XCTAssertEqual(notifier.cleared, ["/r/notes"])
    }

    func testPreflightCodeArrivingLateDoesNotNotifyTwice() throws {
        try connect([repo("idle")])
        send(
            try repo("preflight", extra: ["preflight": "branch 'main' has no upstream tracking branch"]),
            try repo("preflight", code: "no_upstream", extra: ["preflight": "branch 'main' has no upstream tracking branch"])
        )
        XCTAssertEqual(notifier.delivered.count, 1)
        send(try repo("preflight", code: "rebase_in_progress"))
        XCTAssertEqual(notifier.delivered.count, 2)
    }

    func testFirstSnapshotAndReconnectDoNotNotify() throws {
        try connect([repo("conflict", code: "conflict")])
        XCTAssertTrue(notifier.delivered.isEmpty)
        state.handle(.disconnected)
        try connect([repo("idle", code: "conflict")])
        try connect([repo("conflict", code: "conflict")])
        XCTAssertTrue(notifier.delivered.isEmpty)
        try connect([repo("ok")])
        XCTAssertEqual(notifier.delivered.map(\.body), ["Conflict resolved. Syncing again."])
    }

    func testXcodeLicenseAndAuthNotify() throws {
        try connect([repo("ok"), repo("ok", path: "/r/b")])
        send(try repo("error", code: "xcode_license"), try repo("error", code: "auth", path: "/r/b"))
        XCTAssertEqual(notifier.delivered.map(\.repo), ["/r/notes", "/r/b"])
        XCTAssertTrue(notifier.delivered[0].body.contains("xcodebuild -license accept"))
    }

    func testReplayOfRealDaemonSessionNotifiesConflictResolvedAndPreflightOnce() throws {
        for message in try Fixtures.sessionMessages() {
            state.handle(message)
        }
        XCTAssertEqual(notifier.delivered.map(\.repo), [
            "/tmp/gas-fixture/repo",
            "/tmp/gas-fixture/repo",
            "/private/tmp/gas-fixture/pf",
        ])
        XCTAssertEqual(notifier.delivered.map(\.body), [
            "Rebase conflict in 1 file. Local commits are kept; resolve it to resume syncing.",
            "Conflict resolved. Syncing again.",
            "branch 'main' has no upstream tracking branch",
        ])
        XCTAssertEqual(state.repos.map(\.path), ["/private/tmp/gas-fixture/pf", "/private/tmp/gas-fixture/repo", "/tmp/gas-fixture/repo"])
        XCTAssertEqual(state.allEvents.map(\.id), Array(1...12))
    }
}
