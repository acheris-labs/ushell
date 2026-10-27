import SwiftUI

struct RootView: View {
    @Environment(Workspace.self) private var workspace
    @Environment(\.horizontalSizeClass) private var sizeClass

    var body: some View {
        @Bindable var workspace = workspace
        NavigationSplitView(columnVisibility: $workspace.columnVisibility,
                            preferredCompactColumn: $workspace.preferredColumn) {
            SidebarView()
        } detail: {
            Group {
                if let tab = workspace.front {
                    TerminalHost(tab: tab)
                } else {
                    ContentUnavailableView("No session open", systemImage: "terminal",
                                           description: Text("Pick one from the list."))
                }
            }
            // No tabs: the session list, a swipe away, is how to switch.
            .overlay(alignment: .top) {
                if let tab = workspace.front { PhaseBanner(phase: tab.phase) }
            }
            // The terminal gets the whole screen; the pull tab brings the
            // sessions back.
            .toolbar(.hidden, for: .navigationBar)
            .background(ShortcutKeys())
            .overlay(alignment: .leading) {
                if workspace.columnVisibility == .detailOnly || sizeClass == .compact { SidebarPullTab() }
            }
        }
        // The status bar costs the iPad a terminal row. iPhones keep it: the
        // notch or island holds that space either way.
        .statusBarHidden(UIDevice.current.userInterfaceIdiom == .pad)
        .sheet(isPresented: $workspace.scanning) { ScannerSheet() }
    }
}

/// Hardware-keyboard shortcuts. Invisible buttons are how SwiftUI registers
/// key commands on iOS; they reach the hosting controller through the
/// responder chain even while the terminal has focus.
private struct ShortcutKeys: View {
    @Environment(Workspace.self) private var workspace

    var body: some View {
        ZStack {
            ForEach(1...9, id: \.self) { n in
                Button("Tab \(n)") { workspace.select(index: n - 1) }
                    .keyboardShortcut(KeyEquivalent(Character("\(n)")), modifiers: .command)
            }
            Button("Previous tab") { workspace.cycle(by: -1) }.keyboardShortcut("[", modifiers: .command)
            Button("Next tab") { workspace.cycle(by: 1) }.keyboardShortcut("]", modifiers: .command)
            Button("Close tab") { if let t = workspace.front { workspace.close(t) } }.keyboardShortcut("w", modifiers: .command)
            Button("Sessions") { workspace.showTree() }.keyboardShortcut("o", modifiers: .command)
        }
        .opacity(0)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// A grip on the left edge that brings the session list back: tap it or
/// pull it to the right.
private struct SidebarPullTab: View {
    @Environment(Workspace.self) private var workspace

    var body: some View {
        Capsule()
            .fill(.secondary)
            .frame(width: 5, height: 48)
            .padding(.leading, 3)
            .frame(width: 28, height: 96, alignment: .leading)
            .contentShape(.rect)
            .onTapGesture { workspace.showTree() }
            .gesture(DragGesture(minimumDistance: 12).onEnded { g in
                if g.translation.width > 20 { workspace.showTree() }
            })
            .accessibilityElement()
            .accessibilityLabel("Sessions")
            .accessibilityAddTraits(.isButton)
            .accessibilityAction { workspace.showTree() }
            .accessibilityIdentifier("sessions.pull")
    }
}

private struct PhaseBanner: View {
    let phase: TerminalTab.Phase

    var body: some View {
        switch phase {
        case .live:
            EmptyView()
        case .connecting:
            ProgressView("joining…").padding(8).background(.thinMaterial, in: .capsule).padding(.top, 8)
        case .reconnecting:
            Label("reconnecting…", systemImage: "arrow.triangle.2.circlepath")
                .padding(8).background(.thinMaterial, in: .capsule).padding(.top, 8)
        case .ended(let why):
            Label(why.map { "ended: \($0)" } ?? "session ended", systemImage: "stop.circle")
                .padding(8).background(.thinMaterial, in: .capsule).padding(.top, 8)
                .accessibilityIdentifier("ended")
        }
    }
}
