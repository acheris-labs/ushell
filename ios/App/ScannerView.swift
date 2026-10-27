import SwiftUI
import USHCore
import VisionKit

/// Scans a `ush pair` code. Anything else gets a message and scanning goes on.
struct ScannerSheet: View {
    @Environment(Workspace.self) private var workspace
    @Environment(\.dismiss) private var dismiss
    @State private var message = "Point the camera at the code from ush pair."

    var body: some View {
        NavigationStack {
            Group {
                if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
                    QRScanner { text in handle(text) }
                        .ignoresSafeArea()
                        .overlay(alignment: .bottom) {
                            Text(message).padding().background(.thinMaterial, in: .rect(cornerRadius: 12)).padding()
                        }
                } else {
                    ContentUnavailableView("No camera", systemImage: "camera",
                                           description: Text("Scanning needs a camera the app is allowed to use."))
                }
            }
            .navigationTitle("Add rendezvous")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { Button("Cancel") { dismiss() } }
        }
    }

    private func handle(_ text: String) {
        do {
            try workspace.add(parsePayload(text))
            dismiss()
        } catch PayloadError.notUSH {
            message = "That isn't a ush pairing code."
        } catch PayloadError.newerVersion {
            message = "That code is from a newer ush. Update the app."
        } catch {
            message = "That code is damaged or incomplete. Run ush pair again."
        }
    }
}

private struct QRScanner: UIViewControllerRepresentable {
    let found: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])],
                                           qualityLevel: .balanced, isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {
        if !vc.isScanning { try? vc.startScanning() }
    }

    func makeCoordinator() -> Coordinator { Coordinator(found: found) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let found: (String) -> Void
        init(found: @escaping (String) -> Void) { self.found = found }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for case .barcode(let code) in items {
                if let text = code.payloadStringValue { found(text) }
            }
        }
    }
}
