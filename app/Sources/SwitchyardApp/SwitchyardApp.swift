import SwiftUI
import SwitchyardKit

@main
struct SwitchyardApp: App {
    var body: some Scene {
        MenuBarExtra("Switchyard", systemImage: "point.3.connected.trianglepath.dotted") {
            VStack(alignment: .leading, spacing: 8) {
                Text("Switchyard")
                    .font(.headline)
                Text("No environments are running")
                    .foregroundStyle(.secondary)
            }
            .padding()
        }

        WindowGroup(id: "command-center") {
            VStack(alignment: .leading, spacing: 12) {
                Text("Switchyard")
                    .font(.largeTitle.bold())
                Text("Your local environments will appear here.")
                    .foregroundStyle(.secondary)
            }
            .frame(minWidth: 720, minHeight: 480, alignment: .topLeading)
            .padding(24)
        }

        Settings {
            Text("Switchyard Settings")
                .frame(width: 420, height: 240)
                .padding()
        }
    }
}
