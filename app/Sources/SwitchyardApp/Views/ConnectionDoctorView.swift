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
                agentConnectionsCard
                githubCard
                checksCard
            }
            .padding(20)
            .frame(maxWidth: 640, alignment: .leading)
            .switchyardScrollbars()
        }
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }

    private var githubCard: some View {
        SectionCard(title: "GitHub CLI", systemImage: "arrow.triangle.branch") {
            let observations = model.snapshot?.repositories.flatMap(\.worktrees).compactMap(\.pullRequest) ?? []
            let accounts = Array(Set(observations.compactMap(\.account))).sorted()
            if observations.isEmpty {
                Text("Waiting for the daemon's first GitHub observation.")
                    .foregroundStyle(.secondary)
            } else {
                HStack(spacing: 10) {
                    Image(systemName: observations.contains(where: { $0.status == .unavailable }) ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                        .foregroundStyle(observations.contains(where: { $0.status == .unavailable }) ? .orange : .green)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(accounts.isEmpty ? "GitHub observation active" : "Authenticated as \(accounts.joined(separator: ", "))")
                            .font(.callout.weight(.medium))
                        Text("\(observations.count(where: { $0.status == .found })) pull requests · \(observations.count(where: { $0.status == .none })) branches without a PR · \(observations.count(where: { $0.stale })) stale")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                }
                if let unavailable = observations.first(where: { $0.status == .unavailable }) {
                    Divider()
                    Text(githubErrorDescription(unavailable.errorCode))
                        .font(.caption)
                        .foregroundStyle(.orange)
                }
            }
            Text("Switchyard uses the existing Keychain-backed gh login. It never stores or requests a GitHub token, and GitHub availability does not affect environment health.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
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
                .disabled(!model.canRepairAllConnections)
                Button("Run Checks Again") {
                    Task { await model.runConnectionChecks() }
                }
                Spacer()
                if model.lifecycleState == .repairing {
                    ProgressView()
                        .controlSize(.small)
                }
            }
        }
    }

    private var agentConnectionsCard: some View {
        SectionCard(title: "Agent connections", systemImage: "point.3.connected.trianglepath.dotted") {
            if let report = model.agentConnectionReport {
                ForEach(report.statuses) { status in
                    HStack(alignment: .top, spacing: 10) {
                        agentStatusIcon(status.state)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(status.host.displayName)
                                .font(.callout.weight(.medium))
                            Text(status.detail)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        Spacer()
                        if status.state.canRepair {
                            Button(model.repairingAgentHosts.contains(status.host) ? "Repairing…" : "Repair") {
                                Task { await model.repairAgentConnection(status.host) }
                            }
                            .controlSize(.small)
                            .disabled(model.repairingAgentHosts.contains(status.host))
                        }
                    }
                    if status.id != report.statuses.last?.id { Divider() }
                }
            } else if model.isFixtureMode {
                Text("Agent connection checks are unavailable in fixture mode.")
                    .foregroundStyle(.secondary)
            } else {
                ProgressView("Inspecting Codex and Claude Code…")
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

    @ViewBuilder
    private func agentStatusIcon(_ state: AgentConnectionState) -> some View {
        switch state {
        case .connected:
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case .missing, .needsRepair:
            Image(systemName: "wrench.and.screwdriver.fill").foregroundStyle(.orange)
        case .unavailable:
            Image(systemName: "minus.circle").foregroundStyle(.secondary)
        case .refused:
            Image(systemName: "hand.raised.fill").foregroundStyle(.red)
        }
    }
}
