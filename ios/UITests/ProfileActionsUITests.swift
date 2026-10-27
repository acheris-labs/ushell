import XCTest

/// The three buttons on a rendezvous row: edit it, show its pairing code,
/// remove it. No rendezvous has to answer for any of this.
final class ProfileActionsUITests: XCTestCase {
    private func launch(skipAuth: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        if skipAuth { app.launchEnvironment["USH_SKIP_AUTH"] = "1" }
        app.launchEnvironment["USH_TEST_PAYLOAD"] =
            #"{"v":1,"name":"home","host":"192.168.10.5","port":7874,"user":"ush","seed":"aaaqeayeaudaocajbifqydiob4ibceqtcqkrmfyydenbwha5dypq"}"#
        app.launch()
        XCTAssertTrue(app.staticTexts["rendezvous.home"].waitForExistence(timeout: 10),
                      "the test profile never appeared")
        return app
    }

    func testTheEyeShowsThePairingCode() {
        let app = launch()
        app.buttons["qr.home"].tap()
        XCTAssertTrue(app.images["qr.image"].waitForExistence(timeout: 5), "no QR code")
        XCTAssertTrue(app.staticTexts["ush@192.168.10.5:7874"].exists)
        app.buttons["Done"].tap()
        XCTAssertTrue(app.staticTexts["rendezvous.home"].waitForExistence(timeout: 5))
    }

    func testThePairingCodeClosesWhenTheAppLeaves() {
        let app = launch()
        app.buttons["qr.home"].tap()
        XCTAssertTrue(app.images["qr.image"].waitForExistence(timeout: 5), "no QR code")
        XCUIDevice.shared.press(.home)
        app.activate()
        XCTAssertTrue(app.staticTexts["rendezvous.home"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.images["qr.image"].exists, "the code survived the app going away")
    }

    func testThePairingCodeNeedsFaceID() {
        // The simulator has no enrolled face, so the code must stay hidden.
        let app = launch(skipAuth: false)
        app.buttons["qr.home"].tap()
        XCTAssertTrue(app.alerts["Can't show the pairing code"].waitForExistence(timeout: 5),
                      "no refusal without Face ID")
        XCTAssertFalse(app.images["qr.image"].exists, "the code was shown without Face ID")
        app.alerts.buttons["OK"].tap()
    }

    func testThePencilEditsTheRendezvous() {
        let app = launch()
        app.buttons["edit.home"].tap()
        let host = app.textFields["edit.host"]
        XCTAssertTrue(host.waitForExistence(timeout: 5), "no edit sheet")
        host.tap()
        host.clearAndType("10.0.0.9")
        XCTAssertEqual(host.value as? String, "10.0.0.9", "the host field kept its old value")
        app.buttons["edit.save"].tap()

        // Back in the list, and the new address is what the code now carries.
        XCTAssertTrue(app.buttons["qr.home"].waitForExistence(timeout: 5))
        app.buttons["qr.home"].tap()
        XCTAssertTrue(app.staticTexts["ush@10.0.0.9:7874"].waitForExistence(timeout: 5))
    }

    func testTheTrashAsksFirst() {
        let app = launch()
        app.buttons["remove.home"].tap()
        // Backing out leaves it alone.
        app.buttons["Cancel"].tap()
        XCTAssertTrue(app.staticTexts["rendezvous.home"].exists)

        app.buttons["remove.home"].tap()
        app.buttons["Remove"].tap()
        XCTAssertTrue(waitUntilGone(app.staticTexts["rendezvous.home"]),
                      "the rendezvous survived being removed")
    }
}

extension XCTestCase {
    /// True once the element is gone, or false after a few seconds.
    func waitUntilGone(_ element: XCUIElement, timeout: TimeInterval = 5) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if !element.exists { return true }
            _ = XCTWaiter.wait(for: [XCTestExpectation()], timeout: 0.2)
        }
        return !element.exists
    }
}

extension XCUIElement {
    /// Replaces what a text field holds: put the caret at the end, delete
    /// back to the start, then type. A long press for Select All is no good:
    /// on iPad it leaves the caret at the start, and the text goes in front.
    func clearAndType(_ text: String) {
        coordinate(withNormalizedOffset: CGVector(dx: 0.98, dy: 0.5)).tap()
        if let existing = value as? String {
            typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: existing.count))
        }
        typeText(text)
    }
}
