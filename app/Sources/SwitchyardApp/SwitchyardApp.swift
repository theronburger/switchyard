import SwiftUI
import SwitchyardKit

@main
struct SwitchyardApp: App {
    @State private var model = AppModel()

    var body: some Scene {
        MenuBarExtra {
            MenuBarSummaryView(model: model)
        } label: {
            Label("Switchyard", systemImage: "point.3.connected.trianglepath.dotted")
        }
        .menuBarExtraStyle(.window)

        Window("Switchyard", id: "command-center") {
            CommandCenterView(model: model)
        }
        .defaultSize(width: 1100, height: 700)

        Settings {
            SettingsView(model: model)
        }
    }
}
