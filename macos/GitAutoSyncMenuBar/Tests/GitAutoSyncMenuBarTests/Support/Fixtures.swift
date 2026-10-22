import Foundation
import XCTest
@testable import GitAutoSyncMenuBar

enum Fixtures {
    static func url(_ name: String, _ ext: String) throws -> URL {
        try XCTUnwrap(Bundle.module.url(forResource: name, withExtension: ext, subdirectory: "Fixtures"))
    }

    static func sessionLines() throws -> [Data] {
        let data = try Data(contentsOf: url("subscriber-session", "jsonl"))
        return data.split(separator: UInt8(ascii: "\n")).map { Data($0) }
    }

    static func responses() throws -> [String: Data] {
        let data = try Data(contentsOf: url("responses", "json"))
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        var out: [String: Data] = [:]
        for (key, value) in object {
            out[key] = try JSONSerialization.data(withJSONObject: value)
        }
        return out
    }

    static func response(_ name: String) throws -> DaemonResponse {
        let raw = try XCTUnwrap(try responses()[name], "missing fixture \(name)")
        return try XCTUnwrap(DaemonResponse.decode(raw), "fixture \(name) is not a response")
    }

    static func sessionMessages() throws -> [DaemonMessage] {
        try sessionLines().compactMap { line in
            switch DaemonClient.parseFrame(line) {
            case .hello(let hello): return .hello(hello)
            case .state(let state): return .state(state)
            case .event(let event): return .event(event)
            case .response, .ignored: return nil
            }
        }
    }
}
