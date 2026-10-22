import XCTest
@testable import GitAutoSyncMenuBar

@MainActor
final class EventBackfillTests: XCTestCase {
    private var env: TestEnvironment!

    override func setUp() async throws {
        env = TestEnvironment()
    }

    override func tearDown() async throws {
        env.cleanUp()
    }

    private func event(_ id: UInt64, repo: String = "/r/a") -> DaemonEvent {
        DaemonEvent(id: id, type: "commit", repo: repo, message: "e\(id)", ts: 1_792_697_000 + Int64(id))
    }

    private func offlineState() -> AppState {
        env.makeState(socketPath: "/tmp/gas-a4-none/\(UUID().uuidString.prefix(8)).sock")
    }

    func testIngestDedupesSortsAndGroups() {
        let state = offlineState()
        state.ingest([event(3), event(1, repo: "/r/b"), event(2)])
        state.ingest([event(2), event(5), event(4, repo: "/r/b"), event(5)])
        XCTAssertEqual(state.allEvents.map(\.id), [1, 2, 3, 4, 5])
        XCTAssertEqual(state.eventsFor(repo: "/r/a").map(\.id), [2, 3, 5])
        XCTAssertEqual(state.eventsFor(repo: "/r/b").map(\.id), [1, 4])
        XCTAssertEqual(state.lastSeenEventID, 5)
    }

    func testCapKeepsNewestEvents() {
        let state = offlineState()
        state.ingest((1...2500).map { event(UInt64($0), repo: $0 % 2 == 0 ? "/r/a" : "/r/b") })
        XCTAssertEqual(state.allEvents.count, state.maxEvents)
        XCTAssertEqual(state.allEvents.first?.id, 501)
        XCTAssertEqual(state.allEvents.last?.id, 2500)
        XCTAssertEqual(state.eventsFor(repo: "/r/a").count + state.eventsFor(repo: "/r/b").count, state.maxEvents)
        state.ingest([event(10)])
        XCTAssertEqual(state.allEvents.first?.id, 501)
        XCTAssertEqual(state.allEvents.count, state.maxEvents)
    }

    func testBackfillMergesWithLiveEventsAndSkipsDuplicates() async throws {
        let repos = [repoJSON("/r/a", status: "ok")]
        let fake = ScriptedDaemon(DaemonScript(repos: repos, events: (1...5).map { eventJSON($0, repo: "/r/a") }, lastEventId: 5))
        fake.daemon.start()
        defer { fake.daemon.stop() }

        let state = env.makeState(socketPath: fake.path)
        let backfilled = await eventually { state.allEvents.count == 5 }
        XCTAssertTrue(backfilled)
        XCTAssertEqual(fake.daemon.requests("events").first?["limit"] as? Int, state.initialBackfill)
        XCTAssertNil(fake.daemon.requests("events").first?["since"])

        fake.daemon.push(eventJSON(6, repo: "/r/a"))
        fake.daemon.push(eventJSON(5, repo: "/r/a"))
        fake.daemon.push(eventJSON(7, repo: "/r/a"))
        let live = await eventually { state.allEvents.count == 7 }
        XCTAssertTrue(live)
        XCTAssertEqual(state.allEvents.map(\.id), Array(1...7))
    }

