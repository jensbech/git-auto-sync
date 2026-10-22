import Darwin
import Foundation
import XCTest
@testable import GitAutoSyncMenuBar

final class FakeConnection: @unchecked Sendable {
    let fd: Int32
    private var pending = Data()
    private let isStopped: @Sendable () -> Bool

    init(fd: Int32, isStopped: @escaping @Sendable () -> Bool) {
        self.fd = fd
        self.isStopped = isStopped
    }

    func readRequest() -> [String: Any]? {
        while true {
            if let newline = pending.firstIndex(of: UInt8(ascii: "\n")) {
                let line = Data(pending[pending.startIndex..<newline])
                pending.removeSubrange(pending.startIndex...newline)
                if line.isEmpty { continue }
                return (try? JSONSerialization.jsonObject(with: line)) as? [String: Any]
            }
            var pfd = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            let ready = poll(&pfd, 1, 100)
            if isStopped() { return nil }
            if ready == 0 { continue }
            if ready < 0 { if errno == EINTR { continue }; return nil }
            var buffer = [UInt8](repeating: 0, count: 65536)
            let n = read(fd, &buffer, buffer.count)
            if n <= 0 { return nil }
            pending.append(contentsOf: buffer[0..<n])
        }
    }

    @discardableResult
    func send(_ object: Any) -> Bool {
        guard var data = try? JSONSerialization.data(withJSONObject: object) else { return false }
        data.append(UInt8(ascii: "\n"))
        return sendRaw(data)
    }

    @discardableResult
    func sendRaw(_ data: Data) -> Bool {
        data.withUnsafeBytes { raw -> Bool in
            guard let base = raw.baseAddress else { return false }
            var offset = 0
            while offset < raw.count {
                let n = write(fd, base + offset, raw.count - offset)
                if n <= 0 { return false }
                offset += n
            }
            return true
        }
    }
}

final class FakeDaemon: @unchecked Sendable {
    typealias Handler = @Sendable (FakeDaemon, FakeConnection) -> Void

    let directory: String
    let path: String
    private let lock = NSLock()
    private var listenFD: Int32 = -1
    private var stopped = true
    private var connections: [Int32] = []
    private var subscribers: [FakeConnection] = []
    private var acceptedCount = 0
    private var requestLog: [[String: Any]] = []
    private let handler: Handler

    init(handler: @escaping Handler) {
        var template = Array("/tmp/gas-a4-XXXXXX".utf8CString)
        let dir = template.withUnsafeMutableBufferPointer { buf -> String in
            guard let p = mkdtemp(buf.baseAddress) else { return "/tmp/gas-a4-fallback-\(getpid())" }
            return String(cString: p)
        }
        directory = dir
        path = dir + "/d.sock"
        self.handler = handler
        TempDirectoryJanitor.shared.track(dir)
    }

    deinit {
        stop()
        try? FileManager.default.removeItem(atPath: directory)
    }

    var accepted: Int { lock.withLock { acceptedCount } }
    var requests: [[String: Any]] { lock.withLock { requestLog } }

    func requests(_ cmd: String) -> [[String: Any]] {
        requests.filter { $0["cmd"] as? String == cmd }
    }

    func record(_ request: [String: Any]) {
        lock.withLock { requestLog.append(request) }
    }

    func addSubscriber(_ connection: FakeConnection) {
        lock.withLock { subscribers.append(connection) }
    }

    func push(_ frame: Any) {
        let targets = lock.withLock { subscribers }
        for connection in targets {
            connection.send(frame)
        }
    }

    func start() {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        precondition(fd >= 0)
        unlink(path)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = path.utf8CString
        withUnsafeMutablePointer(to: &addr.sun_path) { sunPath in
            sunPath.withMemoryRebound(to: CChar.self, capacity: bytes.count) { dest in
                for (i, b) in bytes.enumerated() { dest[i] = b }
            }
        }
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        precondition(bound == 0, "bind failed: \(errno)")
        precondition(listen(fd, 16) == 0)
        lock.withLock {
            listenFD = fd
            stopped = false
        }
        let thread = Thread { [self] in acceptLoop(fd) }
        thread.start()
    }

    func stop() {
        let (fd, open) = lock.withLock { () -> (Int32, [Int32]) in
            let result = (listenFD, connections)
            stopped = true
            listenFD = -1
            connections = []
            subscribers = []
            return result
        }
        for c in open {
            shutdown(c, SHUT_RDWR)
        }
        if fd >= 0 {
            close(fd)
        }
        unlink(path)
    }

    private var isStopped: Bool { lock.withLock { stopped } }

    private func acceptLoop(_ fd: Int32) {
        while !isStopped {
            var pfd = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            let ready = poll(&pfd, 1, 50)
            if ready <= 0 { continue }
            let client = accept(fd, nil, nil)
            if client < 0 { continue }
            var noSigPipe: Int32 = 1
            setsockopt(client, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))
            let accepted = lock.withLock { () -> Bool in
                guard !stopped else { return false }
                acceptedCount += 1
                connections.append(client)
                return true
            }
            guard accepted else {
                close(client)
                return
            }
            let connection = FakeConnection(fd: client) { [weak self] in self?.isStopped ?? true }
            let thread = Thread { [self] in
                handler(self, connection)
                lock.withLock {
                    connections.removeAll { $0 == client }
                    subscribers.removeAll { $0 === connection }
                }
                close(client)
            }
            thread.start()
        }
    }
}

struct DaemonScript: @unchecked Sendable {
    var protocolVersion = 2
    var repos: [[String: Any]] = []
    var events: [[String: Any]] = []
    var lastEventId: UInt64 = 0
    var silentCommands: Set<String> = []
    var dropOnceCommands: Set<String> = []
}

