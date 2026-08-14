import SwiftUI
import SwitchyardKit

/// The Connection Doctor: daemon lifecycle state, Repair All, and the
/// individual diagnostic checks.
struct ConnectionDoctorView: View {
    @Bindable var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                Text("Connection Doctor")
                    .font(.largeTitle.bold())
                lifecycleCard
                checksCard
            }
            .padding(20)
            .frame(maxWidth: 640, alignment: .leading)
        }
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }

    private var lifecycleCard: some View {
        SectionCard(title: "Daemon lifecycle", systemImage: "gearshape.2") {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: model.lifecycleState.systemImage)
                    .font(.title2)
                    .foregroundStyle(model.lifecycleState.tint)
                VStack(alignment: .leading, spacing: 4) {
                    Text(model.lifecycleState.displayName)
                        .font(.headline)
                    Text(model.lifecycleState.summary)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer()
            }
            HStack {
                Button {
                    Task { await model.repairAll() }
                } label: {
                    Label("Repair All", systemImage: "wrench.and.screwdriver")
                }
                .buttonStyle(.borderedProminent)
                .disabled(!model.lifecycleState.canRepair)
                Button("Run Checks Again") {
                    Task { await model.refresh() }
                }
                Spacer()
                if model.lifecycleState == .repairing {
                    ProgressView()
                        .controlSize(.small)
                }
            }
        }
    }

    private var checksCard: some View {
        SectionCard(title: "Checks", systemImage: "stethoscope") {
            if let report = model.doctorReport {
                Text(report.summaryLine)
                    .font(.callout.weight(.medium))
                    .foregroundStyle(report.isHealthy ? .green : .orange)
                Divider()
                ForEach(report.checks) { check in
                    HStack(alignment: .top, spacing: 10) {
                        outcomeIcon(check.outcome)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(check.title)
                                .font(.callout.weight(.medium))
                            Text(check.outcome.message)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        Spacer()
                    }
                }
            } else {
                Text("No diagnostics have run yet.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func outcomeIcon(_ outcome: DoctorCheck.Outcome) -> some View {
        switch outcome {
        case .passed:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .warning:
            Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.orange)
        case .failed:
            Image(systemName: "xmark.octagon.fill").foregroundStyle(.red)
        case .skipped:
            Image(systemName: "minus.circle").foregroundStyle(.secondary)
        }
    }
}
