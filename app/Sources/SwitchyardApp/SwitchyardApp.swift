import AppKit
import SwiftUI

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
    }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        sender.windows.first(where: { $0.title == "Switchyard" })?.makeKeyAndOrderFront(nil)
        sender.activate(ignoringOtherApps: true)
        return true
    }
}

@main struct SwitchyardApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model: WorkspaceModel
    @State private var updates = AppUpdateController()
    @Environment(\.openWindow) private var openWindow

    init() {
        let installation = AppInstallation.resolve()
        _model = State(initialValue: WorkspaceModel(api: WorkspaceClient(root: installation.root), installation: installation, preferences: .standard))
    }
    var body: some Scene {
        Window("Switchyard", id: "command-center") {
            WorkspaceWindow(model: model).task { model.start(); updates.start() }
                .onChange(of: model.runningCount, initial: true) { _, count in
                    NSApp.applicationIconImage = SwitchyardDockIcon.image(for: count > 0 ? .running : .idle)
                }
        }
        .defaultSize(width: 1320, height: 820)
        .windowResizability(.contentMinSize)
        .commands {
            CommandGroup(after: .appInfo) { Button(updates.buttonTitle) { updates.checkForUpdates() }.disabled(!updates.canCheckForUpdates) }
        }
        MenuBarExtra {
            Button("Open Switchyard") { openWindow(id: "command-center"); NSApp.activate(ignoringOtherApps: true) }
            Divider()
            ForEach(model.workspaces.filter { model.run(for: $0)?.isActive == true }) { workspace in
                Button(workspace.displayTitle) { model.selection = workspace.path; openWindow(id: "command-center"); NSApp.activate(ignoringOtherApps: true) }
            }
            Text("\(model.runningCount) running")
            Divider()
            Button("Quit Switchyard") { NSApp.terminate(nil) }
        } label: {
            Image(nsImage: SwitchyardBrandIcon.image(for: model.runningCount > 0 ? .running : .idle))
        }
        Settings {
            Form {
                Text("Switchyard").font(.title2.bold())
                Toggle("Check for updates automatically", isOn: Binding(get: { updates.automaticallyChecksForUpdates }, set: { updates.setAutomaticUpdateChecks($0) }))
                    .disabled(!updates.isAvailable)
                Button(updates.buttonTitle) { updates.checkForUpdates() }.disabled(!updates.canCheckForUpdates)
                if !updates.isAvailable { Text(updates.unavailableReason).font(.caption).foregroundStyle(.secondary) }
            }.padding(24).frame(width: 440)
        }
    }
}
