import XCTest

final class WorkspaceUITests: XCTestCase {
    func testSessionsSwitchWithCommandDigitsAndTheDetachKeyClosesOne() throws {
        let env = ProcessInfo.processInfo.environment
        guard let port = env["USH_TEST_PORT"], let seed = env["USH_TEST_SEED"] else {
            throw XCTSkip("needs a local ushd: make test-ios-ui")
        }
        let app = XCUIApplication()
        app.launchEnvironment["USH_TEST_PAYLOAD"] =
            #"{"v":1,"name":"test","host":"127.0.0.1","port":\#(port),"user":"ush","seed":"\#(seed)"}"#
        app.launchEnvironment["USH_FORCE_KEYBAR"] = "1"
        app.launch()

        let echo = app.buttons["session.swift-echo"]
        XCTAssertTrue(echo.waitForExistence(timeout: 15), "the tree never listed swift-echo")
        echo.tap()
        // The simulator swallows the first hardware key while the on-screen
        // keyboard is up; spend it on a key that does nothing.
        app.typeKey(XCUIKeyboardKey.shift, modifierFlags: [])

        // The terminal takes the screen; the pull tab brings the tree back.
        let other = app.buttons["session.swift-other"]
        XCTAssertTrue(waitUntil { !other.isHittable }, "the tree stayed on screen with a session open")
        app.buttons["sessions.pull"].tap()
        XCTAssertTrue(waitUntil { other.isHittable }, "the pull tab did not bring the tree back")
        XCTAssertEqual(echo.value as? String, "front")
        other.tap()
        XCTAssertTrue(waitUntil { !echo.isHittable })

        app.typeKey("o", modifierFlags: .command) // ⌘O brings the tree back too
        XCTAssertTrue(waitUntil { echo.isHittable }, "⌘O did not bring the tree back")
        XCTAssertEqual(other.value as? String, "front")
        XCTAssertEqual(echo.value as? String, "joined")

        // ⌘digit picks a session and puts the tree away again.
        app.typeKey("1", modifierFlags: .command)
        XCTAssertTrue(waitUntil { !echo.isHittable })
        app.typeKey("o", modifierFlags: .command)
        XCTAssertTrue(waitUntil { echo.isHittable })
        XCTAssertEqual(echo.value as? String, "front")
        app.typeKey("2", modifierFlags: .command)
        XCTAssertTrue(waitUntil { !echo.isHittable })

        // Ctrl-] from the key bar opens the session panel; "l" goes back to
        // the list, which exits 10 and closes the session.
        let detach = app.buttons["key.detach"]
        XCTAssertTrue(detach.waitForExistence(timeout: 5), "the key bar is not showing")
        detach.tap()
        sleep(1)
        app.typeText("l")
        // Closing it brings echo to the front, which puts the tree away; ask
        // for it until the session shows as closed.
        let closed = waitUntil(timeout: 10) {
            if !other.isHittable { app.typeKey("o", modifierFlags: .command) }
            return other.isHittable && other.value as? String == ""
        }
        XCTAssertTrue(closed, "the session stayed open after back to list")
        XCTAssertEqual(echo.value as? String, "front")
    }

    func testASessionThatEndsCloses() throws {
        let env = ProcessInfo.processInfo.environment
        guard let port = env["USH_TEST_PORT"], let seed = env["USH_TEST_SEED"] else {
            throw XCTSkip("needs a local ushd: make test-ios-ui")
        }
        let app = XCUIApplication()
        app.launchEnvironment["USH_TEST_PAYLOAD"] =
            #"{"v":1,"name":"test","host":"127.0.0.1","port":\#(port),"user":"ush","seed":"\#(seed)"}"#
        app.launch()

        let ender = app.buttons["session.swift-ender"]
        XCTAssertTrue(ender.waitForExistence(timeout: 15), "the tree never listed swift-ender")
        ender.tap()
        XCTAssertTrue(waitUntil { !ender.isHittable }, "the tree stayed on screen with a session open")
        app.typeKey(XCUIKeyboardKey.shift, modifierFlags: []) // see the first test
        sleep(1)
        app.typeText("quit\n")
        // Nothing else is open, so the tree comes back; the session itself is
        // gone from it.
        XCTAssertTrue(waitUntil(timeout: 10) { app.buttons["scan"].isHittable },
                      "the tree did not come back after the session ended")
        XCTAssertTrue(ender.waitForNonExistence(timeout: 10), "the ended session is still listed")
    }