    func testReconnectBackfillsOnlyTheGapAndCounterResetClears() async throws {
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], events: (1...3).map { eventJSON($0, repo: "/r/a") }, lastEventId: 3))
        fake.daemon.start()
        defer { fake.daemon.stop() }

        let state = env.makeState(socketPath: fake.path)
        let first = await eventually { state.allEvents.count == 3 }
        XCTAssertTrue(first)

        fake.daemon.stop()
        fake.update {
            $0.events = (1...6).map { eventJSON($0, repo: "/r/a") }
            $0.lastEventId = 6
        }
        fake.daemon.start()
        let gap = await eventually(timeout: 8) { state.allEvents.count == 6 }
        XCTAssertTrue(gap)
        let since = fake.daemon.requests("events").compactMap { ($0["since"] as? NSNumber)?.uint64Value }
        XCTAssertEqual(since, [3])
        XCTAssertEqual(state.allEvents.map(\.id), Array(1...6))

        fake.daemon.stop()
        fake.update {
            $0.events = [eventJSON(1, repo: "/r/new"), eventJSON(2, repo: "/r/new")]
            $0.lastEventId = 2
        }
        fake.daemon.start()
        let reset = await eventually(timeout: 8) { state.allEvents.map(\.repo) == ["/r/new", "/r/new"] }
        XCTAssertTrue(reset, "events after counter reset: \(state.allEvents.map(\.id))")
        XCTAssertEqual(state.eventsFor(repo: "/r/a"), [])
        XCTAssertEqual(state.lastSeenEventID, 2)
    }

    func testLastSeenEventIdSurvivesRelaunchAndLimitsBackfill() async throws {
        let fake = ScriptedDaemon(DaemonScript(repos: [repoJSON("/r/a", status: "ok")], events: (1...4).map { eventJSON($0, repo: "/r/a") }, lastEventId: 4))
        fake.daemon.start()
        defer { fake.daemon.stop() }

        var first: AppState? = env.makeState(socketPath: fake.path)
        let loaded = await eventually { first?.allEvents.count == 4 }
        XCTAssertTrue(loaded)
        let key = EventStore.keyPrefix + fake.path
        let persisted = await eventually { (self.env.defaults.object(forKey: key) as? NSNumber)?.uint64Value == 4 }
        XCTAssertTrue(persisted)
        first = nil

        fake.update {
            $0.events = (1...7).map { eventJSON($0, repo: "/r/a") }
            $0.lastEventId = 7
        }
        let relaunched = env.makeState(socketPath: fake.path)
        XCTAssertEqual(relaunched.allEvents.map(\.id), [1, 2, 3, 4], "cached history is shown before the daemon answers")
        let caughtUp = await eventually { relaunched.allEvents.count == 7 }
        XCTAssertTrue(caughtUp)
        let since = fake.daemon.requests("events").compactMap { ($0["since"] as? NSNumber)?.uint64Value }
        XCTAssertEqual(since, [4])
        XCTAssertEqual(relaunched.allEvents.map(\.id), Array(1...7))
    }

    func testLastSeenKeyIsNamespacedBySocketPath() {
        let a = env.eventStore(socketPath: "/tmp/a.sock")
        let b = env.eventStore(socketPath: "/tmp/b.sock")
        XCTAssertNotEqual(a.lastSeenKey, b.lastSeenKey)
        XCTAssertEqual(a.lastSeenKey, "gas.lastSeenEventId./tmp/a.sock")
        XCTAssertNotEqual(a.fileURL, b.fileURL)
    }

    func testMissingCacheIgnoresPersistedIdSoHistoryIsRefetched() {
        let store = env.eventStore(socketPath: "/tmp/c.sock")
        env.defaults.set(NSNumber(value: UInt64(900)), forKey: store.lastSeenKey)
        let loaded = store.load()
        XCTAssertTrue(loaded.events.isEmpty)
        XCTAssertEqual(loaded.lastSeen, 0)
    }

    func testLargeBackfillDoesNotBlockTheMainActor() async throws {
        let repos = (0..<20).map { repoJSON("/r/repo\($0)", status: "ok") }
        let events = (2...2001).map { eventJSON(UInt64($0), repo: "/r/repo\($0 % 20)", type: $0 % 3 == 0 ? "push" : "commit") }
        let fake = ScriptedDaemon(DaemonScript(repos: repos, events: events, lastEventId: 2001))
        fake.daemon.start()
        defer { fake.daemon.stop() }

        let seed = env.eventStore(socketPath: fake.path)
        seed.scheduleSave(events: [event(1, repo: "/r/repo1")], lastSeen: 1)
        await seed.flush()

        let probe = MainActorStallProbe()
        probe.start()
        let started = Date()
        let state = env.makeState(socketPath: fake.path, requestTimeout: 5)
        XCTAssertEqual(state.allEvents.map(\.id), [1])
        let done = await eventually(timeout: 10) { state.allEvents.last?.id == 2001 }
        let elapsed = Date().timeIntervalSince(started)
        probe.stop()
        XCTAssertTrue(done)
        XCTAssertEqual(state.allEvents.count, state.maxEvents)
        XCTAssertEqual(state.allEvents.first?.id, 2)
        let request = try XCTUnwrap(fake.daemon.requests("events").first)
        XCTAssertEqual(request["limit"] as? Int, state.maxEvents)
        XCTAssertEqual((request["since"] as? NSNumber)?.uint64Value, 1)

        let response = DaemonResponse.decode(try JSONSerialization.data(withJSONObject: ["id": "x", "ok": true, "result": ["events": events]]))!
        let decodeStart = Date()
        let decoded = response.events
        let decodeTime = Date().timeIntervalSince(decodeStart)

        let fresh = offlineState()
        let ingestStart = Date()
        fresh.ingest(decoded)
        let ingestTime = Date().timeIntervalSince(ingestStart)

        print(String(format: "2000-event backfill: total %.0f ms, worst main-actor stall %.1f ms, decode (off main) %.1f ms, ingest (main) %.1f ms",
                     elapsed * 1000, probe.worstGap * 1000, decodeTime * 1000, ingestTime * 1000))
        XCTAssertLessThan(ingestTime, 0.05)
        XCTAssertLessThan(probe.worstGap, 0.1)
    }
}

@MainActor
final class MainActorStallProbe {
    private(set) var worstGap: TimeInterval = 0
    private var task: Task<Void, Never>?

    func start() {
        task = Task { @MainActor [weak self] in
            var last = Date()
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(2))
                let now = Date()
                if let self { self.worstGap = max(self.worstGap, now.timeIntervalSince(last)) }
                last = now
            }
        }
    }

    func stop() {
        task?.cancel()
        task = nil
    }
}
