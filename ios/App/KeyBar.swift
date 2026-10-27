import SwiftTerm
import UIKit

/// The row above the on-screen keyboard: the keys a phone keyboard lacks, and
/// Ctrl-] for the session panel, and a key that hides the keyboard.
final class KeyBar: UIInputView {
    private weak var terminal: TerminalView?
    private let ctrl = UIButton(type: .system)
    private var resetObserver: NSObjectProtocol?

    init(terminal: TerminalView) {
        self.terminal = terminal
        super.init(frame: CGRect(x: 0, y: 0, width: 0, height: 44), inputViewStyle: .keyboard)
        allowsSelfSizing = true

        let app = { [weak terminal] in terminal?.getTerminal().applicationCursor ?? false }
        let keys: [(String, String, () -> [UInt8])] = [
            ("esc", "key.esc", { [0x1b] }),
            ("tab", "key.tab", { [0x09] }),
            ("←", "key.left", { app() ? EscapeSequences.moveLeftApp : EscapeSequences.moveLeftNormal }),
            ("↓", "key.down", { app() ? EscapeSequences.moveDownApp : EscapeSequences.moveDownNormal }),
            ("↑", "key.up", { app() ? EscapeSequences.moveUpApp : EscapeSequences.moveUpNormal }),
            ("→", "key.right", { app() ? EscapeSequences.moveRightApp : EscapeSequences.moveRightNormal }),
            ("^]", "key.detach", { [0x1d] }),
        ]
        var buttons: [UIView] = []
        let esc = keys[0]
        buttons.append(button(esc.0, esc.1) { [weak self] in self?.send(esc.2()) })
        ctrl.setTitle("ctrl", for: .normal)
        ctrl.accessibilityIdentifier = "key.ctrl"
        ctrl.addAction(UIAction { [weak self] _ in self?.toggleControl() }, for: .touchUpInside)
        buttons.append(ctrl)
        for key in keys.dropFirst() {
            buttons.append(button(key.0, key.1) { [weak self] in self?.send(key.2()) })
        }
        // Tapping the terminal brings the keyboard back.
        let hide = button("", "key.hide") { [weak terminal] in _ = terminal?.resignFirstResponder() }
        hide.setImage(UIImage(systemName: "keyboard.chevron.compact.down"), for: .normal)
        hide.accessibilityLabel = "Hide keyboard"
        buttons.append(hide)
        let stack = UIStackView(arrangedSubviews: buttons)
        stack.distribution = .fillEqually
        stack.translatesAutoresizingMaskIntoConstraints = false
        addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: safeAreaLayoutGuide.leadingAnchor, constant: 4),
            stack.trailingAnchor.constraint(equalTo: safeAreaLayoutGuide.trailingAnchor, constant: -4),
            stack.topAnchor.constraint(equalTo: topAnchor),
            stack.bottomAnchor.constraint(equalTo: bottomAnchor),
            heightAnchor.constraint(equalToConstant: 44),
        ])
        // SwiftTerm clears its control modifier after the next key.
        resetObserver = NotificationCenter.default.addObserver(
            forName: Notification.Name("SwiftTerm.TerminalView.controlModifierReset"),
            object: nil, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.ctrl.isSelected = false }
            }
    }

    required init?(coder: NSCoder) { fatalError("KeyBar is built in code") }

    deinit {
        if let resetObserver { NotificationCenter.default.removeObserver(resetObserver) }
    }

    private func button(_ title: String, _ id: String, _ action: @escaping () -> Void) -> UIButton {
        let b = UIButton(type: .system)
        b.setTitle(title, for: .normal)
        b.accessibilityIdentifier = id
        b.addAction(UIAction { _ in action() }, for: .touchUpInside)
        return b
    }

    private func send(_ bytes: [UInt8]) {
        guard let terminal else { return }
        UIDevice.current.playInputClick()
        terminal.terminalDelegate?.send(source: terminal, data: bytes[...])
    }

    private func toggleControl() {
        guard let terminal else { return }
        terminal.controlModifier.toggle()
        ctrl.isSelected = terminal.controlModifier
    }
}

extension KeyBar: UIInputViewAudioFeedback {
    var enableInputClicksWhenVisible: Bool { true }
}
