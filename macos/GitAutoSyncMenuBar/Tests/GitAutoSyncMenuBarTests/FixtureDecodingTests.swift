import XCTest
@testable import GitAutoSyncMenuBar

final class FixtureDecodingTests: XCTestCase {
    func testEverySubscriberFrameFromTheRealDaemonDecodes() throws {
        let lines = try Fixtures.sessionLines()
        XCTAssertEqual(lines.count, 53)

        var responses = 0, hellos = 0, states = 0
        var events: [DaemonEvent] = []
        for line in lines {
            switch DaemonClient.parseFrame(line) {
            case .response(let response):
                responses += 1
                XCTAssertTrue(response.ok)
                XCTAssertEqual(response.id, "sub-1")
            case .hello(let hello):
                hellos += 1
                XCTAssertEqual(hello.protocolVersion, 2)
                XCTAssertEqual(hello.version, "dev")
                XCTAssertEqual(hello.repos.map(\.statusValue), [.syncing])
            case .state:
                states += 1
            case .event(let event):
                events.append(event)
            case .ignored:
                XCTFail("frame was not understood: \(String(decoding: line, as: UTF8.self))")
            }
        }
        XCTAssertEqual(responses, 1)
        XCTAssertEqual(hellos, 1)
        XCTAssertEqual(states, 39)
        XCTAssertEqual(events.map(\.id), Array(1...12))
        XCTAssertEqual(
            events.map(\.type),
            ["commit", "push", "synced", "commit", "conflict", "paused", "resumed", "recovered", "commit", "offline", "preflight", "offline"]
        )
        XCTAssertEqual(events[4].code, "conflict")
        XCTAssertEqual(events[10].code, "no_upstream")
        XCTAssertEqual(events[9].code, "network")
        XCTAssertTrue(events.allSatisfy { $0.ts > 1_700_000_000 })
    }

    func testStateFramesCarryEveryStatusTheAppHandles() throws {
        let states = try Fixtures.sessionMessages().compactMap { message -> RepoState? in
            if case .state(let state) = message { return state }
            return nil
        }
        let statuses = Set(states.map(\.statusValue))
        XCTAssertEqual(statuses, [.ok, .syncing, .conflict, .paused, .idle, .offline, .preflight])
        XCTAssertFalse(statuses.contains(.unknown))

        let conflict = try XCTUnwrap(states.first { $0.statusValue == .conflict })
        XCTAssertTrue(conflict.conflict)
        XCTAssertEqual(conflict.errorCode, "conflict")
        XCTAssertEqual(conflict.conflictFiles, ["a.md"])

        let offline = try XCTUnwrap(states.first { $0.statusValue == .offline && $0.nextRetryTs != nil })
        XCTAssertEqual(offline.errorCode, "network")
        XCTAssertNotNil(offline.nextRetry)
        XCTAssertEqual(offline.failures, 1)
        XCTAssertEqual(offline.branch, "main")
        XCTAssertEqual(offline.upstream, "origin/main")

        let preflight = try XCTUnwrap(states.last { $0.statusValue == .preflight })
        XCTAssertEqual(preflight.errorCode, "no_upstream")
        XCTAssertEqual(preflight.preflight, "branch 'main' has no upstream tracking branch")
        XCTAssertEqual(preflight.pollInterval, 600)
    }

    func testStatusAndLateHelloDecode() throws {
        let status = try XCTUnwrap(try Fixtures.response("status_final").result(StatusResult.self))
        XCTAssertEqual(status.protocolVersion, 2)
        XCTAssertEqual(status.daemon, "running")
        XCTAssertEqual(status.repos.count, 2)

        let helloLine = try XCTUnwrap(try Fixtures.responses()["hello_late"])
        guard case .hello(let hello) = DaemonClient.parseFrame(helloLine) else {
            return XCTFail("hello_late did not parse as hello")
        }
        XCTAssertEqual(hello.lastEventId, 11)
        XCTAssertEqual(hello.repos.map(\.statusValue), [.preflight, .offline])
        XCTAssertNotNil(hello.repos[1].lastChange)
        XCTAssertNotNil(hello.repos[1].lastCheck)

        guard case .response(let subscribed) = DaemonClient.parseFrame(try XCTUnwrap(try Fixtures.responses()["subscribe_late"])) else {
            return XCTFail("subscribe_late did not parse as a response")
        }
        XCTAssertTrue(subscribed.ok)
    }

    func testEventsResponsesDecode() throws {
        let all = try Fixtures.response("events").events
        XCTAssertEqual(all.map(\.id), Array(1...11))

        let since = try Fixtures.response("events_since").events
        XCTAssertEqual(since.map(\.id), [10, 11])
        XCTAssertEqual(since.map(\.type), ["offline", "preflight"])
        XCTAssertEqual(since.map(\.code), ["network", "no_upstream"])
    }

    func testGetSettingsDropsNulls() throws {
        let settings = try Fixtures.response("get_settings").resultStrings
        XCTAssertEqual(settings, ["repo": "/tmp/gas-fixture/repo"])
    }

    func testAddResponsesExposeTheResolvedRoot() throws {
        let added = try Fixtures.response("add_preflight").resultStrings
        XCTAssertEqual(added["status"], "added")
        XCTAssertEqual(added["repo"], "/private/tmp/gas-fixture/pf")
    }

    @MainActor
    func testErrorResponsesBecomeReadableNotices() throws {
        let cases: [(String, String?, String)] = [
            ("add_not_a_repo", "/tmp/gas-fixture", "Not a git repository: /tmp/gas-fixture"),
            ("sync_now_unknown", "/nonexistent/repo", "/nonexistent/repo is not being watched"),
            ("remove_not_found", "/nonexistent/repo", "Not monitoring /nonexistent/repo"),
            ("unknown_command", nil, "The daemon does not support this command. Update it with `just install`."),
            ("sync_now_paused", "/tmp/gas-fixture/repo", "Sync skipped: /tmp/gas-fixture/repo is paused"),
            ("abort_rebase_none", "/tmp/gas-fixture/repo", "No rebase in progress in /tmp/gas-fixture/repo"),
            ("missing_repo", nil, "The daemon rejected the request: missing repo"),
            ("set_settings_bad", "/tmp/gas-fixture/repo", "pollInterval must be a whole number of seconds between 30 and 86400, got \"5\""),
        ]
        for (name, repo, expected) in cases {
            let response = try Fixtures.response(name)
            XCTAssertFalse(response.ok, name)
            XCTAssertNotNil(response.code, name)
            XCTAssertEqual(AppState.failureMessage(response, repo: repo), expected, name)
        }
    }

    func testUnknownStatusAndMissingFieldsAreTolerated() throws {
        let line = Data(#"{"type":"state","repo":"/r","data":{"path":"/r","status":"from-the-future"}}"#.utf8)
        guard case .state(let state) = DaemonClient.parseFrame(line) else {
            return XCTFail("state frame not parsed")
        }
        XCTAssertEqual(state.statusValue, .unknown)
        XCTAssertEqual(state.failures, 0)
        XCTAssertFalse(state.watching)

        if case .ignored = DaemonClient.parseFrame(Data("not json".utf8)) {} else { XCTFail("garbage must be ignored") }
        if case .ignored = DaemonClient.parseFrame(Data(#"{"type":"commit","repo":"/r"}"#.utf8)) {} else { XCTFail("event without id must be ignored") }
    }
}