    /// Claude's fullscreen UI tracks the mouse; a drag there has to scroll
    /// (wheel ticks), not hold a button down and select.
    func testDragSendsWheelTicksWhenTheAppTracksTheMouse() throws {
        let env = ProcessInfo.processInfo.environment
        guard let port = env["USH_TEST_PORT"], let seed = env["USH_TEST_SEED"],
              let bytesPath = env["USH_TEST_MOUSE"] else {
            throw XCTSkip("needs a local ushd: make test-ios-ui")
        }
        let app = XCUIApplication()
        app.launchEnvironment["USH_TEST_PAYLOAD"] =
            #"{"v":1,"name":"test","host":"127.0.0.1","port":\#(port),"user":"ush","seed":"\#(seed)"}"#
        app.launch()

        let mouse = app.buttons["session.swift-mouse"]
        XCTAssertTrue(mouse.waitForExistence(timeout: 15), "the tree never listed swift-mouse")
        mouse.tap()
        XCTAssertTrue(waitUntil { !mouse.isHittable }, "the tree stayed on screen with a session open")
        sleep(1) // let the mouse-mode escape land before dragging

        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
            .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6)))
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6))
            .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))

        var got = ""
        let ticked = waitUntil {
            got = (try? String(contentsOfFile: bytesPath, encoding: .utf8)) ?? ""
            return got.contains("\u{1b}[<64;") && got.contains("\u{1b}[<65;")
        }
        XCTAssertTrue(ticked, "no wheel ticks both ways; got \(got.debugDescription)")
        XCTAssertFalse(got.contains("\u{1b}[<0;"), "a drag pressed button 1: \(got.debugDescription)")
    }

    /// There is no tab strip: the list is how to switch, and a swipe on a
    /// session there closes it. It also shows who is watching.
    func testNoTabsAndASessionClosesFromTheList() throws {
        let env = ProcessInfo.processInfo.environment
        guard let port = env["USH_TEST_PORT"], let seed = env["USH_TEST_SEED"] else {
            throw XCTSkip("needs a local ushd: make test-ios-ui")
        }
        let app = XCUIApplication()
        app.launchEnvironment["USH_TEST_PAYLOAD"] =
            #"{"v":1,"name":"test","host":"127.0.0.1","port":\#(port),"user":"ush","seed":"\#(seed)"}"#
        app.launch()

        let other = app.buttons["session.swift-other"]
        XCTAssertTrue(other.waitForExistence(timeout: 15), "the tree never listed swift-other")
        other.tap()
        XCTAssertTrue(waitUntil { !other.isHittable }, "the tree stayed on screen with a session open")
        XCTAssertFalse(app.buttons["tab.swift-other"].exists, "there is a tab strip")
        XCTAssertFalse(app.buttons["tabs.pull"].exists, "there is a tab strip's pull tab")

        app.buttons["sessions.pull"].tap()
        XCTAssertTrue(waitUntil { other.isHittable }, "the pull tab did not bring the tree back")
        // This device's own join counts.
        let watching = app.staticTexts["viewers.swift-other"]
        XCTAssertTrue(watching.waitForExistence(timeout: 10), "no viewer count on the joined session")

        other.swipeLeft()
        let close = app.buttons["Close"]
        XCTAssertTrue(close.waitForExistence(timeout: 5), "swiping a joined session offers no Close")
        close.tap()
        XCTAssertTrue(watching.waitForNonExistence(timeout: 10), "the session still has a viewer after closing")
    }

    private func waitUntil(timeout: TimeInterval = 5, _ condition: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if condition() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        return condition()
    }
}
