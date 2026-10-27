import CryptoKit
import Foundation

/// The two keys a seed stands for: the login key every ush device shares, and
/// the public half of the rendezvous's host key, which the app pins.
public struct Keys: Sendable {
    public let login: Curve25519.Signing.PrivateKey
    public let hostPublicKey: Curve25519.Signing.PublicKey

    public init(seed: [UInt8]) throws {
        login = try Curve25519.Signing.PrivateKey(rawRepresentation: seed)
        // psk.HostKey: HKDF-SHA256(seed, salt nil, info "ush/host-key/sshd"), 32 bytes.
        let hostSeed = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: seed), salt: Data(),
            info: Data("ush/host-key/sshd".utf8), outputByteCount: 32)
        let raw = hostSeed.withUnsafeBytes { Data($0) }
        hostPublicKey = try Curve25519.Signing.PrivateKey(rawRepresentation: raw).publicKey
    }

    public init(login: Curve25519.Signing.PrivateKey, hostPublicKey: Curve25519.Signing.PublicKey) {
        self.login = login
        self.hostPublicKey = hostPublicKey
    }
}

/// An ed25519 public key as an authorized_keys line, without a comment.
public func authorizedKeyLine(_ key: Curve25519.Signing.PublicKey) -> String {
    func string(_ b: [UInt8]) -> [UInt8] {
        let n = UInt32(b.count)
        return [UInt8(n >> 24), UInt8(n >> 16 & 0xff), UInt8(n >> 8 & 0xff), UInt8(n & 0xff)] + b
    }
    let blob = string(Array("ssh-ed25519".utf8)) + string(Array(key.rawRepresentation))
    return "ssh-ed25519 " + Data(blob).base64EncodedString()
}
