/// The marks on a tab the user is not looking at.
public struct TabMarks: Equatable, Sendable {
    public var activity = false
    public var bell = false

    public init() {}

    public mutating func output(inFront: Bool) { if !inFront { activity = true } }
    public mutating func rang(inFront: Bool) { if !inFront { bell = true } }
    public mutating func cameToFront() { activity = false; bell = false }
}

/// What a finished `join` means for its tab (exit codes in internal/rendezvous/api.go).
public enum JoinEnd: Equatable, Sendable {
    case close         // the user left through the Ctrl-] panel, or the session is over
    case ended(String) // joining failed; the tab stays to say why
}

public func joinEnd(exitCode: Int) -> JoinEnd {
    switch exitCode {
    case 0, 10, 11: return .close
    default: return .ended("could not join")
    }
}
