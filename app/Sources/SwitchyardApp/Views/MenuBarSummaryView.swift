import SwiftUI
import SwitchyardKit

/// Compact summary shown from the menu bar extra.
struct MenuBarSummaryView: View {
    @Bindable var model: AppModel
    @SwiftUI.Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            header
            Divider()
            content
            Divider()
            footer
        }
        .padding(12)
        .frame(width: 320)
        .task { model.startPolling() }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: model.lifecycleState.systemImage)
                .foregroundStyle(model.lifecycleState.tint)
            VStack(alignment: .leading, spacing: 1) {
                Text("Switchyard")
                    .font(.headline)
                Text(model.lifecycleState.displayName)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if let summary = model.summary, summary.attentionCount > 0 {
                Label("\(summary.attentionCount)", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.orange)
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .idle, .loading:
            HStack {
                ProgressView()
                    .controlSize(.small)
                Text("Loading status…")
                    .foregroundStyle(.secondary)
            }
        case .failed(let message):
            VStack(alignment: .leading, spacing: 6) {
                Label("Daemon unavailable", systemImage: "bolt.horizontal.circle")
                    .foregroundStyle(.orange)
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
            }
        case .empty:
            Text("No environments yet. Open Switchyard to get started.")
                .font(.callout)
                .foregroundStyle(.secondary)
        case .loaded:
            environmentList
        }
    }

    @ViewBuilder
    private var environmentList: some View {
        if let snapshot = model.snapshot {
            VStack(alignment: .leading, spacing: 8) {
                if let summary = model.summary {
                    HStack(spacing: 12) {
                        summaryStat("\(summary.runningCount)", "running")
                        summaryStat("\(summary.attentionCount)", "attention")
                        summaryStat(Format.memory(summary.totalMemoryBytes), "memory")
                    }
                    .font(.caption)
                }
                ForEach(snapshot.environments) { environment in
                    Button {
                        model.selection = .environment(environment.id)
                        openWindow(id: "command-center")
                    } label: {
                        HStack(spacing: 8) {
                            StatusDot(color: environment.health.tint)
                            VStack(alignment: .leading, spacing: 1) {
                                Text(environment.displayName)
                                    .font(.callout.weight(.medium))
                                if let branch = snapshot.worktree(for: environment)?.branch {
                                    Text(branch)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                        .lineLimit(1)
                                }
                            }
                            Spacer()
                            Text(environment.observedState.label)
                                .font(.caption)
                                .foregroundStyle(environment.observedState.tint)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    private func summaryStat(_ value: String, _ label: String) -> some View {
        HStack(spacing: 3) {
            Text(value).fontWeight(.semibold).monospacedDigit()
            Text(label).foregroundStyle(.secondary)
        }
    }

    private var footer: some View {
        HStack {
            Button("Open Switchyard") {
                openWindow(id: "command-center")
            }
            Button("Doctor") {
                model.selection = .connectionDoctor
                openWindow(id: "command-center")
            }
            if model.lifecycleState.canRepair {
                Button("Repair") {
                    Task { await model.repairAll() }
                }
            }
            Spacer()
            Button {
                Task { await model.refresh() }
            } label: {
                Image(systemName: "arrow.clockwise")
            }
            .help("Refresh")
            Button {
                NSApplication.shared.terminate(nil)
            } label: {
                Image(systemName: "power")
            }
            .help("Quit Switchyard")
        }
        .controlSize(.small)
    }
}
