import Foundation
import NIOCore
import NIOPosix
import NIOSSH
import USHCore

/// One SSH connection to a rendezvous. `list` and every joined session run as
/// channels over it.
public final class RendezvousConnection: @unchecked Sendable {
    let channel: Channel

    init(channel: Channel) { self.channel = channel }

    public static func connect(_ profile: Profile) async throws -> RendezvousConnection {
        try await connect(profile, keys: Keys(seed: parseSeed(profile.seed)))
    }

    static func connect(_ profile: Profile, keys: Keys,
                        group: EventLoopGroup = MultiThreadedEventLoopGroup.singleton) async throws -> RendezvousConnection {
        let loop = group.next()
        let ready = loop.makePromise(of: Void.self)
        let login = LoginKey(user: profile.user, key: NIOSSHPrivateKey(ed25519Key: keys.login))
        let host = PinnedHostKey(try NIOSSHPublicKey(openSSHPublicKey: authorizedKeyLine(keys.hostPublicKey)))
        let handshake = Handshake(ready: ready, login: login, host: host)
        let bootstrap = ClientBootstrap(group: loop)
            .connectTimeout(.seconds(10))
            // A peer that vanishes without a FIN or RST (power loss, a network
            // change) is otherwise only noticed when TCP gives up, minutes later.
            .channelOption(ChannelOptions.socketOption(.so_keepalive), value: 1)
            .channelOption(ChannelOptions.Types.SocketOption(level: IPPROTO_TCP, name: TCP_KEEPALIVE), value: 15)
            .channelOption(ChannelOptions.Types.SocketOption(level: IPPROTO_TCP, name: TCP_KEEPINTVL), value: 5)
            .channelOption(ChannelOptions.Types.SocketOption(level: IPPROTO_TCP, name: TCP_KEEPCNT), value: 3)
            .channelInitializer { ch in
                ch.eventLoop.makeCompletedFuture {
                    try ch.pipeline.syncOperations.addHandlers([
                        NIOSSHHandler(role: .client(.init(userAuthDelegate: login, serverAuthDelegate: host)),
                                      allocator: ch.allocator, inboundChildChannelInitializer: nil),
                        handshake,
                    ])
                }
            }
        let channel: Channel
        do {
            channel = try await bootstrap.connect(host: profile.host, port: profile.port).get()
        } catch {
            ready.fail(error)
            throw ClientError.unreachable(String(describing: error))
        }
        // A server that accepts the TCP connection but never finishes the SSH
        // handshake must not hang the caller.
        let limit = loop.scheduleTask(in: .seconds(10)) {
            handshake.abort(ClientError.unreachable("the SSH handshake timed out"))
        }
        defer { limit.cancel() }
        do {
            try await ready.futureResult.get()
        } catch {
            channel.close(promise: nil)
            throw error
        }
        return RendezvousConnection(channel: channel)
    }

    public var isActive: Bool { channel.isActive }

    public func onClose(_ body: @escaping @Sendable () -> Void) {
        channel.closeFuture.whenComplete { _ in body() }
    }

    public func close() { channel.close(promise: nil) }

    public func list(timeout: TimeAmount = .seconds(10)) async throws -> [SessionInfo] {
        try await withDeadline(timeout) {
            let out = Collected(), err = Collected()
            let (_, finished) = try await self.exec("list", pty: nil,
                                                    onStdout: { out.append($0) }, onStderr: { err.append($0) })
            let code = try await finished.get()
            guard code == 0 else {
                throw ClientError.commandFailed(code: code, message: err.text.trimmingCharacters(in: .whitespacesAndNewlines))
            }
            return try decodeSessions(Data(out.bytes))
        }
    }

    public func join(sessionID: String, cols: Int, rows: Int,
                     onOutput: @escaping @Sendable ([UInt8]) -> Void) async throws -> JoinChannel {
        let pty = SSHChannelRequestEvent.PseudoTerminalRequest(
            wantReply: false, term: "xterm-256color",
            terminalCharacterWidth: cols, terminalRowHeight: rows,
            terminalPixelWidth: 0, terminalPixelHeight: 0,
            terminalModes: SSHTerminalModes([:]))
        // Only opening the channel has a deadline; the joined session then
        // lives as long as the user keeps it.
        let (child, finished) = try await withDeadline(.seconds(10)) {
            try await self.exec("join \(sessionID)", pty: pty, onStdout: onOutput, onStderr: onOutput)
        }
        return JoinChannel(channel: child, finished: finished)
    }

    private func exec(_ command: String, pty: SSHChannelRequestEvent.PseudoTerminalRequest?,
                      onStdout: @escaping @Sendable ([UInt8]) -> Void,
                      onStderr: @escaping @Sendable ([UInt8]) -> Void) async throws -> (Channel, EventLoopFuture<Int>) {
        let loop = channel.eventLoop
        let finished = loop.makePromise(of: Int.self)
        let opened = channel.pipeline.handler(type: NIOSSHHandler.self).flatMap { ssh in
            let created = loop.makePromise(of: Channel.self)
            ssh.createChannel(created, channelType: .session) { child, _ in
                child.pipeline.addHandler(ExecHandler(command: command, pty: pty, finished: finished,
                                                      onStdout: onStdout, onStderr: onStderr))
            }
            return created.futureResult
        }
        // If the channel never opened, the handler that completes `finished`
        // was never added.
        opened.whenFailure { finished.fail($0) }
        return (try await opened.get(), finished.futureResult)
    }
}

extension RendezvousConnection {
    /// Runs `body`, and if it has not finished within `timeout`, closes the
    /// whole connection and throws `timedOut`. A connection that stopped
    /// answering one request is not worth keeping: closing it makes
    /// `isActive` false, so the next use reconnects.
    func withDeadline<T>(_ timeout: TimeAmount, _ body: () async throws -> T) async throws -> T {
        let expired = Flag()
        let deadline = channel.eventLoop.scheduleTask(in: timeout) { [channel] in
            expired.set()
            channel.close(promise: nil)
        }
        defer { deadline.cancel() }
        do {
            return try await body()
        } catch {
            throw expired.isSet ? ClientError.timedOut : error
        }
    }
}

private final class Flag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    func set() { lock.withLock { value = true } }
    var isSet: Bool { lock.withLock { value } }
}

private final class Collected: @unchecked Sendable {
    private let lock = NSLock()
    private var buffer: [UInt8] = []
    func append(_ b: [UInt8]) { lock.withLock { buffer += b } }
    var bytes: [UInt8] { lock.withLock { buffer } }
    var text: String { String(decoding: bytes, as: UTF8.self) }
}
