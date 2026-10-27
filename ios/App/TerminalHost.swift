import SwiftUI
import SwiftTerm

/// Shows a tab's TerminalView. Each tab keeps its own view, so switching tabs
/// keeps scrollback and state.
struct TerminalHost: UIViewRepresentable {
    let tab: TerminalTab

    func makeUIView(context: Context) -> UIView { UIView() }

    func updateUIView(_ container: UIView, context: Context) {
        let view = tab.terminal
        if view.superview !== container {
            container.subviews.forEach { $0.removeFromSuperview() }
            view.frame = container.bounds
            view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
            container.addSubview(view)
        }
    }
}
