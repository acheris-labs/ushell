import Foundation
import GameController
import Observation
import os
import SwiftUI
import USHClient
import USHCore

/// Whether a failed join is worth retrying: a lost or silent connection
/// is; anything else ends the tab.
func rejoinOutcome(after error: Error) -> RejoinOutcome {
    switch error as? ClientError {
    case .disconnected?, .timedOut?, .unreachable?: return .dropped
    case let e?: return .failed(e.description)
    case nil: return .failed(error.localizedDescription)
    }
}

@MainActor @Observable
final class Workspace {
    var rendezvous: [RendezvousState] = []
    var scanning = false
    var active = true
    var tabs: [TerminalTab] = []
    var frontID: TerminalTab.ID?
    var columnVisibility: NavigationSplitViewVisibility = .all
    var preferredColumn: NavigationSplitViewColumn = .sidebar
    var hardwareKeyboard = GCKeyboard.coalesced != nil {
        didSet { tabs.forEach { $0.applyKeyBar(hardwareKeyboard: hardwareKeyboard) } }
    }
    /// Settings > ush > Blinking Cursor.
    var blinkingCursor = UserDefaults.standard.bool(forKey: "blinkingCursor") {
        didSet { if blinkingCursor != oldValue { tabs.forEach { $0.applyCursor(blinking: blinkingCursor) } } }
    }
    @ObservationIgnored private let store = ProfileStore()

