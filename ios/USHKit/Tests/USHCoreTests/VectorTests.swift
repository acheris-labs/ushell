import CryptoKit
import Foundation
import XCTest
@testable import USHCore

struct Vector: Decodable {
    let seed: String
    let login_public_key: String
    let host_public_key: String
    let payload: String

    static func load() throws -> Vector {
        // ios/USHKit/Tests/USHCoreTests/VectorTests.swift → repo root
        let root = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let data = try Data(contentsOf: root.appendingPathComponent("test/vectors/pair.json"))
        return try JSONDecoder().decode(Vector.self, from: data)
    }
}

final class VectorTests: XCTestCase {
    func testKeysMatchTheGoDerivation() throws {
        let v = try Vector.load()
        let keys = try Keys(seed: parseSeed(v.seed))
        XCTAssertEqual(authorizedKeyLine(keys.login.publicKey), v.login_public_key)
        XCTAssertEqual(authorizedKeyLine(keys.hostPublicKey), v.host_public_key)
    }

    func testSeedTextIsForgiving() throws {
        let v = try Vector.load()
        let grouped = stride(from: 0, to: v.seed.count, by: 4).map { i -> String in
            let s = v.seed.index(v.seed.startIndex, offsetBy: i)
            let e = v.seed.index(s, offsetBy: min(4, v.seed.count - i))
            return String(v.seed[s..<e])
        }.joined(separator: "-").uppercased()
        XCTAssertEqual(try parseSeed(grouped + "\n"), try parseSeed(v.seed))
    }

    func testBadSeedsAreRefused() {
        XCTAssertThrowsError(try parseSeed(""))
        XCTAssertThrowsError(try parseSeed("not base32 at all!"))
        XCTAssertThrowsError(try parseSeed("aaaa")) // decodes, but not 32 bytes
    }
}
