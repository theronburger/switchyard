import SwiftUI
import SwitchyardKit

struct EnvironmentDetailView: View {
    @Bindable var model: AppModel
    let environment: EnvironmentModel
    let snapshot: StatusSnapshot
    @State private var expandedServiceIDs = Set<String>()
    @State private var worktreeExpanded = true
    @State private var infrastructureExpanded = true
    @State private var identifiersExpanded = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                header
                Divider()
                EnvironmentActionBanner(model: model)
                    .padding(.top, 18)
                if environment.observedState == .stopped, let worktree = snapshot.worktree(for: environment) {
                    StartEnvironmentView(model: model, snapshot: snapshot, initialWorktreeId: worktree.id)
                        .padding(.top, 18)
                }
                services
                Divider()
                worktreeAndRepository
                Divider()
                publishedURLs
                Divider()
                infrastructure
                Divider()
                activityAndAlerts
                Divider()
                identityAndSnapshot
            }
            .padding(.horizontal, 28)
            .padding(.bottom, 30)
            .frame(maxWidth: 1_180, alignment: .leading)
        }
        .frame(maxWidth: .infinity, alignment: .top)
        .onAppear {
            if expandedServiceIDs.isEmpty {
                expandedServiceIDs = Set(environment.services.map(\.id))
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: "arrow.triangle.branch")
                    .font(.title2)
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("Worktree environment")
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 9) {
                        Text(environment.displayName)
                            .font(.largeTitle.bold())
                        HealthBadge(health: environment.health)
                    }
                    Text(headerSubtitle)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                Spacer()
                stopButton
            }

            if let worktree = snapshot.worktree(for: environment) {
                HStack(spacing: 0) {
                    NativeFact(label: "Path", value: worktree.path, monospaced: true)
                    NativeFact(label: "Branch", value: worktree.branch ?? "Detached HEAD", monospaced: true)
                    NativeFact(label: "Head", value: Format.shortRevision(worktree.headRevision), monospaced: true)
                    NativeFact(label: "Role", value: worktree.isPrimary ? "Primary checkout" : "Linked worktree")
                    NativeFact(
                        label: "Git state",
                        value: worktree.git.isClean ? "Clean" : "Needs review",
                        tint: worktree.git.isClean ? .green : .orange
                    )
                }
            }

            HStack(spacing: 0) {
                NativeMetric(value: environment.desiredState.label, label: "Desired")
                NativeMetric(value: environment.observedState.label, label: "Observed", tint: environment.observedState.tint)
                NativeMetric(value: "\(environment.services.count)", label: "Services")
                NativeMetric(value: "\(environment.totalProcessCount)", label: "Processes")
                NativeMetric(value: Format.cpu(environment.resources.cpuPercent), label: "CPU")
                NativeMetric(value: Format.memory(environment.resources.memoryBytes), label: "Memory")
                NativeMetric(value: "\(environment.revision)", label: "Revision")
            }
            .padding(.vertical, 14)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .padding(.vertical, 24)
    }

    @ViewBuilder
    private var stopButton: some View {
        if environment.observedState == .running || environment.observedState == .failed {
            Button {
                Task { await model.stopEnvironment(environment) }
            } label: {
                Label(
                    model.environmentActionState.isSubmitting ? "Submitting…" : "Stop environment",
                    systemImage: "stop.fill"
                )
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            .disabled(!model.canSubmitEnvironmentAction)
        }
    }

    private var services: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text("Services")
                    .font(.title3.weight(.semibold))
                Text("\(environment.services.count)")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 7)
                    .padding(.vertical, 3)
                    .background(.quaternary, in: Capsule())
            }
            .padding(.top, 20)
            .padding(.bottom, 8)

            if environment.services.isEmpty {
                ContentUnavailableView(
                    "No services requested",
                    systemImage: "pause.circle",
                    description: Text("This environment currently owns no service runs.")
                )
                .padding(.vertical, 18)
            }

            ForEach(environment.services) { service in
                ServiceInspector(
                    service: service,
                    leases: environment.portLeases(for: service),
                    publishedURL: environment.urls[service.id],
                    expanded: serviceExpansionBinding(service.id)
                )
                Divider()
            }
        }
    }

    private var worktreeAndRepository: some View {
        DisclosureGroup(isExpanded: $worktreeExpanded) {
            if let repository = snapshot.repository(for: environment),
               let worktree = snapshot.worktree(for: environment) {
                HStack(alignment: .top, spacing: 28) {
                    VStack(alignment: .leading, spacing: 7) {
                        Text("Worktree")
                            .font(.callout.weight(.semibold))
                        KeyValueRow(key: "Path", value: worktree.path, monospaced: true)
                        KeyValueRow(key: "Branch", value: worktree.branch ?? "Detached HEAD", monospaced: true)
                        KeyValueRow(key: "Head", value: worktree.headRevision, monospaced: true)
                        KeyValueRow(key: "Role", value: worktree.isPrimary ? "Primary checkout" : "Linked worktree")
                        GitStateDetail(state: worktree.git)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    VStack(alignment: .leading, spacing: 7) {
                        Text("Repository")
                            .font(.callout.weight(.semibold))
                        KeyValueRow(key: "Name", value: repository.displayName)
                        KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true)
                        KeyValueRow(key: "Remote", value: repository.remote, monospaced: true)
                        KeyValueRow(key: "Adapter", value: repository.adapter, monospaced: true)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.top, 14)
            } else {
                Text("The reported repository or worktree is no longer available.")
                    .foregroundStyle(.secondary)
                    .padding(.top, 12)
            }
        } label: {
            Label("Worktree & repository", systemImage: "arrow.triangle.branch")
                .font(.headline)
                .padding(.vertical, 17)
        }
    }

    private var publishedURLs: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Published URLs")
                .font(.headline)
            if environment.sortedURLs.isEmpty {
                Text("No URLs published.")
                    .foregroundStyle(.secondary)
            }
            ForEach(environment.sortedURLs, id: \.service) { entry in
                HStack(spacing: 10) {
                    Image(systemName: "link")
                        .foregroundStyle(.secondary)
                    Text(entry.service)
                        .font(.callout.weight(.medium))
                    Spacer()
                    if let url = URL(string: entry.url) {
                        Link(destination: url) {
                            Label(entry.url, systemImage: "arrow.up.right.square")
                        }
                        .font(.callout.monospaced())
                    } else {
                        Text(entry.url)
                            .font(.callout.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .padding(.vertical, 18)
    }

    private var infrastructure: some View {
        DisclosureGroup(isExpanded: $infrastructureExpanded) {
            if environment.infrastructureLeases.isEmpty {
                Text("No infrastructure leases.")
                    .foregroundStyle(.secondary)
                    .padding(.top, 10)
            }
            ForEach(environment.infrastructureLeases) { lease in
                InfrastructureLeaseInspector(lease: lease)
                    .padding(.top, 12)
            }
        } label: {
            Label("Infrastructure", systemImage: "externaldrive.connected.to.line.below")
                .font(.headline)
                .padding(.vertical, 17)
        }
    }

    private var activityAndAlerts: some View {
        HStack(alignment: .top, spacing: 32) {
            VStack(alignment: .leading, spacing: 12) {
                Text("Operations")
                    .font(.headline)
                let operations = snapshot.operations(forEnvironment: environment.id)
                if operations.isEmpty {
                    Text("No recorded operations.")
                        .foregroundStyle(.secondary)
                }
                ForEach(operations) { operation in
                    OperationSummaryRow(operation: operation)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)

            VStack(alignment: .leading, spacing: 12) {
                Text("Alerts")
                    .font(.headline)
                let alerts = snapshot.alerts.filter { $0.environmentId == environment.id }
                if alerts.isEmpty {
                    Label("No alerts recorded", systemImage: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                }
                ForEach(alerts) { alert in
                    AlertSummaryRow(alert: alert)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.vertical, 20)
    }

    private var identityAndSnapshot: some View {
        DisclosureGroup(isExpanded: $identifiersExpanded) {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Environment ID", value: environment.id, monospaced: true)
                KeyValueRow(key: "Repository ID", value: environment.repositoryId, monospaced: true)
                KeyValueRow(key: "Worktree ID", value: environment.worktreeId, monospaced: true)
                KeyValueRow(key: "Attention alert IDs", value: environment.attentionAlertIds.isEmpty ? "None" : environment.attentionAlertIds.joined(separator: ", "), monospaced: true)
                KeyValueRow(key: "Snapshot revision", value: "\(snapshot.snapshotRevision)", monospaced: true)
                KeyValueRow(key: "Snapshot generated", value: snapshot.generatedAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 12)
        } label: {
            Label("Identifiers & snapshot", systemImage: "number")
                .font(.headline)
                .padding(.vertical, 17)
        }
    }

    private var headerSubtitle: String {
        let repository = snapshot.repository(for: environment)?.displayName ?? "Unknown repository"
        let path = snapshot.worktree(for: environment)?.path ?? "Worktree unavailable"
        return "\(repository) · \(path)"
    }

    private func serviceExpansionBinding(_ id: String) -> Binding<Bool> {
        Binding(
            get: { expandedServiceIDs.contains(id) },
            set: { expanded in
                if expanded { expandedServiceIDs.insert(id) } else { expandedServiceIDs.remove(id) }
            }
        )
    }
}

private struct ServiceInspector: View {
    let service: Service
    let leases: [PortLease]
    let publishedURL: String?
    @Binding var expanded: Bool

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 14) {
                HStack(alignment: .top, spacing: 24) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Lifecycle")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                        KeyValueRow(key: "Service ID", value: service.id, monospaced: true)
                        KeyValueRow(key: "Desired", value: service.desiredState.label)
                        KeyValueRow(key: "Observed", value: service.observedState.label)
                        KeyValueRow(key: "Health", value: service.health.label)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Current run")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                        if let run = service.run {
                            KeyValueRow(key: "Run ID", value: run.id, monospaced: true)
                            KeyValueRow(key: "Started", value: run.startedAt.formatted(date: .abbreviated, time: .standard))
                            KeyValueRow(key: "Restarts", value: "\(run.restartCount)", monospaced: true)
                            KeyValueRow(key: "Processes", value: "\(run.processCount)", monospaced: true)
                            KeyValueRow(key: "CPU", value: Format.cpu(run.cpuPercent))
                            KeyValueRow(key: "Memory", value: Format.memory(run.memoryBytes))
                        } else {
                            Text("No service run is reported.")
                                .foregroundStyle(.secondary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }

                if let publishedURL, let url = URL(string: publishedURL) {
                    Link(destination: url) {
                        Label("Open \(publishedURL)", systemImage: "arrow.up.right.square")
                    }
                }

                if !leases.isEmpty {
                    Divider()
                    Text("Assigned ports")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(leases) { lease in
                        PortLeaseRow(lease: lease)
                    }
                }
            }
            .padding(.leading, 31)
            .padding(.top, 12)
            .padding(.bottom, 15)
        } label: {
            HStack(spacing: 11) {
                Image(systemName: service.kind == "web" ? "macwindow" : "network")
                    .foregroundStyle(.secondary)
                    .frame(width: 18)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 7) {
                        Text(service.displayName)
                            .font(.headline)
                        Text(service.kind)
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(.secondary)
                    }
                    HStack(spacing: 11) {
                        Text(service.observedState.label)
                            .foregroundStyle(service.observedState.tint)
                        if let run = service.run {
                            Text("\(run.processCount) processes")
                            Text(Format.cpu(run.cpuPercent))
                            Text(Format.memory(run.memoryBytes))
                            if run.restartCount > 0 {
                                Text("\(run.restartCount) restarts")
                                    .foregroundStyle(.orange)
                            }
                        }
                    }
                    .font(.caption)
                    .foregroundStyle(.secondary)
                }
                Spacer()
                HealthBadge(health: service.health)
            }
            .padding(.vertical, 14)
        }
    }
}

private struct PortLeaseRow: View {
    let lease: PortLease

    var body: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 16, verticalSpacing: 3) {
            GridRow {
                Text(lease.purpose)
                    .font(.callout.weight(.medium))
                Text(verbatim: "\(lease.host):\(lease.port)")
                    .font(.callout.monospaced())
                Text(lease.state)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            GridRow {
                Text("Lease ID").foregroundStyle(.secondary)
                Text(lease.id).font(.caption.monospaced()).textSelection(.enabled)
                Text("Acquired \(Format.relative(lease.acquiredAt))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private struct InfrastructureLeaseInspector: View {
    let lease: InfrastructureLease

    var body: some View {
        HStack(alignment: .top, spacing: 11) {
            Image(systemName: "externaldrive.connected.to.line.below")
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 5) {
                Text(lease.displayName)
                    .font(.callout.weight(.semibold))
                HStack(spacing: 9) {
                    Text(lease.kind)
                    Text("\(lease.scope) scope")
                    Text(lease.state)
                    Text(lease.ownership)
                        .foregroundStyle(lease.ownership == "owned" ? .green : .secondary)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                Text("service \(lease.serviceId) · lease \(lease.id)")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            Spacer()
        }
    }
}