    init() {
        rendezvous = store.load().map(RendezvousState.init)
        for name in [Notification.Name.GCKeyboardDidConnect, .GCKeyboardDidDisconnect] {
            NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.hardwareKeyboard = GCKeyboard.coalesced != nil }
            }
        }
        NotificationCenter.default.addObserver(forName: UserDefaults.didChangeNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.blinkingCursor = UserDefaults.standard.bool(forKey: "blinkingCursor") }
        }
    }

    /// Adds a scanned profile; one with the same name replaces it.
    func add(_ profile: Profile) throws {
        try store.save(profile)
        if let i = rendezvous.firstIndex(where: { $0.profile.name == profile.name }) {
            forget(rendezvous[i])
            rendezvous[i] = RendezvousState(profile: profile)
        } else {
            rendezvous.append(RendezvousState(profile: profile))
            rendezvous.sort { $0.profile.name < $1.profile.name }
        }
    }

    /// Why this profile can't replace that rendezvous, or nil when it can.
    func check(_ profile: Profile, replacing rs: RendezvousState) -> String? {
        if profile.name.isEmpty || profile.host.isEmpty || profile.user.isEmpty {
            return "Name, host and user can't be empty."
        }
        if !(1...65535).contains(profile.port) {
            return "The port has to be between 1 and 65535."
        }
        if rendezvous.contains(where: { $0 !== rs && $0.profile.name == profile.name }) {
            return "Another rendezvous is already called \(profile.name)."
        }
        return nil
    }

    /// Replaces a rendezvous's details, keeping its paired seed. Its sessions
    /// close: they run over the connection that just changed. The new item is
    /// saved before the old one goes, so a failed save loses nothing.
    func update(_ rs: RendezvousState, to profile: Profile) throws {
        guard profile != rs.profile else { return }
        try store.save(profile)
        if profile.name != rs.profile.name { store.delete(name: rs.profile.name) }
        forget(rs)
        if let i = rendezvous.firstIndex(where: { $0 === rs }) {
            rendezvous[i] = RendezvousState(profile: profile)
            rendezvous.sort { $0.profile.name < $1.profile.name }
        }
    }

    func remove(_ rs: RendezvousState) {
        store.delete(name: rs.profile.name)
        forget(rs)
        rendezvous.removeAll { $0 === rs }
    }

    func refreshAll() async {
        await withTaskGroup(of: Void.self) { group in
            for rs in rendezvous { group.addTask { await rs.refresh() } }
        }
    }

    var front: TerminalTab? { tabs.first { $0.id == frontID } }

    func tab(for session: SessionInfo, in rs: RendezvousState) -> TerminalTab? {
        tabs.first { $0.profileName == rs.profile.name && $0.sessionID == session.id }
    }

    /// Switches to the session's tab, opening one if it has none.
    func open(_ session: SessionInfo, in rs: RendezvousState) {
        if let existing = tab(for: session, in: rs) { return bringToFront(existing) }
        let tab = TerminalTab(profileName: rs.profile.name, session: session, workspace: self)
        tabs.append(tab)
        bringToFront(tab)
        Task { await attach(tab, via: rs) }
    }

    func bringToFront(_ tab: TerminalTab) {
        frontID = tab.id
        tab.marks.cameToFront()
        columnVisibility = .detailOnly
        preferredColumn = .detail
    }

    func close(_ tab: TerminalTab) {
        tab.close()
        guard let i = tabs.firstIndex(where: { $0 === tab }) else { return }
        tabs.remove(at: i)
        if frontID == tab.id {
            if let next = tabs.indices.contains(i) ? tabs[i] : tabs.last { bringToFront(next) }
            else { frontID = nil; showTree() }
        }
    }

    func select(index: Int) {
        if tabs.indices.contains(index) { bringToFront(tabs[index]) }
    }

    func cycle(by step: Int) {
        guard !tabs.isEmpty else { return }
        let i = tabs.firstIndex { $0.id == frontID } ?? 0
        bringToFront(tabs[(i + step + tabs.count) % tabs.count])
    }

    func showTree() {
        columnVisibility = .all
        preferredColumn = .sidebar
    }

    /// Joins `tab`'s session. A lost connection leaves the tab waiting for
    /// recovery; any other failure ends it.
    @discardableResult
    func attach(_ tab: TerminalTab, via rs: RendezvousState) async -> RejoinOutcome {
        tab.phase = .connecting
        do {
            try await tab.attach(rs.connection())
            return .joined
        } catch is CancellationError {
            return .joined // the tab was closed meanwhile; nothing is waiting
        } catch {
            let outcome = rejoinOutcome(after: error)
            switch outcome {
            case .dropped:
                tab.phase = .reconnecting
                recover(rs)
            case .failed(let why):
                tab.phase = .ended(why)
            case .joined:
                break
            }
            return outcome
        }
    }

    /// A tab's join finished. `code` is nil when the connection dropped.
    func tabExited(_ tab: TerminalTab, code: Int?) {
        guard let code else {
            tab.phase = .reconnecting
            if let rs = rendezvous.first(where: { $0.profile.name == tab.profileName }) { recover(rs) }
            return
        }
        finish(tab, joinEnd(exitCode: code))
    }

    /// A session that is over takes its tab with it; a failure stays to say why.
    private func finish(_ tab: TerminalTab, _ end: JoinEnd) {
        switch end {
        case .close: close(tab)
        case .ended(let why): tab.phase = .ended(why)
        }
    }

    /// Reconnects a rendezvous whose tabs lost their channels, with backoff,
    /// then rejoins the ones still listed. Only while the app is in front:
    /// iOS suspends it otherwise.
    func recover(_ rs: RendezvousState) {
        guard active, rs.recovery == nil else { return }
        let reconnector = Reconnector<TerminalTab>(
            waiting: { [weak self] in self?.reconnecting(rs) ?? [] },
            sessionID: { $0.sessionID },
            refresh: {
                await rs.refresh()
                switch rs.status {
                case .online: return .online(listed: Set(rs.sessions.map(\.id)))
                case .refused(let why): return .refused(why)
                default: return .failed
                }
            },
            rejoin: { [weak self] tab in await self?.attach(tab, via: rs) ?? .failed("closed") },
            end: { [weak self] tab, end in self?.finish(tab, end) },
            sleep: { try await Task.sleep(for: .seconds($0)) })
        rs.recovery = Task {
            await reconnector.run()
            rs.recovery = nil
        }
    }

    private func reconnecting(_ rs: RendezvousState) -> [TerminalTab] {
        tabs.filter { $0.profileName == rs.profile.name && $0.phase == .reconnecting }
    }

    func sceneBecameActive() {
        active = true
        for rs in rendezvous where !reconnecting(rs).isEmpty { recover(rs) }
    }

    func sceneLeftForeground() {
        active = false
        for rs in rendezvous {
            rs.recovery?.cancel()
            rs.recovery = nil
        }
    }

    /// Drops a rendezvous's tabs and connection.
    func forget(_ rs: RendezvousState) {
        tabs.filter { $0.profileName == rs.profile.name }.forEach(close)
        rs.disconnect()
    }

    /// UI tests hand the app a profile instead of a QR code. It is the only
    /// one: a rendezvous left in the simulator's Keychain from earlier would
    /// list the same sessions again.
    func addTestProfileIfAsked() {
        #if DEBUG
        guard let text = ProcessInfo.processInfo.environment["USH_TEST_PAYLOAD"] else { return }
        rendezvous.forEach(remove)
        do {
            try add(parsePayload(text))
        } catch {
            Logger(subsystem: "net.acheris.ush", category: "profiles")
                .error("test profile not added: \(String(describing: error), privacy: .public)")
        }
        #endif
    }
}
