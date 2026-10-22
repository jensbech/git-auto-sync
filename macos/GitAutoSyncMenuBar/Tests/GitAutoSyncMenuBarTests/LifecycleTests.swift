import XCTest
@testable import GitAutoSyncMenuBar

@MainActor
final class LifecycleTests: XCTestCase {
    func testAppStateIsReleasedAndStopsItsSubscriber() async throws {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], events: [eventJSON(1, repo: "/r/a")], lastEventId: 1))
        fake.daemon.start()
        defer { fake.daemon.stop() }

        weak var weakState: AppState?
        weak var weakClient: DaemonClient?
        do {
            let client = DaemonClient(socketPath: fake.path, requestTimeout: 2)
            weakClient = client
            let state = AppState(client: client, notifier: RecordingNotifier(), launcher: .disabled("tests"), eventStore: env.eventStore(socketPath: fake.path))
            weakState = state
            let ready = await eventually { state.connection == .connected && state.allEvents.count == 1 }
            XCTAssertTrue(ready)
            await state.syncNow("/r/a")
            await state.refreshStatus()
        }

        let released = await eventually { weakState == nil }
        XCTAssertTrue(released, "AppState leaked")
        let clientReleased = await eventually { weakClient == nil }
        XCTAssertTrue(clientReleased, "DaemonClient leaked: the subscriber thread kept running")
        let connectionsBefore = fake.daemon.accepted
        try await Task.sleep(for: .seconds(1.5))
        XCTAssertEqual(fake.daemon.accepted, connectionsBefore, "a released AppState must not keep reconnecting")
    }

    func testMenuCommandRequestsAreCounters() {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let state = env.makeState(socketPath: "/tmp/gas-a4-none/m.sock")
        state.requestAddRepository()
        state.requestFocusSearch()
        state.requestFocusSearch()
        state.requestRemoveSelected()
        XCTAssertEqual(state.addRepoRequest, 1)
        XCTAssertEqual(state.focusSearchRequest, 2)
        XCTAssertEqual(state.removeSelectedRequest, 0, "nothing is selected")
        XCTAssertNil(state.selectedRepo)
    }
}

