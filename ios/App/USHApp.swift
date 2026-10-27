import SwiftUI

@main
struct USHApp: App {
    @State private var workspace = Workspace()
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(workspace)
                .onAppear { workspace.addTestProfileIfAsked() }
        }
        .onChange(of: scenePhase) { _, phase in
            switch phase {
            case .active: workspace.sceneBecameActive()
            case .background: workspace.sceneLeftForeground()
            default: break
            }
        }
    }
}
