import XCTest
import USHCore
@testable import USH

/// ⌘1–⌘9: the numbers the session list shows, and the flash ⌘N leaves.
@MainActor
final class ShortcutTests: XCTestCase {
    let sessions = try! decodeSessions(Data(#"""
        [{"session_id":"aaaaaaaaaaaaaaaaaaaa","name":"api","machine":"m","user":"u","command":"sh","cwd":"/",
          "description":"pr monitor","read_only":false,"started":"x","started_epoch":1},
         {"session_id":"bbbbbbbbbbbbbbbbbbbb","name":"build","machine":"m","user":"u","command":"make","cwd":"/",
          "description":"","read_only":false,"started":"x","started_epoch":2}]
        """#.utf8))

    func workspace(opening count: Int) -> Workspace {
        let ws = Workspace()
        for i in 0..<count {
            ws.tabs.append(TerminalTab(profileName: "p", session: sessions[i % sessions.count], workspace: ws))
        }
        return ws
    }

    func testNumbersFollowTheOrderTabsOpened() {
        let ws = workspace(opening: 2)
        XCTAssertEqual(ws.shortcut(for: ws.tabs[0]), 1)
        XCTAssertEqual(ws.shortcut(for: ws.tabs[1]), 2)
    }

    func testClosingATabRenumbersTheOnesAfterIt() {
        let ws = workspace(opening: 3)
        let last = ws.tabs[2]
        ws.close(ws.tabs[0])
        XCTAssertEqual(ws.shortcut(for: last), 2)
    }

    func testOnlyTheFirstNineHaveANumber() {
        let ws = workspace(opening: 10)
        XCTAssertEqual(ws.shortcut(for: ws.tabs[8]), 9)
        XCTAssertNil(ws.shortcut(for: ws.tabs[9]))
    }

    func testCommandNFlashesTheTabItPicked() {
        let ws = workspace(opening: 2)
        ws.select(index: 1)
        XCTAssertEqual(ws.frontID, ws.tabs[1].id)
        XCTAssertEqual(ws.flashed?.tabID, ws.tabs[1].id)
    }

    func testPressingItAgainStartsANewFlash() {
        // Each press restarts the timer, so a second ⌘N doesn't vanish early.
        let ws = workspace(opening: 1)
        ws.select(index: 0)
        let first = ws.flashed
        ws.select(index: 0)
        XCTAssertNotEqual(ws.flashed, first)
    }

    func testANumberWithNoTabDoesNothing() {
        let ws = workspace(opening: 1)
        ws.select(index: 4)
        XCTAssertNil(ws.flashed)
    }

    func testTheFlashShowsTheListedDescription() {
        let ws = workspace(opening: 2)
        let rs = RendezvousState(profile: Profile(name: "p", host: "h", port: 1, user: "u", seed: "s"))
        rs.sessions = sessions
        ws.rendezvous = [rs]
        XCTAssertEqual(ws.session(for: ws.tabs[0])?.summary, "pr monitor")
        XCTAssertEqual(ws.session(for: ws.tabs[1])?.summary, "make") // no description: the command
    }
}
