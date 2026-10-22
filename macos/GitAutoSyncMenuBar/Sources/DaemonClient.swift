import Foundation
import Darwin
import os

enum DaemonRequestError: Error, Equatable, Sendable {
    case unreachable
    case timeout
    case incompatible
    case io(String)
}

final class DaemonClient: Sendable {
    let socketPath: String
    let messages: AsyncStream<DaemonMessage>

    private let continuation: AsyncStream<DaemonMessage>.Continuation
    private let state = OSAllocatedUnfairLock(initialState: SubscriberState())
    private let wake = DispatchSemaphore(value: 0)
    private let requestTimeout: TimeInterval

    private static let requestQueue = DispatchQueue(
        label: "git-auto-sync.requests",
        qos: .userInitiated,
        attributes: .concurrent
    )
    private static let log = Logger(subsystem: "com.gitautosync.menubar", category: "daemon-client")

    private struct SubscriberState {
        var started = false
        var stopped = false
        var subscriberFD: Int32 = -1
        var nextID: UInt64 = 0
    }

    static let stableSession: TimeInterval = 10
    static let maxRetryDelay: TimeInterval = 10

    private enum SessionOutcome {
        case closed(established: Bool)
        case incompatible(Int)
    }

    enum Frame {
        case response(DaemonResponse)
        case hello(DaemonHello)
        case state(RepoState)
        case event(DaemonEvent)
        case ignored
    }

    private struct StateFrame: Decodable {
        let data: RepoState
    }

    static var defaultSocketPath: String {
        socketOverride ?? standardSocketPath
    }

    static var standardSocketPath: String {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        return "\(home)/.local/share/git-auto-sync/daemon.sock"
    }

    static var socketOverride: String? {
        guard let override = ProcessInfo.processInfo.environment["GIT_AUTO_SYNC_SOCKET"], !override.isEmpty else {
            return nil
        }
        return override
    }

    init(socketPath: String? = nil, requestTimeout: TimeInterval = 15) {
        self.socketPath = socketPath ?? Self.defaultSocketPath
        self.requestTimeout = requestTimeout
        let (stream, continuation) = AsyncStream.makeStream(of: DaemonMessage.self, bufferingPolicy: .unbounded)
        self.messages = stream
        self.continuation = continuation
    }

    func start() {
        let shouldStart = state.withLock { s -> Bool in
            guard !s.started, !s.stopped else { return false }
            s.started = true
            return true
        }
        guard shouldStart else { return }
        let thread = Thread { [self] in
            runSubscriber()
        }
        thread.name = "git-auto-sync.subscriber"
        thread.qualityOfService = .utility
        thread.start()
    }

    func stop() {
        let fd = state.withLock { s -> Int32 in
            s.stopped = true
            return s.subscriberFD
        }
        if fd >= 0 {
            shutdown(fd, SHUT_RDWR)
        }
        wake.signal()
    }

    func reconnectNow() {
        wake.signal()
    }

    func request(_ command: SocketCommand, timeout: TimeInterval? = nil) async -> Result<DaemonResponse, DaemonRequestError> {
        var cmd = command
        let id = makeID("req")
        cmd.id = id
        let path = socketPath
        let limit = timeout ?? requestTimeout
        return await withCheckedContinuation { cont in
            Self.requestQueue.async {
                cont.resume(returning: Self.perform(cmd, id: id, path: path, timeout: limit))
            }
        }
    }

    private var isStopped: Bool {
        state.withLock { $0.stopped }
    }

    private func makeID(_ prefix: String) -> String {
        let n = state.withLock { s -> UInt64 in
            s.nextID += 1
            return s.nextID
        }
        return "\(prefix)-\(n)"
    }

    private func pause(_ seconds: TimeInterval) {
        _ = wake.wait(timeout: .now() + seconds)
        while wake.wait(timeout: .now()) == .success {}
    }

    static func reconnectDelay(quickDrops: Int) -> TimeInterval {
        guard quickDrops > 0 else { return 0.5 }
        return min(0.5 * pow(2, Double(min(quickDrops, 8))), maxRetryDelay)
    }

    private func runSubscriber() {
        var retryDelay: TimeInterval = 1
        var quickDrops = 0
        while !isStopped {
            let fd = Self.connectSocket(socketPath)
            if fd < 0 {
                continuation.yield(.unavailable)
                pause(retryDelay)
                retryDelay = min(retryDelay * 2, Self.maxRetryDelay)
                continue
            }
            retryDelay = 1

            let registered = state.withLock { s -> Bool in
                guard !s.stopped else { return false }
                s.subscriberFD = fd
                return true
            }
            guard registered else {
                close(fd)
                break
            }

            let started = Date()
            let outcome = runSession(fd: fd)
            state.withLock { $0.subscriberFD = -1 }
            close(fd)
            if isStopped { break }

            switch outcome {
            case .incompatible(let version):
                continuation.yield(.incompatible(daemonProtocol: version))
                pause(Self.maxRetryDelay)
            case .closed(let established):
                continuation.yield(.disconnected)
                if established && Date().timeIntervalSince(started) >= Self.stableSession {
                    quickDrops = 0
                } else {
                    quickDrops += 1
                }
                pause(Self.reconnectDelay(quickDrops: quickDrops))
            }
        }
        continuation.finish()
    }

