import Foundation

/// One rendezvous, as `ush pair` hands it over.
public struct Profile: Codable, Sendable, Equatable, Identifiable {
    public var name: String
    public var host: String
    public var port: Int
    public var user: String
    public var seed: String
    public var id: String { name }

    public init(name: String, host: String, port: Int, user: String, seed: String) {
        self.name = name; self.host = host; self.port = port; self.user = user; self.seed = seed
    }
}

/// The text `ush pair` puts in a QR code, so the app can show one of its own
/// for another device to scan. Whoever reads it gets the seed, and with it
/// every session on that rendezvous.
public func payloadText(_ profile: Profile) -> String {
    struct Wire: Encodable {
        let v = 1
        let name, host: String
        let port: Int
        let user, seed: String
    }
    let wire = Wire(name: profile.name, host: profile.host, port: profile.port,
                    user: profile.user, seed: profile.seed)
    let encoder = JSONEncoder()
    encoder.outputFormatting = .sortedKeys
    return String(decoding: (try? encoder.encode(wire)) ?? Data(), as: UTF8.self)
}

public enum PayloadError: Error, Equatable {
    case notUSH             // someone else's QR code
    case newerVersion(Int)  // from a newer ush than this app
    case incomplete
    case invalidSeed
}

public func parsePayload(_ text: String) throws -> Profile {
    struct Wire: Decodable {
        let v: Int
        let name, host, user, seed: String?
        let port: Int?
    }
    guard let w = try? JSONDecoder().decode(Wire.self, from: Data(text.utf8)) else {
        throw PayloadError.notUSH
    }
    guard w.v >= 1 else { throw PayloadError.notUSH }
    guard w.v == 1 else { throw PayloadError.newerVersion(w.v) }
    guard let name = w.name, !name.isEmpty, let host = w.host, !host.isEmpty,
          let user = w.user, !user.isEmpty, let seed = w.seed, !seed.isEmpty,
          let port = w.port, (1...65535).contains(port) else {
        throw PayloadError.incomplete
    }
    guard (try? parseSeed(seed)) != nil else { throw PayloadError.invalidSeed }
    return Profile(name: name, host: host, port: port, user: user, seed: seed)
}
