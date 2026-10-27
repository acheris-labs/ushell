import XCTest
@testable import USHCore

final class PayloadTests: XCTestCase {
    func testTheVectorPayloadParses() throws {
        let v = try Vector.load()
        let p = try parsePayload(v.payload)
        XCTAssertEqual(p, Profile(name: "home", host: "192.168.10.5", port: 7874, user: "ush", seed: v.seed))
    }

    func testSomeoneElsesQRCodeIsNotUSH() {
        XCTAssertThrowsError(try parsePayload("https://example.com")) { XCTAssertEqual($0 as? PayloadError, .notUSH) }
        XCTAssertThrowsError(try parsePayload("WIFI:S:home;T:WPA;P:pw;;")) { XCTAssertEqual($0 as? PayloadError, .notUSH) }
        XCTAssertThrowsError(try parsePayload(#"{"hello":1}"#)) { XCTAssertEqual($0 as? PayloadError, .notUSH) }
    }

    func testANewerVersionIsRefusedByName() {
        XCTAssertThrowsError(try parsePayload(#"{"v":2,"name":"x"}"#)) { XCTAssertEqual($0 as? PayloadError, .newerVersion(2)) }
    }

    func testMissingFieldsAndBadPortsAreIncomplete() {
        XCTAssertThrowsError(try parsePayload(#"{"v":1,"name":"x","host":"h","port":7874,"user":"ush"}"#)) { XCTAssertEqual($0 as? PayloadError, .incomplete) }
        XCTAssertThrowsError(try parsePayload(#"{"v":1,"name":"","host":"h","port":7874,"user":"ush","seed":"s"}"#)) { XCTAssertEqual($0 as? PayloadError, .incomplete) }
        XCTAssertThrowsError(try parsePayload(#"{"v":1,"name":"x","host":"h","port":70000,"user":"ush","seed":"s"}"#)) { XCTAssertEqual($0 as? PayloadError, .incomplete) }
    }

    func testABadSeedIsInvalid() {
        XCTAssertThrowsError(try parsePayload(#"{"v":1,"name":"x","host":"h","port":7874,"user":"ush","seed":"zzzz"}"#)) { XCTAssertEqual($0 as? PayloadError, .invalidSeed) }
    }
}

final class PayloadTextTests: XCTestCase {
    func testAProfileSurvivesTheRoundTrip() throws {
        let profile = Profile(name: "home", host: "192.168.10.5", port: 7874, user: "ush",
                              seed: "aaaqeayeaudaocajbifqydiob4ibceqtcqkrmfyydenbwha5dypq")
        let text = payloadText(profile)
        XCTAssertEqual(try parsePayload(text), profile)
        // The same shape `ush pair` writes, so either can read the other's.
        XCTAssertTrue(text.contains(#""v":1"#), text)
        XCTAssertTrue(text.contains(#""port":7874"#), text)
    }
}
