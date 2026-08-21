import SwiftUI
import SwitchyardKit

struct SettingsView: View {
    @Bindable var model: AppModel
    @Bindable var updates: AppUpdateController
    @AppStorage(MenuBarPreferenceKey.showAttention) private var showAttention = true
    @AppStorage(MenuBarPreferenceKey.showProcesses) private var showProcesses = false
    @AppStorage(MenuBarPreferenceKey.showMemory) private var showMemory = false

    var body: some View {
        Form {
            if model.isFixtureMode {
                Section("Development data") {
                    Picker("Fixture scenario", selection: scenarioBinding) {
                        ForEach(FixtureScenario.allCases) { scenario in
                            Text(scenario.displayName).tag(scenario)
                        }
                    }
                    Text(model.scenario.blurb)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    LabeledContent("Source", value: model.dataSourceDescription)
                    if let path = try? FixtureStatusProvider.locateCanonicalFixture().path {
                        LabeledContent("Canonical fixture") {
                            Text(path)
                                .font(.caption.monospaced())
                                .textSelection(.enabled)
                        }
                    }
                }
            } else {
                Section("Connection") {
                    LabeledContent("Source", value: model.dataSourceDescription)
                    LabeledContent("Daemon", value: model.lifecycleState.displayName)
                    Text(model.lifecycleState.summary)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Section("Menu bar indicators") {
                Toggle("Attention count", isOn: $showAttention)
                Toggle("Owned process count", isOn: $showProcesses)
                Toggle("Environment memory", isOn: $showMemory)
                Text("The Switchyard mark connects when an environment is running. Optional runtime indicators update from the same atomic daemon snapshot as the app.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .toggleStyle(.switch)
            Section("Updates") {
                LabeledContent(
                    "Installed version",
                    value: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "Development"
                )
                if updates.isAvailable {
                    if let availableVersion = updates.availableVersion {
                        LabeledContent("Available version", value: availableVersion)
                    }
                    Button(updates.buttonTitle) { updates.checkForUpdates() }
                        .disabled(!updates.canCheckForUpdates)
                    Toggle(
                        "Check automatically",
                        isOn: Binding(
                            get: { updates.automaticallyChecksForUpdates },
                            set: { updates.setAutomaticUpdateChecks($0) }
                        )
                    )
                    Text("Switchyard checks once per day and verifies every update with its dedicated Ed25519 release key before extraction.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    Text(updates.unavailableReason)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            .toggleStyle(.switch)
            Section("About") {
                LabeledContent("Contract schema", value: "v\(contractSchemaVersion)")
                if let daemon = model.snapshot?.daemon {
                    LabeledContent("Daemon version", value: daemon.version)
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .padding(.vertical, 8)
    }

    private var scenarioBinding: Binding<FixtureScenario> {
        Binding(
            get: { model.scenario },
            set: { scenario in
                Task { await model.select(scenario: scenario) }
            }
        )
    }
}
