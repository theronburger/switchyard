import SwiftUI
import SwitchyardKit

enum MenuBarPreferenceKey {
    static let showAttention = "menuBar.showAttention"
    static let showProcesses = "menuBar.showProcesses"
    static let showMemory = "menuBar.showMemory"
}

struct MenuBarStatusLabel: View {
    @Bindable var model: AppModel
    @AppStorage(MenuBarPreferenceKey.showAttention) private var showAttention = true
    @AppStorage(MenuBarPreferenceKey.showProcesses) private var showProcesses = false
    @AppStorage(MenuBarPreferenceKey.showMemory) private var showMemory = false

    var body: some View {
        HStack(spacing: 5) {
            SwitchyardBrandMark(state: markState)
                .frame(width: 18, height: 18)
            if let snapshot = model.snapshot, let summary = model.summary {
                if showAttention, summary.attentionCount > 0 {
                    indicator("\(summary.attentionCount)", symbol: "exclamationmark.triangle.fill")
                }
                if showProcesses {
                    indicator("\(snapshot.environments.reduce(0) { $0 + $1.totalProcessCount })", symbol: "cpu")
                }
                if showMemory {
                    Text(Format.memory(summary.totalMemoryBytes))
                        .monospacedDigit()
                }
            }
        }
        .font(.caption2.weight(.medium))
        .accessibilityLabel("Switchyard status")
        .onChange(of: markState, initial: true) { _, state in
            SwitchyardDockIcon.apply(state: state)
        }
    }

    var markState: SwitchyardBrandMarkState {
        (model.summary?.runningCount ?? 0) > 0 ? .running : .idle
    }

    private func indicator(_ value: String, symbol: String) -> some View {
        HStack(spacing: 2) {
            Image(systemName: symbol)
            Text(value).monospacedDigit()
        }
    }
}
