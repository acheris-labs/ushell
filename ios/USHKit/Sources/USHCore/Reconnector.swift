/// What a refresh of the rendezvous found.
public enum RefreshOutcome: Sendable, Equatable {
    case online(listed: Set<String>) // the session ids it lists
    case refused(String)             // wrong seed for it; retrying won't help
    case failed                      // unreachable for now
}

/// What rejoining one tab did.
public enum RejoinOutcome: Sendable, Equatable {
    case joined
    case dropped        // the connection went again; the tab still waits
    case failed(String) // the tab is over
}

/// Brings a rendezvous's disconnected tabs back: refresh with backoff, then
/// rejoin each tab whose session is still listed and close the rest, until no
/// tab is waiting. The waiting set is re-read at every step, so a tab that
/// drops again, closes, or starts waiting mid-pass is handled on the next one.
@MainActor
public struct Reconnector<Tab: Hashable> {
    public var waiting: () -> [Tab]
    public var sessionID: (Tab) -> String
    public var refresh: () async -> RefreshOutcome
    public var rejoin: (Tab) async -> RejoinOutcome
    public var end: (Tab, JoinEnd) -> Void
    public var sleep: (Double) async throws -> Void

    public init(waiting: @escaping () -> [Tab], sessionID: @escaping (Tab) -> String,
                refresh: @escaping () async -> RefreshOutcome, rejoin: @escaping (Tab) async -> RejoinOutcome,
                end: @escaping (Tab, JoinEnd) -> Void, sleep: @escaping (Double) async throws -> Void) {
        self.waiting = waiting
        self.sessionID = sessionID
        self.refresh = refresh
        self.rejoin = rejoin
        self.end = end
        self.sleep = sleep
    }

    /// Returns when nothing is waiting, the rendezvous refuses the seed, or
    /// the task is cancelled (tabs then stay waiting for the next run).
    public func run() async {
        var backoff = Backoff()
        while !Task.isCancelled, !waiting().isEmpty {
            switch await refresh() {
            case .refused(let why):
                waiting().forEach { end($0, .ended(why)) }
                return
            case .failed:
                break
            case .online(let listed):
                for tab in waiting() {
                    guard !Task.isCancelled else { return }
                    guard waiting().contains(tab) else { continue } // closed meanwhile
                    guard listed.contains(sessionID(tab)) else {
                        end(tab, .close) // the session ended while disconnected
                        continue
                    }
                    if case .failed(let why) = await rejoin(tab) { end(tab, .ended(why)) }
                }
                if waiting().isEmpty { return }
            }
            do { try await sleep(backoff.delay()) } catch { return }
        }
    }
}
