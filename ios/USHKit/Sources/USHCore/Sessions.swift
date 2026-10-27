import Foundation

/// One entry of `ushd`'s `list` output (internal/rendezvous/sessions.go).
public struct SessionInfo: Decodable, Sendable, Equatable, Identifiable {
    public let id: String
    public let name: String
    public let machine: String
    public let user: String
    public let command: String
    public let cwd: String
    public let description: String
    public let readOnly: Bool
    public let startedEpoch: Int64
    /// How many are joined, this device included. ushd leaves it out when
    /// nobody is, and older ushd never sends it.
    public let viewers: Int?

    enum CodingKeys: String, CodingKey {
        case id = "session_id", name, machine, user, command, cwd, description
        case readOnly = "read_only", startedEpoch = "started_epoch", viewers
    }

    /// How long ago it started, as the menu shows it: 42s / 7m / 3h / 2d.
    public func age(now: Date) -> String {
        let s = max(Int64(now.timeIntervalSince1970) - startedEpoch, 0)
        switch s {
        case ..<60: return "\(s)s"
        case ..<3600: return "\(s / 60)m"
        case ..<86400: return "\(s / 3600)h"
        default: return "\(s / 86400)d"
        }
    }
}

/// Decodes `list` output, sorted as the menu sorts it: by name, then start.
public func decodeSessions(_ data: Data) throws -> [SessionInfo] {
    try JSONDecoder().decode([SessionInfo].self, from: data).sorted {
        ($0.name, $0.startedEpoch) < ($1.name, $1.startedEpoch)
    }
}

/// Sessions grouped under the machine they run on, machines by name,
/// each keeping the order it was given.
public func byMachine(_ sessions: [SessionInfo]) -> [(machine: String, sessions: [SessionInfo])] {
    Dictionary(grouping: sessions, by: \.machine)
        .sorted { $0.key < $1.key }
        .map { (machine: $0.key, sessions: $0.value) }
}

/// Which of `count` colours a machine gets: the same every launch and on
/// every device, which `hashValue` is not (FNV-1a over the name).
public func colorIndex(machine: String, count: Int) -> Int {
    var h: UInt32 = 2_166_136_261
    for b in machine.utf8 { h = (h ^ UInt32(b)) &* 16_777_619 }
    return Int(h % UInt32(count))
}
