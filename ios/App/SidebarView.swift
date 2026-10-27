import SwiftUI
import USHCore

struct SidebarView: View {
    @Environment(Workspace.self) private var workspace
    @Environment(\.scenePhase) private var scenePhase
    @State private var editing: RendezvousState?
    @State private var showing: RendezvousState?
    @State private var removing: RendezvousState?
    @State private var refusal: String?

    var body: some View {
        List {
            ForEach(workspace.rendezvous) { rs in
                @Bindable var rs = rs
                DisclosureGroup(isExpanded: $rs.expanded) {
                    StatusRow(status: rs.status) { Task { await rs.refresh() } }
                    ForEach(byMachine(rs.sessions), id: \.machine) { group in
                        Text(group.machine)
                            .font(.caption).foregroundStyle(machineColor(group.machine))
                            .accessibilityIdentifier("machine.\(group.machine)")
                        ForEach(group.sessions) { s in
                            SessionRow(rendezvous: rs, session: s)
                        }
                    }
                } label: {
                    HStack {
                        Label(rs.profile.name, systemImage: "server.rack")
                            .accessibilityIdentifier("rendezvous.\(rs.profile.name)")
                        Spacer()
                        RendezvousActions(name: rs.profile.name,
                                          edit: { editing = rs },
                                          show: { Task { await reveal(rs) } },
                                          remove: { removing = rs })
                    }
                }
                .swipeActions {
                    Button("Remove", role: .destructive) { removing = rs }
                    Button("Edit") { editing = rs }.tint(.blue)
                }
                // Each rendezvous polls on its own, so a slow one never
                // holds up the rest.
                .task(id: rs.id) {
                    while !Task.isCancelled {
                        if rs.pollsAutomatically { await rs.refresh() }
                        try? await Task.sleep(for: .seconds(3))
                    }
                }
            }
        }
        .overlay {
            if workspace.rendezvous.isEmpty {
                ContentUnavailableView("No rendezvous", systemImage: "qrcode.viewfinder",
                                       description: Text("Run ush pair on a computer, then tap + to scan it."))
            }
        }
        .refreshable { await workspace.refreshAll() }
        .sheet(item: $editing) { EditProfileSheet(rendezvous: $0) }
        .sheet(item: $showing) { QRCodeSheet(profile: $0.profile) }
        // Gone before iOS takes the app switcher snapshot, and it takes Face
        // ID again to bring back. Inactive rather than background, so the
        // sheet has time to leave before the snapshot is taken.
        .onChange(of: scenePhase) { _, phase in
            if phase != .active { showing = nil }
        }
        .alert("Remove \(removing?.profile.name ?? "")?",
               isPresented: .init(get: { removing != nil },
                                  set: { if !$0 { removing = nil } })) {
            Button("Remove", role: .destructive) {
                if let rs = removing { workspace.remove(rs) }
                removing = nil
            }
            Button("Cancel", role: .cancel) { removing = nil }
        } message: {
            Text("Its key is deleted from this device. Pair again to bring it back.")
        }
        .alert("Can't show the pairing code",
               isPresented: .init(get: { refusal != nil },
                                  set: { if !$0 { refusal = nil } })) {
            Button("OK", role: .cancel) { refusal = nil }
        } message: {
            Text(refusal ?? "")
        }
        .toolbar {
            Button { workspace.scanning = true } label: { Image(systemName: "plus") }
                .accessibilityIdentifier("scan")
        }
        .navigationTitle("ush")
    }

    private func reveal(_ rs: RendezvousState) async {
        switch await unlockPairingCode(for: rs.profile.name) {
        case .allowed: showing = rs
        case .cancelled: break
        case .refused(let why): refusal = why
        }
    }
}

/// Edit, show the pairing code, remove. Borderless so a tap lands on the
/// button rather than folding the rendezvous away.
private struct RendezvousActions: View {
    let name: String
    let edit: () -> Void
    let show: () -> Void
    let remove: () -> Void

    var body: some View {
        HStack(spacing: 14) {
            button("pencil", "Edit \(name)", "edit.\(name)", edit)
            button("eye", "Show \(name)'s pairing code", "qr.\(name)", show)
            button("trash", "Remove \(name)", "remove.\(name)", remove)
        }
        .foregroundStyle(.secondary)
        .imageScale(.medium)
    }

    private func button(_ symbol: String, _ label: String, _ id: String,
                        _ action: @escaping () -> Void) -> some View {
        Button(action: action) { Image(systemName: symbol) }
            .buttonStyle(.borderless)
            .accessibilityLabel(label)
            .accessibilityIdentifier(id)
    }
}

private struct StatusRow: View {
    let status: RendezvousState.Status
    let retry: () -> Void

    var body: some View {
        switch status {
        case .idle, .online:
            EmptyView()
        case .stale(let why):
            Label("stale: \(why)", systemImage: "exclamationmark.triangle").foregroundStyle(.secondary).font(.caption)
        case .unreachable(let why):
            Button(action: retry) {
                Label("\(why) — tap to retry", systemImage: "wifi.exclamationmark").font(.caption)
            }
        case .refused(let why):
            Label(why, systemImage: "lock.trianglebadge.exclamationmark").foregroundStyle(.red).font(.caption)
        }
    }
}

struct SessionRow: View {
    @Environment(Workspace.self) private var workspace
    let rendezvous: RendezvousState
    let session: SessionInfo

    private var tab: TerminalTab? { workspace.tab(for: session, in: rendezvous) }
    private var front: Bool { tab.map { $0.id == workspace.frontID } ?? false }

    var body: some View {
        Button {
            workspace.open(session, in: rendezvous)
        } label: {
            HStack {
                Image(systemName: tab == nil ? "circle" : "circle.inset.filled")
                VStack(alignment: .leading) {
                    Text(session.name).bold()
                    Text(session.command).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                if tab?.marks.bell == true { Image(systemName: "bell.fill").foregroundStyle(.orange) }
                else if tab?.marks.activity == true { Image(systemName: "circle.fill").font(.system(size: 6)) }
                if let n = session.viewers, n > 0 {
                    Label("\(n)", systemImage: "eye")
                        .labelStyle(.titleAndIcon).font(.caption).monospacedDigit().foregroundStyle(.secondary)
                        .accessibilityLabel("\(n) watching")
                        .accessibilityIdentifier("viewers.\(session.name)")
                }
                Text(session.age(now: .now)).font(.caption).monospacedDigit().foregroundStyle(.secondary)
            }
        }
        .opacity(rendezvous.status.isStale ? 0.5 : 1)
        // The session on screen wears its machine's colour.
        .listRowBackground(front ? machineColor(session.machine).opacity(0.25) : nil)
        .accessibilityIdentifier("session.\(session.name)")
        .accessibilityValue(front ? "front" : tab != nil ? "joined" : "")
        // With no tabs, this is where a session closes.
        .swipeActions {
            if let tab {
                Button("Close", role: .destructive) { workspace.close(tab) }
            }
        }
    }
}

extension RendezvousState.Status {
    var isStale: Bool { if case .stale = self { return true } else { return false } }
}

/// The colour a machine's header in the tree and its session on screen
/// share. No red, orange or yellow: those already mean an error, a bell, or
/// nothing legible.
func machineColor(_ machine: String) -> Color {
    let palette: [Color] = [.blue, .green, .purple, .teal, .pink, .indigo, .mint, .brown]
    return palette[colorIndex(machine: machine, count: palette.count)]
}
