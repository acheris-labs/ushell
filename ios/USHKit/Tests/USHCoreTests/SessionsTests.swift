import Foundation
import XCTest
@testable import USHCore

final class SessionsTests: XCTestCase {
    // The shape ushd's list command prints (internal/rendezvous/sessions.go).
    let json = #"""
    [
      {"session_id":"bbbbbbbbbbbbbbbbbbbb","name":"web","machine":"mbp","user":"alex","command":"claude","cwd":"/src","description":"","read_only":false,"started":"x","started_epoch":200},
      {"session_id":"aaaaaaaaaaaaaaaaaaaa","name":"api","machine":"laptop","user":"alex","command":"zsh","cwd":"/","description":"staging","read_only":true,"started":"x","started_epoch":100,"viewers":2},
      {"session_id":"cccccccccccccccccccc","name":"api","machine":"pod","user":"alex","command":"zsh","cwd":"/","description":"","read_only":false,"started":"x","started_epoch":50}
    ]
    """#

    func testDecodesAndSortsByNameThenStart() throws {
        let s = try decodeSessions(Data(json.utf8))
        XCTAssertEqual(s.map(\.id), ["cccccccccccccccccccc", "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"])
        XCTAssertEqual(s[1].description, "staging")
        XCTAssertTrue(s[1].readOnly)
        XCTAssertEqual(s[1].viewers, 2)
        XCTAssertNil(s[0].viewers, "nobody watching: ushd leaves the field out")
    }

    func testGroupsByMachineKeepingOrder() throws {
        let s = try decodeSessions(Data(#"""
        [
          {"session_id":"aaaaaaaaaaaaaaaaaaaa","name":"web","machine":"pod","user":"alex","command":"zsh","cwd":"/","description":"","read_only":false,"started":"x","started_epoch":1},
          {"session_id":"bbbbbbbbbbbbbbbbbbbb","name":"api","machine":"mbp","user":"alex","command":"zsh","cwd":"/","description":"","read_only":false,"started":"x","started_epoch":2},
          {"session_id":"cccccccccccccccccccc","name":"db","machine":"pod","user":"alex","command":"zsh","cwd":"/","description":"","read_only":false,"started":"x","started_epoch":3}
        ]
        """#.utf8))
        let g = byMachine(s)
        XCTAssertEqual(g.map(\.machine), ["mbp", "pod"])
        XCTAssertEqual(g[1].sessions.map(\.name), ["db", "web"])
        XCTAssertTrue(byMachine([]).isEmpty)
    }

    func testColorIndexIsStableAndInRange() {
        XCTAssertEqual(colorIndex(machine: "laptop", count: 8), colorIndex(machine: "laptop", count: 8))
        XCTAssertEqual(colorIndex(machine: "", count: 8), Int(2_166_136_261 % 8))
        let used = Set(["laptop", "mbp", "pod", "build-box", "nas"].map { colorIndex(machine: $0, count: 8) })
        XCTAssertTrue(used.allSatisfy { (0..<8).contains($0) })
        XCTAssertGreaterThan(used.count, 1)
    }

    func testEmptyList() throws {
        XCTAssertEqual(try decodeSessions(Data("[]\n".utf8)), [])
    }

    func testAgeMatchesTheMenu() throws {
        let s = try decodeSessions(Data(json.utf8))[0] // started_epoch 50
        func at(_ t: Int64) -> String { s.age(now: Date(timeIntervalSince1970: TimeInterval(t))) }
        XCTAssertEqual(at(50 + 42), "42s")
        XCTAssertEqual(at(50 + 7 * 60), "7m")
        XCTAssertEqual(at(50 + 3 * 3600), "3h")
        XCTAssertEqual(at(50 + 2 * 86400), "2d")
        XCTAssertEqual(at(10), "0s") // clock skew never shows negative
    }
}
