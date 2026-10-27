import NIOCore
import NIOSSH

/// Runs one command on a session channel, optionally on a pty, and reports its
/// output and exit status.
final class ExecHandler: ChannelInboundHandler, @unchecked Sendable {
    typealias InboundIn = SSHChannelData

    private let command: String
    private let pty: SSHChannelRequestEvent.PseudoTerminalRequest?
    private let onStdout: @Sendable ([UInt8]) -> Void
    private let onStderr: @Sendable ([UInt8]) -> Void
    private let finished: EventLoopPromise<Int>
    private var status: Int?
    private var done = false

    init(command: String, pty: SSHChannelRequestEvent.PseudoTerminalRequest?,
         finished: EventLoopPromise<Int>,
         onStdout: @escaping @Sendable ([UInt8]) -> Void,
         onStderr: @escaping @Sendable ([UInt8]) -> Void) {
        self.command = command
        self.pty = pty
        self.finished = finished
        self.onStdout = onStdout
        self.onStderr = onStderr
    }

    private func finish(_ result: Result<Int, Error>) {
        guard !done else { return }
        done = true
        finished.completeWith(result)
    }

    func handlerAdded(context: ChannelHandlerContext) {
        // Keep reading after the far side's EOF, until the channel closes and
        // the exit status has arrived.
        _ = context.channel.setOption(ChannelOptions.allowRemoteHalfClosure, value: true)
    }

    func channelActive(context: ChannelHandlerContext) {
        if let pty { context.triggerUserOutboundEvent(pty, promise: nil) }
        context.triggerUserOutboundEvent(
            SSHChannelRequestEvent.ExecRequest(command: command, wantReply: true), promise: nil)
        context.fireChannelActive()
    }

    func channelRead(context: ChannelHandlerContext, data: NIOAny) {
        let message = unwrapInboundIn(data)
        guard case .byteBuffer(let buffer) = message.data else { return }
        let bytes = Array(buffer.readableBytesView)
        switch message.type {
        case .channel: onStdout(bytes)
        case .stdErr: onStderr(bytes)
        default: break
        }
    }

    func userInboundEventTriggered(context: ChannelHandlerContext, event: Any) {
        switch event {
        case let s as SSHChannelRequestEvent.ExitStatus:
            status = s.exitStatus
        case is ChannelFailureEvent:
            finish(.failure(ClientError.channelRefused))
            context.close(promise: nil)
        default:
            context.fireUserInboundEventTriggered(event)
        }
    }

    func errorCaught(context: ChannelHandlerContext, error: Error) {
        finish(.failure(error))
        context.close(promise: nil)
    }

    func channelInactive(context: ChannelHandlerContext) {
        finish(status.map { .success($0) } ?? .failure(ClientError.disconnected))
        context.fireChannelInactive()
    }
}
