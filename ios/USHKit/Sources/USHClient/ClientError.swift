public enum ClientError: Error, Equatable, CustomStringConvertible {
    case hostKeyMismatch
    case authenticationRefused
    case unreachable(String)
    case disconnected
    case channelRefused
    case timedOut
    case commandFailed(code: Int, message: String)

    public var description: String {
        switch self {
        case .hostKeyMismatch: return "not the rendezvous this seed belongs to"
        case .authenticationRefused: return "this seed isn't accepted by this rendezvous"
        case .unreachable(let why): return "unreachable: \(why)"
        case .disconnected: return "disconnected"
        case .channelRefused: return "the rendezvous refused the request"
        case .timedOut: return "the rendezvous stopped answering"
        case .commandFailed(let code, let message):
            return message.isEmpty ? "failed (exit \(code))" : message
        }
    }
}
