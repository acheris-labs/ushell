/// RFC 4648 base32 without padding, as Go's `base32.StdEncoding.WithPadding(NoPadding)`.
enum Base32 {
    private static let alphabet = Array("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567".utf8)

    static func decode(_ text: String) -> [UInt8]? {
        var out: [UInt8] = []
        var buffer: UInt64 = 0
        var bits = 0
        for c in text.utf8 {
            guard let v = alphabet.firstIndex(of: c) else { return nil }
            buffer = (buffer << 5) | UInt64(v)
            bits += 5
            if bits >= 8 {
                bits -= 8
                out.append(UInt8((buffer >> UInt64(bits)) & 0xff))
                buffer &= (1 << UInt64(bits)) - 1
            }
        }
        return out
    }
}
