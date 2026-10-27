import Foundation
import UIKit
import XCTest
import USHClient
import USHCore
@testable import USH

/// A joined session that records what the tab does with it.
final class FakeJoin: JoinSession, @unchecked Sendable {
    var resizes: [[Int]] = []
    var closed = false
    private var exit: CheckedContinuation<Int, Error>?

    func send(_ bytes: [UInt8]) {}
    func resize(cols: Int, rows: Int) { resizes.append([cols, rows]) }
    func exitCode() async throws -> Int {
        try await withCheckedThrowingContinuation { exit = $0 }
    }
    func close() {
        closed = true
        exit?.resume(throwing: ClientError.disconnected)
        exit = nil
    }
}

@MainActor
final class TabTests: XCTestCase {
    let session = try! decodeSessions(Data(#"""
        [{"session_id":"aaaaaaaaaaaaaaaaaaaa","name":"api","machine":"m","user":"u","command":"sh","cwd":"/",
          "description":"","read_only":false,"started":"x","started_epoch":1}]
        """#.utf8))[0]

    func makeTab() -> TerminalTab {
        TerminalTab(profileName: "p", session: session, workspace: Workspace())
    }

    // The view settles its size while the join is in flight; that size must
    // reach the session once the channel exists.
    func testASizeChangeDuringTheJoinReachesTheSession() async throws {
        let tab = makeTab()
        let fake = FakeJoin()
        try await tab.attach { _, _, _ in
            tab.terminal.getTerminal().resize(cols: 120, rows: 40)
            return fake
        }
        XCTAssertEqual(fake.resizes.last, [120, 40])
        fake.close()
    }

    func testATabClosedWhileJoiningClosesTheNewChannel() async throws {
        let tab = makeTab()
        let fake = FakeJoin()
        try? await tab.attach { _, _, _ in
            tab.close()
            return fake
        }
        XCTAssertTrue(fake.closed, "the join landed on a closed tab and stayed open")
    }

    func testRejoiningClosesThePreviousChannel() async throws {
        let tab = makeTab()
        let first = FakeJoin(), second = FakeJoin()
        try await tab.attach { _, _, _ in first }
        try await tab.attach { _, _, _ in second }
        XCTAssertTrue(first.closed, "two live channels on one tab")
        second.close()
    }

    func testARefusedRendezvousIsNotPolled() {
        let rs = RendezvousState(profile: Profile(name: "p", host: "h", port: 1, user: "u", seed: "s"))
        rs.status = .online
        XCTAssertTrue(rs.pollsAutomatically)
        rs.status = .refused("this seed isn't accepted by this rendezvous")
        XCTAssertFalse(rs.pollsAutomatically)
    }

    func testADroppedConnectionKeepsTheTabWaiting() {
        XCTAssertEqual(rejoinOutcome(after: ClientError.disconnected), .dropped)
        XCTAssertEqual(rejoinOutcome(after: ClientError.timedOut), .dropped)
        XCTAssertEqual(rejoinOutcome(after: ClientError.unreachable("x")), .dropped)
        XCTAssertEqual(rejoinOutcome(after: ClientError.hostKeyMismatch), .failed("not the rendezvous this seed belongs to"))
    }

    func testOnlyWebLinksOpen() {
        XCTAssertEqual(webLink("https://example.com/x")?.absoluteString, "https://example.com/x")
        XCTAssertNotNil(webLink("HTTP://example.com"))
        for link in ["tel:5551234", "sms:5551234", "shortcuts://run-shortcut?name=x", "file:///etc/passwd", "javascript:alert(1)", "not a url"] {
            XCTAssertNil(webLink(link), link)
        }
    }

    func testASessionsCopyStaysOnThisDevice() {
        copyFromSession(Data("copied-by-session".utf8))
        XCTAssertEqual(UIPasteboard.general.string, "copied-by-session")
    }
}
