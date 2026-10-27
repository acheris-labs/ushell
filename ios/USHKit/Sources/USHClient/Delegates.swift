import NIOCore
import NIOSSH

/// Offers the one login key, once. A second call means it was refused.
final class LoginKey: NIOSSHClientUserAuthenticationDelegate, @unchecked Sendable {
    private let user: String
    private let key: NIOSSHPrivateKey
    private var offered = false
    private(set) var refused = false // event loop only
    /// Called on refusal. NIO SSH does not close the connection itself when
    /// the delegate gives up, so the handshake has to.
    var onRefused: (() -> Void)?

    init(user: String, key: NIOSSHPrivateKey) {
        self.user = user
        self.key = key
    }

    func nextAuthenticationType(
        availableMethods: NIOSSHAvailableUserAuthenticationMethods,
        nextChallengePromise: EventLoopPromise<NIOSSHUserAuthenticationOffer?>
    ) {
        guard !offered, availableMethods.contains(.publicKey) else {
            refused = true
            nextChallengePromise.succeed(nil)
            onRefused?()
            return
        }
        offered = true
        nextChallengePromise.succeed(NIOSSHUserAuthenticationOffer(
            username: user, serviceName: "", offer: .privateKey(.init(privateKey: key))))
    }
}

/// Accepts exactly the host key derived from the seed.
final class PinnedHostKey: NIOSSHClientServerAuthenticationDelegate, @unchecked Sendable {
    private let expected: NIOSSHPublicKey
    private(set) var mismatched = false // event loop only
    var onMismatch: (() -> Void)?

    init(_ expected: NIOSSHPublicKey) { self.expected = expected }

    func validateHostKey(hostKey: NIOSSHPublicKey, validationCompletePromise: EventLoopPromise<Void>) {
        if hostKey == expected {
            validationCompletePromise.succeed(())
        } else {
            mismatched = true
            validationCompletePromise.fail(ClientError.hostKeyMismatch)
            onMismatch?()
        }
    }
}

/// Completes `ready` when authentication succeeds, or fails it with the reason
/// the connection could not be made.
final class Handshake: ChannelInboundHandler, @unchecked Sendable {
    typealias InboundIn = Any

    private let ready: EventLoopPromise<Void>
    private let login: LoginKey
    private let host: PinnedHostKey
    private var done = false
    private var context: ChannelHandlerContext?

    init(ready: EventLoopPromise<Void>, login: LoginKey, host: PinnedHostKey) {
        self.ready = ready
        self.login = login
        self.host = host
        login.onRefused = { [weak self] in self?.abort(ClientError.authenticationRefused) }
        host.onMismatch = { [weak self] in self?.abort(ClientError.hostKeyMismatch) }
    }

    /// Ends the handshake with `error` and closes the connection. Event loop only.
    func abort(_ error: Error) {
        finish(error)
        context?.close(promise: nil)
    }

    func handlerAdded(context: ChannelHandlerContext) { self.context = context }
    func handlerRemoved(context: ChannelHandlerContext) { self.context = nil }

    private func finish(_ error: Error?) {
        guard !done else { return }
        done = true
        if let error {
            ready.fail(host.mismatched ? ClientError.hostKeyMismatch
                       : login.refused ? ClientError.authenticationRefused : error)
        } else {
            ready.succeed(())
        }
    }

    func userInboundEventTriggered(context: ChannelHandlerContext, event: Any) {
        if event is UserAuthSuccessEvent { finish(nil) }
        context.fireUserInboundEventTriggered(event)
    }

    func errorCaught(context: ChannelHandlerContext, error: Error) {
        finish(error)
        context.close(promise: nil)
    }

    func channelInactive(context: ChannelHandlerContext) {
        finish(ClientError.disconnected)
        context.fireChannelInactive()
    }
}
