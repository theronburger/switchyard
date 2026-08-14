import SwiftUI
import SwitchyardKit

struct SettingsView: View {
    @Bindable var model: AppModel

    var body: some View {
        Form {
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
