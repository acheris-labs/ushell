import SwiftTerm
import UIKit

/// A TerminalView whose one-finger drag scrolls apps that track the mouse.
/// SwiftTerm sends that drag as a held button, which Claude's fullscreen UI
/// reads as a text selection; this sends wheel ticks instead, a tick per
/// row of travel. A flick keeps ticking after the finger lifts and slows
/// down as a scroll view would. Long-press still selects.
final class WheelTerminalView: TerminalView {
    private var wheelPan: UIPanGestureRecognizer?
    private var carried: CGFloat = 0
    /// Where the finger last was: momentum ticks report it as the pointer.
    private var pointer: CGPoint = .zero
    private var coast: Coast?
    private var coastLink: CADisplayLink?

    /// The keyboard comes up when a terminal comes on screen, and only then:
    /// once hidden it stays hidden until the terminal is tapped.
    override func didMoveToWindow() {
        super.didMoveToWindow()
        guard window != nil else { return stopCoast() }
        DispatchQueue.main.async { [weak self] in _ = self?.becomeFirstResponder() }
    }

    /// A finger landing stops a coast, as it stops a scroll view.
    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent?) {
        stopCoast()
        super.touchesBegan(touches, with: event)
    }

    override func mouseModeChanged(source: Terminal) {
        // Not super: that installs SwiftTerm's drag-as-button gesture.
        if source.mouseMode == .off {
            if let pan = wheelPan { removeGestureRecognizer(pan) }
            wheelPan = nil
            stopCoast()
        } else if wheelPan == nil {
            let pan = UIPanGestureRecognizer(target: self, action: #selector(wheel))
            pan.maximumNumberOfTouches = 1
            // A trackpad swipe or mouse wheel scrolls too; pans take only touches unless told.
            pan.allowedScrollTypesMask = .all
            addGestureRecognizer(pan)
            wheelPan = pan
        }
    }

    @objc private func wheel(_ pan: UIPanGestureRecognizer) {
        switch pan.state {
        case .began:
            stopCoast()
            carried = 0
        case .changed:
            carried += pan.translation(in: self).y
            pan.setTranslation(.zero, in: self)
            pointer = pan.location(in: self)
            sendTicks(atMost: .max)
        case .ended:
            let flick = Coast(velocity: pan.velocity(in: self).y)
            guard !flick.done else { return }
            coast = flick
            let link = CADisplayLink(target: self, selector: #selector(glide))
            link.add(to: .main, forMode: .common)
            coastLink = link
        default:
            break
        }
    }

    @objc private func glide(_ link: CADisplayLink) {
        guard var c = coast else { return stopCoast() }
        carried += c.advance(by: link.targetTimestamp - link.timestamp)
        coast = c
        // One tick a frame: apps that speed up fast wheels (Claude does)
        // would otherwise fling much further than the finger asked.
        sendTicks(atMost: 1)
        if c.done { stopCoast() }
    }

    private func stopCoast() {
        coastLink?.invalidate()
        coastLink = nil
        coast = nil
    }

    /// Turns whole rows of `carried` travel into wheel ticks; travel past
    /// `atMost` ticks is dropped.
    private func sendTicks(atMost limit: Int) {
        let t = getTerminal()
        guard allowMouseReporting, t.rows > 0, t.cols > 0 else { return }
        let rowHeight = bounds.height / CGFloat(t.rows)
        let ticks = Int(carried / rowHeight)
        guard ticks != 0 else { return }
        carried -= CGFloat(ticks) * rowHeight
        // Finger down shows earlier lines: wheel up (button 4).
        let button = ticks > 0 ? 4 : 5
        let col = min(t.cols - 1, max(0, Int(pointer.x / (bounds.width / CGFloat(t.cols)))))
        let row = min(t.rows - 1, max(0, Int((pointer.y - contentOffset.y) / rowHeight)))
        let flags = t.encodeButton(button: button, release: false, shift: false, meta: false, control: false)
        for _ in 0..<min(abs(ticks), limit) {
            t.sendEvent(buttonFlags: flags, x: col, y: row)
        }
    }
}

/// A flick's glide after the finger lifts, slowing at a scroll view's
/// normal rate.
struct Coast {
    /// Points per second; positive is finger down.
    private(set) var velocity: CGFloat

    init(velocity: CGFloat) { self.velocity = velocity }

    /// Too slow to bother: under a row every few frames.
    var done: Bool { abs(velocity) < 60 }

    /// The distance covered in the next `seconds`, slowing as it goes.
    mutating func advance(by seconds: CFTimeInterval) -> CGFloat {
        let distance = velocity * CGFloat(seconds)
        velocity *= pow(UIScrollView.DecelerationRate.normal.rawValue, CGFloat(seconds) * 1000)
        return distance
    }
}
