import AppKit
import SwiftUI
import SwitchyardKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApplication.shared.setActivationPolicy(.regular)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            NotificationCenter.default.post(name: .switchyardOpenCommandCenter, object: nil)
        }
        sender.activate(ignoringOtherApps: true)
        return true
    }
}

@main
struct SwitchyardApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var model: AppModel

    init() {
        let model = AppModel(configuration: .resolve())
        model.startPolling()
        _model = State(initialValue: model)
    }

    var body: some Scene {
        Window("Switchyard", id: "command-center") {
            CommandCenterView(model: model)
                .task { model.startPolling() }
        }
        .defaultSize(width: CommandCenterLayout.defaultWidth, height: CommandCenterLayout.defaultHeight)
        .windowResizability(.contentMinSize)

        MenuBarExtra {
            MenuBarSummaryView(model: model)
        } label: {
            MenuBarStatusLabel(model: model)
        }
        .menuBarExtraStyle(.window)

        Settings {
            SettingsView(model: model)
        }
    }
}
