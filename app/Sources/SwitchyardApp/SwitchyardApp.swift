import SwiftUI
import SwitchyardKit

@main
struct SwitchyardApp: App {
    @State private var model: AppModel

    init() {
        let model = AppModel(configuration: .resolve())
        model.startPolling()
        _model = State(initialValue: model)
    }

    var body: some Scene {
        MenuBarExtra {
            MenuBarSummaryView(model: model)
        } label: {
            Label("Switchyard", systemImage: "point.3.connected.trianglepath.dotted")
        }
        .menuBarExtraStyle(.window)

        Window("Switchyard", id: "command-center") {
            CommandCenterView(model: model)
                .task { model.startPolling() }
        }
        .defaultSize(width: 1100, height: 700)

        Settings {
            SettingsView(model: model)
        }
    }
}
