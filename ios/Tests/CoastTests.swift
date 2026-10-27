import UIKit
import XCTest
@testable import USH

final class CoastTests: XCTestCase {
    /// Runs a coast at 60fps until it stops; returns the distance and frames.
    private func glide(_ velocity: CGFloat) -> (distance: CGFloat, frames: Int) {
        var c = Coast(velocity: velocity)
        var distance: CGFloat = 0
        var frames = 0
        while !c.done && frames < 10_000 {
            distance += c.advance(by: 1.0 / 60)
            frames += 1
        }
        return (distance, frames)
    }

    func testAFlickGlidesAboutAsFarAsAScrollViewWouldThenStops() {
        // A scroll view's normal rate carries v points/s about v / 2 points.
        let (distance, frames) = glide(1000)
        XCTAssertEqual(distance, 485, accuracy: 25)
        XCTAssertLessThan(frames, 200, "a 1000pt/s flick should stop within a few seconds")
    }

    func testAFlickUpGlidesUp() {
        XCTAssertLessThan(glide(-1000).distance, 0)
    }

    func testASlowLiftDoesNotGlide() {
        XCTAssertTrue(Coast(velocity: 30).done)
        XCTAssertFalse(Coast(velocity: 500).done)
    }
}
