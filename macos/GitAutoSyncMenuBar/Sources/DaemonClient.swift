import Foundation
import Darwin

final class DaemonClient: @unchecked Sendable {
    private let socketPath: String
    private var fileDescriptor: Int32 = -1
    private var readTask: Task<Void, Never>?
    private var reconnectTask: Task<Void, Never>?
    private var messageContinuation: AsyncStream<DaemonMessage>.Continuation?

    private(set) var connectionState: ConnectionState = .disconnected

    lazy var messages: AsyncStream<DaemonMessage> = {
        AsyncStream { [weak self] continuation in
            self?.messageContinuation = continuation
        }
    }()

    init(socketPath: String? = nil) {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        self.socketPath = socketPath ?? "\(home)/.local/share/git-auto-sync/daemon.sock"
    }

    func connect() {
        guard connectionState == .disconnected else { return }
        connectionState = .connecting

        // Materialize lazy stream so the continuation is registered before reads start.
        _ = self.messages

        let fd = Self.connectSocket(socketPath)
        guard fd >= 0 else {
            connectionState = .disconnected
            scheduleReconnect()
            return
        }

        fileDescriptor = fd
        connectionState = .connected
        startReading(fd: fd)
    }

    private func startReading(fd: Int32) {
        readTask = Task.detached { [weak self] in
            let bufferSize = 8192
            var lineBuffer = Data()
            var buffer = [UInt8](repeating: 0, count: bufferSize)

            while !Task.isCancelled {
                let bytesRead = read(fd, &buffer, bufferSize)
                if bytesRead <= 0 {
                    break
                }

                lineBuffer.append(contentsOf: buffer[0..<bytesRead])

                while let newlineIndex = lineBuffer.firstIndex(of: UInt8(ascii: "\n")) {
                    let lineData = lineBuffer[lineBuffer.startIndex..<newlineIndex]
                    lineBuffer = Data(lineBuffer[lineBuffer.index(after: newlineIndex)...])

                    if let msg = Self.decodeMessage(Data(lineData)) {
                        self?.messageContinuation?.yield(msg)
                    }
                }
            }

            close(fd)
            self?.connectionState = .disconnected
            self?.fileDescriptor = -1
            if !Task.isCancelled {
                self?.scheduleReconnect()
            }
        }
    }

    static func decodeMessage(_ data: Data) -> DaemonMessage? {
        guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String else {
            if let s = String(data: data, encoding: .utf8), !s.isEmpty {
                NSLog("git-auto-sync: unparseable message: %@", s)
            }
            return nil
        }

        let decoder = JSONDecoder()
        switch type {
        case "hello":
            struct Hello: Codable { let repos: [RepoState] }
            if let h = try? decoder.decode(Hello.self, from: data) {
                return .hello(h.repos)
            }
        case "state":
            struct StateMsg: Codable { let data: RepoState }
            if let s = try? decoder.decode(StateMsg.self, from: data) {
                return .state(s.data)
            }
        default:
            if let e = try? decoder.decode(DaemonEvent.self, from: data) {
                return .event(e)
            }
        }
        NSLog("git-auto-sync: failed to decode message type=%@", type)
        return nil
    }

    func disconnect() {
        readTask?.cancel()
        readTask = nil
        reconnectTask?.cancel()
        reconnectTask = nil
        if fileDescriptor >= 0 {
            close(fileDescriptor)
            fileDescriptor = -1
        }
        connectionState = .disconnected
    }

    private func scheduleReconnect() {
        reconnectTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 3_000_000_000)
            if !Task.isCancelled {
                self?.connect()
            }
        }
    }

    @discardableResult
    func sendCommand(_ command: SocketCommand) async -> Data? {
        let fd = Self.connectSocket(socketPath)
        guard fd >= 0 else { return nil }
        defer { close(fd) }

        guard let data = try? JSONEncoder().encode(command) else { return nil }
        var payload = data
        payload.append(UInt8(ascii: "\n"))

        let written = payload.withUnsafeBytes { ptr in
            write(fd, ptr.baseAddress!, payload.count)
        }
        guard written > 0 else { return nil }

        var tv = timeval(tv_sec: 5, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var collected = Data()
        var buffer = [UInt8](repeating: 0, count: 8192)
        // Read until we've seen the response newline (skip a possible welcome 'hello' frame first).
        var responses: [Data] = []
        while responses.count < 2 {
            let bytesRead = read(fd, &buffer, buffer.count)
            if bytesRead <= 0 { break }
            collected.append(contentsOf: buffer[0..<bytesRead])
            while let nl = collected.firstIndex(of: UInt8(ascii: "\n")) {
                let line = Data(collected[collected.startIndex..<nl])
                collected = Data(collected[collected.index(after: nl)...])
                responses.append(line)
            }
        }

        // Return the first response that isn't a hello frame.
        for line in responses {
            if let json = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
               (json["type"] as? String) != "hello" {
                return line
            }
        }
        return responses.first
    }

    func sendStatusCommand() async -> StatusResponse? {
        guard let data = await sendCommand(SocketCommand(cmd: "status")) else { return nil }
        return try? JSONDecoder().decode(StatusResponse.self, from: data)
    }

    private static func connectSocket(_ path: String) -> Int32 {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return -1 }

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
