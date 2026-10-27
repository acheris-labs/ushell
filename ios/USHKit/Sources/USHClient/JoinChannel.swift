import NIOCore
import NIOSSH

/// A joined session, as the app drives it.
public protocol JoinSession: AnyObject, Sendable {
    func send(_ bytes: [UInt8])
    func resize(cols: Int, rows: Int)
    /// The exit status of `join`; throws `ClientError.disconnected` if the
    /// channel closed without one.
    func exitCode() async throws -> Int
    func close()
}

/// One joined session: a pty channel running `join <id>` on the rendezvous.
public final class JoinChannel: JoinSession, @unchecked Sendable {
    let channel: Channel
    let finished: EventLoopFuture<Int>

    init(channel: Channel, finished: EventLoopFuture<Int>) {
        self.channel = channel
        self.finished = finished
    }

    public func send(_ bytes: [UInt8]) {
        let buffer = channel.allocator.buffer(bytes: bytes)
        channel.writeAndFlush(SSHChannelData(type: .channel, data: .byteBuffer(buffer)), promise: nil)
    }

    public func resize(cols: Int, rows: Int) {
        channel.triggerUserOutboundEvent(SSHChannelRequestEvent.WindowChangeRequest(
            terminalCharacterWidth: cols, terminalRowHeight: rows,
            terminalPixelWidth: 0, terminalPixelHeight: 0), promise: nil)
    }

    /// The exit status of `join`; throws `ClientError.disconnected` if the
    /// channel closed without one (the connection dropped).
    public func exitCode() async throws -> Int { try await finished.get() }

    public func close() { channel.close(promise: nil) }
}
