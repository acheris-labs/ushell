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
            .overlay { TabFlash() }
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

/// Names the tab ⌘N picked for a moment: with no tab bar, nothing else says
/// which session that number was.
private struct TabFlash: View {
    @Environment(Workspace.self) private var workspace
    /// UI tests stretch it: the simulator takes longer than a second just to
    /// report the key press.
    private let flashSeconds = Double(ProcessInfo.processInfo.environment["USH_TEST_FLASH_SECONDS"] ?? "") ?? 1

    var body: some View {
        Group {
            if let flash = workspace.flashed, let tab = workspace.tabs.first(where: { $0.id == flash.tabID }) {
                let session = workspace.session(for: tab)
                VStack(spacing: 4) {
                    Text("⌘\(workspace.shortcut(for: tab).map(String.init) ?? "")  \(session?.name ?? tab.title)")
                        .font(.title2).bold()
                    if let summary = session?.summary, !summary.isEmpty {
                        Text(summary).font(.body).multilineTextAlignment(.center)
                    }
                    Text(tab.machine).font(.caption).foregroundStyle(machineColor(tab.machine))
                }
                .padding(.horizontal, 24).padding(.vertical, 16)
                .background(.regularMaterial, in: .rect(cornerRadius: 16))
                .padding(32)
                .transition(.opacity)
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("flash")
            }
        }
        .animation(.easeOut(duration: 0.2), value: workspace.flashed)
        .allowsHitTesting(false)
        // A new press changes the id, which cancels this wait and starts
        // another, so the flash always lasts its full time from the last press.
        .task(id: workspace.flashed) {
            guard let flash = workspace.flashed else { return }
            guard (try? await Task.sleep(for: .seconds(flashSeconds))) != nil else { return }
            if workspace.flashed == flash { workspace.flashed = nil }
        }
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
