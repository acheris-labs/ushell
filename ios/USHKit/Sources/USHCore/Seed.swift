public enum SeedError: Error, Equatable { case invalid }

/// Decodes seed text the way `psk.ParseSeed` does: case, dashes and
/// whitespace are ignored, and it must decode to exactly 32 bytes.
public func parseSeed(_ text: String) throws -> [UInt8] {
    let clean = text.uppercased().filter { !["-", " ", "\t", "\n", "\r"].contains($0) }
    guard !clean.isEmpty, let bytes = Base32.decode(clean), bytes.count == 32 else {
        throw SeedError.invalid
    }
    return bytes
}
