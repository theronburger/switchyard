import AppKit
import SwiftUI

extension Notification.Name {
    static let switchyardOpenCommandCenter = Notification.Name("switchyard.open-command-center")
}

@MainActor
enum CommandCenterWindowPresenter {
    static func open(using openWindow: OpenWindowAction) {
        openWindow(id: "command-center")
        presentWhenAvailable()
    }

    static func presentWhenAvailable() {
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)
        DispatchQueue.main.async {
            NSApplication.shared.activate(ignoringOtherApps: true)
            NSApplication.shared.windows
                .filter(\.canBecomeKey)
                .forEach { window in
                    window.makeKeyAndOrderFront(nil)
                    window.orderFrontRegardless()
                }
        }
    }
}
