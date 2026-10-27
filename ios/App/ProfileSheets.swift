import CoreImage
import CoreImage.CIFilterBuiltins
import LocalAuthentication
import SwiftUI
import USHCore

/// Editing a rendezvous: its address and who it connects as. The seed stays as
/// it was paired — a different one is a different rendezvous, so that means
/// scanning again.
struct EditProfileSheet: View {
    let rendezvous: RendezvousState
    @Environment(Workspace.self) private var workspace
    @Environment(\.dismiss) private var dismiss
    @State private var name: String
    @State private var host: String
    @State private var port: String
    @State private var user: String
    @State private var problem: String?

    init(rendezvous: RendezvousState) {
        self.rendezvous = rendezvous
        _name = State(initialValue: rendezvous.profile.name)
        _host = State(initialValue: rendezvous.profile.host)
        _port = State(initialValue: String(rendezvous.profile.port))
        _user = State(initialValue: rendezvous.profile.user)
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("Name") {
                        TextField("home", text: $name).multilineTextAlignment(.trailing)
                            .accessibilityIdentifier("edit.name")
                    }
                    LabeledContent("Host") {
                        TextField("192.168.10.5", text: $host)
                            .multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .accessibilityIdentifier("edit.host")
                    }
                    LabeledContent("Port") {
                        TextField("7874", text: $port)
                            .multilineTextAlignment(.trailing)
                            .keyboardType(.numberPad)
                            .accessibilityIdentifier("edit.port")
                    }
                    LabeledContent("User") {
                        TextField("ush", text: $user)
                            .multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .accessibilityIdentifier("edit.user")
                    }
                } footer: {
                    Text("The paired key is kept. Saving closes this rendezvous's open "
                         + "sessions, since they run over the old connection.")
                }
                if let problem {
                    Section { Label(problem, systemImage: "exclamationmark.triangle").foregroundStyle(.red) }
                }
            }
            .navigationTitle("Edit rendezvous")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { save() }.accessibilityIdentifier("edit.save")
                }
            }
        }
    }

    private func save() {
        let trimmed = Profile(
            name: name.trimmingCharacters(in: .whitespaces),
            host: host.trimmingCharacters(in: .whitespaces),
            port: Int(port.trimmingCharacters(in: .whitespaces)) ?? 0,
            user: user.trimmingCharacters(in: .whitespaces),
            seed: rendezvous.profile.seed
        )
        switch workspace.check(trimmed, replacing: rendezvous) {
        case let .some(why):
            problem = why
        case nil:
            do {
                try workspace.update(rendezvous, to: trimmed)
                dismiss()
            } catch {
                problem = "Couldn't save it to the Keychain: \(error.localizedDescription)"
            }
        }
    }
}

/// Whether the pairing code may be shown, and if not, why not.
enum PairingCodeUnlock: Equatable {
    case allowed
    case cancelled
    case refused(String)
}

/// The pairing code is the key to every session, so showing it takes the
/// owner's face or finger, not just an unlocked phone. There is deliberately
/// no passcode fallback: someone who has watched the passcode being typed
/// still does not get the key.
@MainActor
func unlockPairingCode(for name: String) async -> PairingCodeUnlock {
    #if DEBUG
    // The simulator has no face to offer; UI tests opt out.
    if ProcessInfo.processInfo.environment["USH_SKIP_AUTH"] == "1" { return .allowed }
    #endif
    let context = LAContext()
    context.localizedFallbackTitle = "" // hides "Enter Password"
    var error: NSError?
    guard context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error) else {
        return .refused(unlockProblem(error))
    }
    do {
        try await context.evaluatePolicy(.deviceOwnerAuthenticationWithBiometrics,
                                         localizedReason: "Show the pairing code for \(name)")
        return .allowed
    } catch let e as LAError where [.userCancel, .appCancel, .systemCancel, .userFallback].contains(e.code) {
        return .cancelled
    } catch {
        return .refused(unlockProblem(error))
    }
}

private func unlockProblem(_ error: Error?) -> String {
    switch (error as? LAError)?.code {
    case .biometryNotEnrolled, .biometryNotAvailable, .passcodeNotSet:
        return "Set up Face ID or Touch ID in Settings to show pairing codes."
    case .biometryLockout:
        return "Face ID is locked after too many tries. Lock the phone and unlock it with its passcode, then try again."
    default:
        return error?.localizedDescription ?? "Face ID didn't recognize you."
    }
}

/// The rendezvous as a QR code, the same payload `ush pair` shows, for pairing
/// another device from this one.
struct QRCodeSheet: View {
    let profile: Profile
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            VStack(spacing: 20) {
                if let image = qrImage(for: payloadText(profile)) {
                    Image(uiImage: image)
                        .interpolation(.none)
                        .resizable()
                        .scaledToFit()
                        .frame(maxWidth: 320, maxHeight: 320)
                        .padding()
                        .background(.white, in: RoundedRectangle(cornerRadius: 12))
                        .accessibilityIdentifier("qr.image")
                } else {
                    ContentUnavailableView("Couldn't draw the code", systemImage: "qrcode")
                }
                Text(verbatim: "\(profile.user)@\(profile.host):\(profile.port)")
                    .font(.callout.monospaced())
                    .foregroundStyle(.secondary)
                Label("Whoever scans this gets the key to every session on this rendezvous.",
                      systemImage: "exclamationmark.triangle")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .padding(.horizontal)
                Spacer()
            }
            .padding(.top, 20)
            .navigationTitle(profile.name)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
        }
    }

    /// Renders text as a QR code, scaled up: the generator's output is one
    /// pixel per module, which would be a blur on screen.
    private func qrImage(for text: String) -> UIImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }
        let scaled = output.transformed(by: CGAffineTransform(scaleX: 12, y: 12))
        let context = CIContext()
        guard let cgImage = context.createCGImage(scaled, from: scaled.extent) else { return nil }
        return UIImage(cgImage: cgImage)
    }
}
