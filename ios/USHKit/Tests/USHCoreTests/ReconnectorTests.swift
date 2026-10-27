import XCTest
@testable import USHCore

/// A rendezvous and its tabs, as the reconnector sees them. Tabs are named
/// after their session.
@MainActor
final class World {
    var waiting: [String]
    var listed: Set<String> = []
    var refreshes: [RefreshOutcome] = []
    var rejoins: [String: [RejoinOutcome]] = [:]
    var joined: [String] = []
    var ended: [String: JoinEnd] = [:]
    var slept: [Double] = []
    var onRejoin: ((String) -> Void)?
    var onRefresh: (() -> Void)?

    init(waiting: [String]) { self.waiting = waiting }

    var reconnector: Reconnector<String> {
        Reconnector(
            waiting: { self.waiting },
            sessionID: { $0 },
            refresh: {
                self.onRefresh?()
                return self.refreshes.isEmpty ? .online(listed: self.listed) : self.refreshes.removeFirst()
            },
            rejoin: { tab in
                self.onRejoin?(tab)
                let outcome = self.rejoins[tab]?.isEmpty == false ? self.rejoins[tab]!.removeFirst() : .joined
                if case .joined = outcome {
                    self.joined.append(tab)
                    self.waiting.removeAll { $0 == tab }
                }
                return outcome
            },
            end: { tab, end in
                self.ended[tab] = end
                self.waiting.removeAll { $0 == tab }
            },
            sleep: { self.slept.append($0) })
    }
}

@MainActor
final class ReconnectorTests: XCTestCase {
    func testRejoinsListedTabsAndClosesTheRest() async {
        let w = World(waiting: ["a", "b"])
        w.listed = ["a"]
        await w.reconnector.run()
        XCTAssertEqual(w.joined, ["a"])
        XCTAssertEqual(w.ended, ["b": .close])
    }

    func testBacksOffWhileTheRendezvousIsUnreachable() async {
        let w = World(waiting: ["a"])
        w.listed = ["a"]
        w.refreshes = [.failed, .failed]
        await w.reconnector.run()
        XCTAssertEqual(w.slept, [2, 4])
        XCTAssertEqual(w.joined, ["a"])
    }

    func testARefusalEndsEveryWaitingTab() async {
        let w = World(waiting: ["a", "b"])
        w.refreshes = [.refused("this seed isn't accepted by this rendezvous")]
        await w.reconnector.run()
        XCTAssertEqual(w.ended.count, 2)
        XCTAssertEqual(w.ended["a"], .ended("this seed isn't accepted by this rendezvous"))
        XCTAssertEqual(w.joined, [])
    }

    // The connection drops again while tabs are rejoining: the tab must go
    // back to waiting and be retried, not stranded on "reconnecting".
    func testATabThatDropsAgainDuringRejoinIsRetried() async {
        let w = World(waiting: ["a", "b"])
        w.listed = ["a", "b"]
        w.rejoins = ["a": [.dropped], "b": [.dropped]]
        await w.reconnector.run()
        XCTAssertEqual(Set(w.joined), ["a", "b"])
        XCTAssertEqual(w.ended, [:])
        XCTAssertEqual(w.slept, [2])
    }

    func testATabThatFailsToRejoinIsEndedWithTheReason() async {
        let w = World(waiting: ["a"])
        w.listed = ["a"]
        w.rejoins = ["a": [.failed("could not join")]]
        await w.reconnector.run()
        XCTAssertEqual(w.ended, ["a": .ended("could not join")])
    }

    // A tab closed while another one rejoins must not be attached.
    func testATabClosedDuringRejoinIsLeftAlone() async {
        let w = World(waiting: ["a", "b"])
        w.listed = ["a", "b"]
        w.onRejoin = { tab in if tab == "a" { w.waiting.removeAll { $0 == "b" } } }
        await w.reconnector.run()
        XCTAssertEqual(w.joined, ["a"])
        XCTAssertEqual(w.ended, [:])
    }

    // A tab that starts waiting while a pass is under way is picked up.
    func testATabThatDropsMidPassIsPickedUp() async {
        let w = World(waiting: ["a"])
        w.listed = ["a", "c"]
        w.onRejoin = { tab in if tab == "a" { w.waiting.append("c") } }
        await w.reconnector.run()
        XCTAssertEqual(w.joined, ["a", "c"])
    }

    // Recovery cancelled (the app went to the background) while refreshing
    // must not rejoin anything: a new recovery will, when the app is back.
    func testCancelledDuringRefreshRejoinsNothing() async {
        let w = World(waiting: ["a"])
        w.listed = ["a"]
        let task = Task { await w.reconnector.run() }
        w.onRefresh = { task.cancel() }
        await task.value
        XCTAssertEqual(w.joined, [])
        XCTAssertEqual(w.waiting, ["a"])
    }

    func testNothingWaitingDoesNothing() async {
        let w = World(waiting: [])
        var refreshed = false
        w.onRefresh = { refreshed = true }
        await w.reconnector.run()
        XCTAssertFalse(refreshed)
    }
}
