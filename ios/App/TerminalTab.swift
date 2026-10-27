import Foundation
import Observation
import SwiftTerm
import UIKit
import UniformTypeIdentifiers
import USHClient
import USHCore

@MainActor @Observable
final class TerminalTab: Identifiable {
    enum Phase: Equatable {
        case connecting
        case live
        case reconnecting
        case ended(String?)
    }

    let id = UUID()
    let profileName: String
    let sessionID: String
    let title: String
    let machine: String
    var phase: Phase = .connecting
    var marks = TabMarks()
    @ObservationIgnored let terminal: TerminalView
    @ObservationIgnored private var driver: TerminalDriver?
    @ObservationIgnored private var channel: JoinSession?
    @ObservationIgnored private var closed = false
    @ObservationIgnored weak var workspace: Workspace?

    init(profileName: String, session: SessionInfo, workspace: Workspace) {
        self.profileName = profileName
        self.sessionID = session.id
        self.title = session.name
        self.machine = session.machine
        self.workspace = workspace
        terminal = WheelTerminalView(frame: CGRect(x: 0, y: 0, width: 800, height: 600))
        let driver = TerminalDriver(tab: self)
        self.driver = driver
        terminal.terminalDelegate = driver
        applyKeyBar(hardwareKeyboard: workspace.hardwareKeyboard)
        applyCursor(blinking: workspace.blinkingCursor)
    }

    var isFront: Bool { workspace?.frontID == id }

    typealias Join = (_ cols: Int, _ rows: Int, _ onOutput: @escaping @Sendable ([UInt8]) -> Void) async throws -> JoinSession

    /// Joins the session over `connection`, replacing any earlier channel.
    /// Scrollback survives a rejoin: the view is the same.
    func attach(_ connection: RendezvousConnection) async throws {
        let id = sessionID
        try await attach { cols, rows, onOutput in
            try await connection.join(sessionID: id, cols: cols, rows: rows, onOutput: onOutput)
        }
    }

    func attach(_ join: Join) async throws {
        let t = terminal.getTerminal()
        let ch = try await join(t.cols, t.rows) { [weak self] bytes in
            // FIFO onto the main queue: Tasks could reorder output.
            DispatchQueue.main.async { MainActor.assumeIsolated { self?.received(bytes) } }
        }
        guard !closed else {
            ch.close()
            throw CancellationError()
        }
        channel?.close()
        channel = ch
        // The view may have settled its size while the join was in flight,
        // when there was no channel to tell.
        ch.resize(cols: t.cols, rows: t.rows)
        phase = .live
        Task { [weak self] in
            let code = try? await ch.exitCode()
            guard let self, self.channel === ch else { return } // replaced or closed on purpose
            self.channel = nil
            self.workspace?.tabExited(self, code: code)
        }
    }

    /// Ends this tab's channel for good; a join still in flight is closed
    /// when it lands.
    func close() {
        closed = true
        channel?.close()
        channel = nil
    }

    func applyKeyBar(hardwareKeyboard: Bool) {
        let forced = ProcessInfo.processInfo.environment["USH_FORCE_KEYBAR"] == "1"
        terminal.inputAccessoryView = hardwareKeyboard && !forced ? nil : KeyBar(terminal: terminal)
        terminal.reloadInputViews()
    }

    /// A blinking cursor animates forever, redrawing the screen every frame
    /// even when nothing else changes, so it is off unless asked for in
    /// Settings. A session can still ask for one.
    func applyCursor(blinking: Bool) {
        terminal.getTerminal().setCursorStyle(blinking ? .blinkBlock : .steadyBlock)
    }

    fileprivate func received(_ bytes: [UInt8]) {
        terminal.feed(byteArray: bytes[...])
        mark { $0.output(inFront: isFront) }
    }

    /// Sets the marks only when they change: every set redraws the views
    /// that show them, and output arrives many times a second.
    private func mark(_ change: (inout TabMarks) -> Void) {
        var m = marks
        change(&m)
        if m != marks { marks = m }
    }

    fileprivate func send(_ data: ArraySlice<UInt8>) { channel?.send(Array(data)) }
    fileprivate func resize(cols: Int, rows: Int) { channel?.resize(cols: cols, rows: rows) }
    fileprivate func rang() { mark { $0.rang(inFront: isFront) } }
}

/// SwiftTerm's delegate, forwarding to the tab. SwiftTerm calls it on the main thread.
private final class TerminalDriver: NSObject, TerminalViewDelegate {
    weak var tab: TerminalTab?
    init(tab: TerminalTab) { self.tab = tab }

    func send(source: TerminalView, data: ArraySlice<UInt8>) {
        MainActor.assumeIsolated { tab?.send(data) }
    }
    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        MainActor.assumeIsolated { tab?.resize(cols: newCols, rows: newRows) }
    }
    func bell(source: TerminalView) {
        MainActor.assumeIsolated { tab?.rang() }
    }
    func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        if let url = webLink(link) { UIApplication.shared.open(url) }
    }
    func setTerminalTitle(source: TerminalView, title: String) {}
    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    func scrolled(source: TerminalView, position: Double) {}
    func clipboardCopy(source: TerminalView, content: Data) {
        copyFromSession(content)
    }
    func clipboardRead(source: TerminalView) -> Data? { nil }
    func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
    func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}

/// The URL a tapped link may open: web links only. A link's text can hide
/// where it goes, and other schemes (tel:, sms:, other apps') act on the device.
func webLink(_ link: String) -> URL? {
    guard let url = URL(string: link), ["http", "https"].contains(url.scheme?.lowercased()) else { return nil }
    return url
}

/// A copy the session asked for (OSC 52) stays on this device: anything the
/// session runs can send one, unasked, and Universal Clipboard would carry it
/// to a Mac to be pasted into a shell there.
func copyFromSession(_ content: Data) {
    UIPasteboard.general.setItems([[UTType.utf8PlainText.identifier: String(decoding: content, as: UTF8.self)]],
                                  options: [.localOnly: true])
}

extension TerminalTab: Hashable {
    nonisolated static func == (a: TerminalTab, b: TerminalTab) -> Bool { a === b }
    nonisolated func hash(into hasher: inout Hasher) { hasher.combine(ObjectIdentifier(self)) }
}
