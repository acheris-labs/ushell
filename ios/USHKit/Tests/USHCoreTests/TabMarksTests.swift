import XCTest
@testable import USHCore

final class TabMarksTests: XCTestCase {
    func testOnlyBackgroundTabsGetMarked() {
        var m = TabMarks()
        m.output(inFront: true); m.rang(inFront: true)
        XCTAssertEqual(m, TabMarks())
        m.output(inFront: false); m.rang(inFront: false)
        XCTAssertTrue(m.activity); XCTAssertTrue(m.bell)
        m.cameToFront()
        XCTAssertEqual(m, TabMarks())
    }

    func testExitCodes() {
        XCTAssertEqual(joinEnd(exitCode: 10), .close)
        XCTAssertEqual(joinEnd(exitCode: 11), .close)
        XCTAssertEqual(joinEnd(exitCode: 0), .close)
        XCTAssertEqual(joinEnd(exitCode: 1), .ended("could not join"))
        XCTAssertEqual(joinEnd(exitCode: 2), .ended("could not join"))
    }

    func testBackoffDoublesToThirty() {
        var b = Backoff()
        XCTAssertEqual((0..<6).map { _ in b.delay() }, [2, 4, 8, 16, 30, 30])
        b.reset()
        XCTAssertEqual(b.delay(), 2)
    }
}
