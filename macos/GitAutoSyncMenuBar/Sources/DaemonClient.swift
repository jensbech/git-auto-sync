import Foundation

enum ConnectionState {
    case disconnected
    case connecting
    case connected
}

final class DaemonClient: @unchecked Sendable {
    private let socketPath: String
    private var fileDescriptor: Int32 = -1
    private var readTask: Task<Void, Never>?
    private var reconnectTask: Task<Void, Never>?
    private var eventContinuation: AsyncStream<DaemonEvent>.Continuation?

    private(set) var connectionState: ConnectionState = .disconnected

    lazy var events: AsyncStream<DaemonEvent> = {
        AsyncStream { [weak self] continuation in
            self?.eventContinuation = continuation
        }
    }()

    init(socketPath: String? = nil) {
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        self.socketPath = socketPath ?? "\(home)/.local/share/git-auto-sync/daemon.sock"
    }

    func connect() {
        guard connectionState == .disconnected else { return }
        connectionState = .connecting

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
            let bufferSize = 4096
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

                    if let event = try? JSONDecoder().decode(DaemonEvent.self, from: Data(lineData)) {
                        self?.eventContinuation?.yield(event)
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
            try? await Task.sleep(nanoseconds: 5_000_000_000)
            if !Task.isCancelled {
                self?.connect()
            }
        }
    }

    func sendCommandOneShot(_ command: SocketCommand) async -> StatusResponse? {
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

        var buffer = [UInt8](repeating: 0, count: 8192)
        let bytesRead = read(fd, &buffer, buffer.count)
        guard bytesRead > 0 else { return nil }

        let responseData = Data(buffer[0..<bytesRead])
        return try? JSONDecoder().decode(StatusResponse.self, from: responseData)
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
