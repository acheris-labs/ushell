import Foundation
import Observation
import USHClient
import USHCore

/// One rendezvous in the tree: its connection, its last list, and how it is doing.
@MainActor @Observable
final class RendezvousState: Identifiable {
    enum Status: Equatable {
        case idle
        case online
        case stale(String)       // the list shown is the last good one
        case unreachable(String)
        case refused(String)     // wrong seed for this rendezvous; retrying won't help
    }

    let profile: Profile
    var sessions: [SessionInfo] = []
    var status: Status = .idle
    var expanded = true
    var recovery: Task<Void, Never>?
    @ObservationIgnored private var conn: RendezvousConnection?
    @ObservationIgnored private var connecting: Task<RendezvousConnection, Error>?

    nonisolated var id: String { profile.name }

    init(profile: Profile) { self.profile = profile }

    /// Whether the tree's timer should refresh it.
    /// A refused seed is a hard stop: only a manual retry tries again.
    var pollsAutomatically: Bool {
        if case .refused = status { return false }
        return true
    }

    func connection() async throws -> RendezvousConnection {
        if let conn, conn.isActive { return conn }
        if let connecting { return try await connecting.value }
        let profile = self.profile
        let task = Task { try await RendezvousConnection.connect(profile) }
        connecting = task
        defer { connecting = nil }
        do {
            let c = try await task.value
            conn = c
            return c
        } catch {
            record(error)
            throw error
        }
    }

    func refresh() async {
        do {
            let c = try await connection()
            sessions = try await c.list()
            status = .online
        } catch {
            record(error)
        }
    }

    func disconnect() {
        recovery?.cancel()
        recovery = nil
        conn?.close()
        conn = nil
    }

    private func record(_ error: Error) {
        let why = (error as? ClientError)?.description ?? error.localizedDescription
        switch error as? ClientError {
        case .hostKeyMismatch?, .authenticationRefused?:
            status = .refused(why)
        default:
            status = sessions.isEmpty ? .unreachable(why) : .stale(why)
        }
        conn?.close()
        conn = nil
    }
}
