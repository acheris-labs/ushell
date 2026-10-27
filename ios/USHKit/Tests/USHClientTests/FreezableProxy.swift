import Foundation
import NIOCore
import NIOPosix

/// A TCP relay to a local port that can stop relaying without closing either
/// side: what a rendezvous that lost power, or a network that changed under
/// the phone, looks like to the client.
final class FreezableProxy: @unchecked Sendable {
    private let lock = NSLock()
    private var frozenValue = false
    private let server: Channel
    let port: Int

    var frozen: Bool { lock.withLock { frozenValue } }
    func freeze() { lock.withLock { frozenValue = true } }

    init(target: Int) async throws {
        let group = MultiThreadedEventLoopGroup.singleton
        var proxy: FreezableProxy?
        let box = { proxy }
        server = try await ServerBootstrap(group: group)
            .childChannelOption(ChannelOptions.autoRead, value: false)
            .childChannelInitializer { inbound in
                ClientBootstrap(group: inbound.eventLoop)
                    .connect(host: "127.0.0.1", port: target)
                    .flatMap { outbound in
                        let frozen = { box()?.frozen ?? false }
                        return inbound.pipeline.addHandler(Pump(peer: outbound, frozen: frozen))
                            .flatMap { outbound.pipeline.addHandler(Pump(peer: inbound, frozen: frozen)) }
                            .flatMap { inbound.setOption(ChannelOptions.autoRead, value: true) }
                    }
            }
            .bind(host: "127.0.0.1", port: 0).get()
        port = server.localAddress!.port!
        proxy = self
    }

    deinit { server.close(promise: nil) }
}

private final class Pump: ChannelInboundHandler, @unchecked Sendable {
    typealias InboundIn = ByteBuffer
    private let peer: Channel
    private let frozen: () -> Bool

    init(peer: Channel, frozen: @escaping () -> Bool) {
        self.peer = peer
        self.frozen = frozen
    }

    func channelRead(context: ChannelHandlerContext, data: NIOAny) {
        if !frozen() { peer.writeAndFlush(data, promise: nil) }
    }

    func channelInactive(context: ChannelHandlerContext) {
        peer.close(promise: nil)
        context.fireChannelInactive()
    }
}
