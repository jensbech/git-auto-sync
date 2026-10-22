import XCTest
@testable import GitAutoSyncMenuBar

enum LegacyFixture {
    static func replies() throws -> [String: [Data]] {
        let data = try Data(contentsOf: Fixtures.url("protocol1-replies", "json"))
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: [Any]])
        return try object.mapValues { lines in
            try lines.map { try JSONSerialization.data(withJSONObject: $0) }
        }
    }
}

final class LegacyReplyTests: XCTestCase {
    func testProtocolOneRepliesAreRecognised() throws {
        let replies = try LegacyFixture.replies()
        XCTAssertEqual(Set(replies.keys), ["status", "sync_now", "subscribe", "events"])
        for (name, lines) in replies {
            XCTAssertEqual(lines.count, 2, name)
            guard case .skip = DaemonClient.classifyReply(lines[0], id: "req-1") else {
                return XCTFail("\(name): the unsolicited protocol-1 hello must be skipped")
            }
            guard case .legacy = DaemonClient.classifyReply(lines[1], id: "req-1") else {
                return XCTFail("\(name): \(String(decoding: lines[1], as: UTF8.self)) must be detected as protocol 1")
            }
        }
    }

    func testProtocolTwoRepliesAreNotLegacy() throws {
        let responses = try Fixtures.responses()
        for name in ["status", "sync_now", "add_not_a_repo", "unknown_command", "events", "get_settings"] {
            let line = try XCTUnwrap(responses[name])
            let id = try XCTUnwrap(try JSONSerialization.jsonObject(with: line) as? [String: Any])["id"] as? String
            guard case .response = DaemonClient.classifyReply(line, id: try XCTUnwrap(id)) else {
                return XCTFail("\(name) must be accepted as a protocol-2 response")
            }
        }
        let coded = Data(#"{"ok":false,"code":"bad_request","error":"request too large"}"#.utf8)
        guard case .response(let tooLarge) = DaemonClient.classifyReply(coded, id: "req-9") else {
            return XCTFail("a coded reply without id is still a protocol-2 response")
        }
        XCTAssertEqual(tooLarge.code, "bad_request")
        guard case .skip = DaemonClient.classifyReply(Data(#"{"id":"other","ok":true}"#.utf8), id: "req-9") else {
            return XCTFail("a reply for another id must be skipped")
        }
    }
}

@MainActor
final class LegacyDaemonTests: XCTestCase {
    func testRequestToProtocolOneDaemonMarksItOutdated() async throws {
        let env = TestEnvironment()
        defer { env.cleanUp() }
        let replies = try LegacyFixture.replies()
        let fake = FakeDaemon { daemon, connection in
            let hello = replies["status"]![0]
            connection.sendRaw(hello + Data("\n".utf8))
            while let request = connection.readRequest() {
                daemon.record(request)
                let cmd = request["cmd"] as? String ?? ""
                let reply = replies[cmd]?[1] ?? replies["events"]![1]
                connection.sendRaw(reply + Data("\n".utf8))
            }
        }
        fake.start()
        defer { fake.stop() }

        let state = env.makeState(socketPath: fake.path, requestTimeout: 5)
        let outdated = await eventually { state.connection == .incompatible(daemonProtocol: 1) }
        XCTAssertTrue(outdated)

        let started = Date()
        await state.syncNow("/tmp/nowhere")
        XCTAssertLessThan(Date().timeIntervalSince(started), 2, "a protocol-1 reply must not wait for the request timeout")
        XCTAssertEqual(state.notice?.text, AppState.outdatedDaemonMessage)
        XCTAssertEqual(state.notice?.isError, true)
        XCTAssertTrue(AppState.outdatedDaemonMessage.contains("just install"))
        XCTAssertTrue(AppState.outdatedDaemonMessage.contains("daemon enable"))
        XCTAssertEqual(state.connection, .incompatible(daemonProtocol: 1))

        state.dismissNotice()
        let statusStarted = Date()
        await state.refreshStatus()
        XCTAssertLessThan(Date().timeIntervalSince(statusStarted), 2)
        XCTAssertEqual(fake.requests("status").count, 1, "an outdated daemon is not retried")
        XCTAssertTrue(state.repos.isEmpty)
    }
}