final class ScriptedDaemon: @unchecked Sendable {
    private let lock = NSLock()
    private var script: DaemonScript
    private var dropped: Set<String> = []
    private(set) var daemon: FakeDaemon!

    init(_ script: DaemonScript) {
        self.script = script
        daemon = FakeDaemon { [unowned self] daemon, connection in
            self.serve(daemon, connection)
        }
    }

    var path: String { daemon.path }

    func update(_ change: (inout DaemonScript) -> Void) {
        lock.withLock { change(&script) }
    }

    private var current: DaemonScript { lock.withLock { script } }

    private func serve(_ daemon: FakeDaemon, _ connection: FakeConnection) {
        while let request = connection.readRequest() {
            daemon.record(request)
            let id = request["id"] as? String ?? ""
            let cmd = request["cmd"] as? String ?? ""
            let script = current

            let drop = lock.withLock { () -> Bool in
                guard script.dropOnceCommands.contains(cmd), !dropped.contains(cmd) else { return false }
                dropped.insert(cmd)
                return true
            }
            if drop { return }
            if script.silentCommands.contains(cmd) { continue }

            switch cmd {
            case "subscribe":
                connection.send(["id": id, "ok": true, "result": ["protocol": script.protocolVersion, "version": "fake"]])
                connection.send([
                    "type": "hello",
                    "protocol": script.protocolVersion,
                    "version": "fake",
                    "repos": script.repos,
                    "lastEventId": script.lastEventId,
                ])
                daemon.addSubscriber(connection)
            case "status":
                connection.send(["id": id, "ok": true, "result": ["daemon": "running", "protocol": script.protocolVersion, "repos": script.repos, "lastEventId": script.lastEventId]])
            case "events":
                let since = (request["since"] as? NSNumber)?.uint64Value ?? 0
                let limit = (request["limit"] as? Int) ?? 0
                var matching = script.events.filter { (($0["id"] as? NSNumber)?.uint64Value ?? 0) > since }
                if limit > 0, matching.count > limit { matching = Array(matching.suffix(limit)) }
                connection.send(["id": id, "ok": true, "result": ["events": matching, "lastEventId": script.lastEventId]])
            default:
                connection.send(["id": id, "ok": true, "result": ["status": "ok", "repo": request["repo"] ?? ""]])
            }
        }
    }
}

func repoJSON(_ path: String, status: String, code: String? = nil, extra: [String: Any] = [:]) -> [String: Any] {
    var repo: [String: Any] = [
        "path": path, "status": status, "ahead": 0, "behind": 0,
        "paused": status == "paused", "conflict": status == "conflict",
        "watching": true, "failures": 0,
    ]
    if let code { repo["errorCode"] = code }
    for (k, v) in extra { repo[k] = v }
    return repo
}

func eventJSON(_ id: UInt64, repo: String, type: String = "commit", ts: Int64 = 1_792_697_000) -> [String: Any] {
    ["id": id, "type": type, "repo": repo, "message": "event \(id)", "ts": ts + Int64(id)]
}

@MainActor
final class RecordingNotifier: RepoNotifying {
    struct Delivery: Equatable {
        let repo: String
        let body: String
    }

    var delivered: [Delivery] = []
    var cleared: [String] = []

    func notify(repo: String, body: String) {
        delivered.append(Delivery(repo: repo, body: body))
    }

    func clear(repo: String) {
        cleared.append(repo)
    }
}

@MainActor
func eventually(timeout: TimeInterval = 5, _ condition: () -> Bool) async -> Bool {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if condition() { return true }
        try? await Task.sleep(for: .milliseconds(20))
    }
    return condition()
}

final class MemoryDefaults: KeyValueStoring {
    private var values: [String: Any] = [:]

    func object(forKey defaultName: String) -> Any? {
        values[defaultName]
    }

    func set(_ value: Any?, forKey defaultName: String) {
        values[defaultName] = value
    }

    func removeObject(forKey defaultName: String) {
        values[defaultName] = nil
    }
}

@MainActor
final class TestEnvironment {
    let defaults = MemoryDefaults()
    let cacheDirectory: URL

    init() {
        cacheDirectory = FileManager.default.temporaryDirectory.appendingPathComponent("gas-a4-cache-\(UUID().uuidString)")
    }

    func eventStore(socketPath: String) -> EventStore {
        EventStore(socketPath: socketPath, defaults: defaults, directory: cacheDirectory, saveDelay: .milliseconds(50))
    }

    func makeState(socketPath: String, notifier: RecordingNotifier? = nil, launcher: DaemonLauncher = .disabled("tests never start a daemon"), requestTimeout: TimeInterval = 2) -> AppState {
        AppState(
            client: DaemonClient(socketPath: socketPath, requestTimeout: requestTimeout),
            notifier: notifier ?? RecordingNotifier(),
            launcher: launcher,
            eventStore: eventStore(socketPath: socketPath)
        )
    }

    func cleanUp() {
        try? FileManager.default.removeItem(at: cacheDirectory)
    }
}

final class TempDirectoryJanitor: NSObject, XCTestObservation, @unchecked Sendable {
    static let shared = TempDirectoryJanitor()

    private let lock = NSLock()
    private var paths: [String] = []
    private var registered = false

    func track(_ path: String) {
        let register = lock.withLock { () -> Bool in
            paths.append(path)
            defer { registered = true }
            return !registered
        }
        if register {
            XCTestObservationCenter.shared.addTestObserver(self)
        }
    }

    func testBundleDidFinish(_ testBundle: Bundle) {
        let all = lock.withLock { paths }
        for path in all {
            try? FileManager.default.removeItem(atPath: path)
        }
    }
}