    private func runSession(fd: Int32) -> SessionOutcome {
        Self.setReceiveTimeout(fd, seconds: 5)
        let subscribe = SocketCommand(cmd: DaemonCommand.subscribe, id: makeID("sub"))
        guard Self.writeLine(fd, subscribe) else { return .closed(established: false) }

        var reader = LineReader(fd: fd)
        var established = false
        while !isStopped, let line = reader.next() {
            switch Self.parseFrame(line) {
            case .response(let response):
                guard !response.ok else { continue }
                if response.code == "unknown_command" || (response.error?.lowercased().contains("unknown command") ?? false) {
                    return .incompatible(1)
                }
                Self.log.error("subscribe failed: \(response.error ?? "unknown error", privacy: .public)")
                return .closed(established: established)
            case .hello(let hello):
                let version = hello.protocolVersion ?? 1
                guard version == DaemonCommand.protocolVersion else { return .incompatible(version) }
                if !established {
                    established = true
                    Self.setReceiveTimeout(fd, seconds: 0)
                }
                continuation.yield(.hello(hello))
            case .state(let repoState):
                if established { continuation.yield(.state(repoState)) }
            case .event(let event):
                if established { continuation.yield(.event(event)) }
            case .ignored:
                continue
            }
        }
        return .closed(established: established)
    }

    static func parseFrame(_ line: Data) -> Frame {
        guard let json = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else {
            if let text = String(data: line, encoding: .utf8), !text.isEmpty {
                log.error("unparseable frame: \(text, privacy: .public)")
            }
            return .ignored
        }
        guard json["ok"] == nil, let type = json["type"] as? String else {
            return DaemonResponse.decode(line).map(Frame.response) ?? .ignored
        }

        let decoder = JSONDecoder()
        switch type {
        case "hello":
            if let hello = try? decoder.decode(DaemonHello.self, from: line) { return .hello(hello) }
        case "state":
            if let frame = try? decoder.decode(StateFrame.self, from: line) { return .state(frame.data) }
        default:
            if let event = try? decoder.decode(DaemonEvent.self, from: line) { return .event(event) }
        }
        log.error("failed to decode frame type=\(type, privacy: .public)")
        return .ignored
    }

    private static func perform(_ command: SocketCommand, id: String, path: String, timeout: TimeInterval) -> Result<DaemonResponse, DaemonRequestError> {
        let fd = connectSocket(path)
        guard fd >= 0 else { return .failure(.unreachable) }
        defer { close(fd) }

        setReceiveTimeout(fd, seconds: timeout)
        guard writeLine(fd, command) else { return .failure(.io("Could not send the command to the daemon")) }

        var reader = LineReader(fd: fd)
        while let line = reader.next() {
            switch classifyReply(line, id: id) {
            case .skip:
                continue
            case .legacy:
                return .failure(.incompatible)
            case .response(let response):
                return .success(response)
            }
        }
        return .failure(reader.timedOut ? .timeout : .io("The daemon closed the connection before answering. It may have restarted; try again."))
    }

    enum Reply {
        case skip
        case legacy
        case response(DaemonResponse)
    }

    static func classifyReply(_ line: Data, id: String) -> Reply {
        guard let json = try? JSONSerialization.jsonObject(with: line) as? [String: Any] else { return .skip }
        if json["ok"] == nil, json["type"] as? String == "hello" { return .skip }
        let replyID = json["id"] as? String
        let code = (json["code"] as? String).flatMap { $0.isEmpty ? nil : $0 }
        if replyID == nil, code == nil { return .legacy }
        guard let response = DaemonResponse.decode(line), replyID == nil || replyID == id else { return .skip }
        return .response(response)
    }

    private static func writeLine(_ fd: Int32, _ command: SocketCommand) -> Bool {
        guard var payload = try? JSONEncoder().encode(command) else { return false }
        payload.append(UInt8(ascii: "\n"))
        return payload.withUnsafeBytes { raw -> Bool in
            guard let base = raw.baseAddress else { return false }
            var offset = 0
            while offset < raw.count {
                let n = write(fd, base + offset, raw.count - offset)
                if n < 0 {
                    if errno == EINTR { continue }
                    return false
                }
                offset += n
            }
            return true
        }
    }

    private static func setReceiveTimeout(_ fd: Int32, seconds: TimeInterval) {
        let whole = Int(seconds)
        let micro = Int32((seconds - Double(whole)) * 1_000_000)
        var tv = timeval(tv_sec: whole, tv_usec: micro)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
    }

    private static func connectSocket(_ path: String) -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return -1 }
        _ = fcntl(fd, F_SETFD, FD_CLOEXEC)
        var noSigPipe: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)

        let pathBytes = path.utf8CString
        let maxLen = MemoryLayout.size(ofValue: addr.sun_path)
        guard pathBytes.count <= maxLen else {
            close(fd)
            return -1
        }

        withUnsafeMutablePointer(to: &addr.sun_path) { sunPath in
            sunPath.withMemoryRebound(to: CChar.self, capacity: maxLen) { dest in
                for (i, byte) in pathBytes.enumerated() {
                    dest[i] = byte
                }
            }
        }

        let result = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.connect(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }

        guard result == 0 else {
            close(fd)
            return -1
        }

        return fd
    }
}

private struct LineReader {
    let fd: Int32
    private(set) var timedOut = false
    private var pending = Data()
    private var buffer = [UInt8](repeating: 0, count: 64 * 1024)

    init(fd: Int32) {
        self.fd = fd
    }

    mutating func next() -> Data? {
        while true {
            if let newline = pending.firstIndex(of: UInt8(ascii: "\n")) {
                let line = Data(pending[pending.startIndex..<newline])
                pending.removeSubrange(pending.startIndex...newline)
                if line.isEmpty { continue }
                return line
            }
            let count = buffer.withUnsafeMutableBytes { raw in
                read(fd, raw.baseAddress, raw.count)
            }
            if count > 0 {
                pending.append(contentsOf: buffer[0..<count])
                continue
            }
            if count < 0 {
                if errno == EINTR { continue }
                if errno == EAGAIN || errno == EWOULDBLOCK { timedOut = true }
            }
            return nil
        }
    }
}
