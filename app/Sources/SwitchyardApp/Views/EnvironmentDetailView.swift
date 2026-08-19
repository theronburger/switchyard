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
    @State private var showRebuildConfirmation = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                header
                Divider()
                if environment.hasUnverifiableServices || environment.observedState == .orphaned {
                    recoveryNotice
                        .padding(.top, 18)
                }
                if environment.observedState == .stopped, let worktree = snapshot.worktree(for: environment) {
                    StartEnvironmentView(
                        model: model,
                        snapshot: snapshot,
                        initialWorktreeId: worktree.id,
                        initialTargetId: environment.targetId
                    )
                        .padding(.top, 18)
                }
                if let worktree = snapshot.worktree(for: environment) {
                    JiraIssueView(worktree: worktree, loadsLiveData: !model.isFixtureMode)
                        .padding(.top, 18)
                    GitHubPullRequestView(worktree: worktree)
                        .padding(.top, 18)
                }
                workloads
                Divider()
                environmentDetailsHeading
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
            .switchyardScrollbars()
        }
        .frame(maxWidth: .infinity, alignment: .top)
        .onAppear {
            if expandedServiceIDs.isEmpty {
                expandedServiceIDs = Set(environment.services.map(\.id))
            }
        }
        .confirmationDialog(
            "Rebuild local services against \(runtimeTarget?.displayName ?? "this target")?",
            isPresented: $showRebuildConfirmation,
            titleVisibility: .visible
        ) {
            Button("Confirm, rebuild, and restart", role: .destructive) {
                rebuild(confirmedTargetId: environment.targetId)
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This stops the current cluster, rebuilds its selected workloads, and starts them again against the same remote target.")
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
                        CopyValueButton(value: environment.displayName, label: "branch")
                        HealthBadge(health: environment.health)
                    }
                    Text(headerSubtitle)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                Spacer()
                if let worktree = snapshot.worktree(for: environment) {
                    OpenInZedButton(worktree: worktree)
                }
                environmentButtons
            }

            if let worktree = snapshot.worktree(for: environment) {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(alignment: .center, spacing: 8) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text("Path")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Text(worktree.path)
                                .font(.callout.monospaced())
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .help(worktree.path)
                                .textSelection(.enabled)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        CopyValueButton(value: worktree.path, label: "path")
                        OpenInFinderButton(path: worktree.path)
                    }
                    .padding(.horizontal, 12)

                    Divider()

                    HStack(spacing: 0) {
                        NativeFact(
                            label: "Branch",
                            value: worktree.branch ?? "Detached HEAD",
                            monospaced: true,
                            copyable: true
                        )
                        .frame(minWidth: 130)
                        NativeFact(
                            label: "Head",
                            value: Format.shortRevision(worktree.headRevision),
                            monospaced: true,
                            copyable: true
                        )
                        NativeFact(label: "Role", value: worktree.isPrimary ? "Primary checkout" : "Linked worktree")
                        NativeFact(
                            label: "Git state",
                            value: worktree.git.isClean ? "Clean" : "Needs review",
                            tint: worktree.git.isClean ? .green : .orange,
                            showsDivider: false
                        )
                    }
                }
                .padding(.vertical, 12)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }

            ScrollView(.horizontal) {
                HStack(spacing: 0) {
                    NativeMetric(value: targetDisplayName, label: "Target", tint: targetTint)
                    NativeMetric(value: environment.desiredState.label, label: "Desired")
                    NativeMetric(value: environment.observedState.label, label: "Observed", tint: environment.observedState.tint)
                    NativeMetric(value: "\(environment.services.count)", label: "Workloads")
                    NativeMetric(value: "\(environment.totalProcessCount)", label: "Processes")
                    NativeMetric(value: Format.cpu(environment.resources.cpuPercent), label: "CPU")
                    NativeMetric(value: Format.memory(environment.resources.memoryBytes), label: "Memory")
                    NativeMetric(value: "\(environment.revision)", label: "Revision", showsDivider: false)
                }
                .frame(minWidth: 820)
                .switchyardScrollbars()
            }
            .padding(.vertical, 14)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .padding(.vertical, 24)
    }

    @ViewBuilder
    private var environmentButtons: some View {
        if environment.allowsStopRequest || actionKind != nil {
            HStack(spacing: 8) {
                Button {
                    if runtimeTarget?.warnOnStart == true {
                        showRebuildConfirmation = true
                    } else {
                        rebuild(confirmedTargetId: nil)
                    }
                } label: {
                    Label(rebuildButtonLabel, systemImage: "arrow.clockwise")
                }
                .buttonStyle(.bordered)
                .tint(actionKind == .rebuild ? .blue : .accentColor)
                .disabled(!model.canSubmitEnvironmentAction || !environment.allowsRebuildRequest)
                .help("Stop this environment, rebuild its selected workloads, and start it again")

                Button {
                    Task { await model.stopEnvironment(environment) }
                } label: {
                    Label(stopButtonLabel, systemImage: "stop.fill")
                }
                .buttonStyle(.borderedProminent)
                .tint(actionStage == .stopping ? .blue : .red)
                .disabled(!model.canSubmitEnvironmentAction || !environment.allowsStopRequest)
            }
        }
    }

    private var actionKind: EnvironmentActionKind? {
        model.environmentActionKind(forWorktreeId: environment.worktreeId)
    }

    private var actionStage: EnvironmentActionStage? {
        model.environmentTransition(forWorktreeId: environment.worktreeId)
    }

    private var stopButtonLabel: String {
        actionStage == .stopping ? "Stopping…" : "Stop environment"
    }

    private var rebuildButtonLabel: String {
        if actionKind == .start, actionStage == .starting { return "Starting…" }
        guard actionKind == .rebuild else { return "Rebuild & restart" }
        switch actionStage {
        case .stopping: return "Stopping…"
        case .rebuilding: return "Rebuilding…"
        case .starting: return "Starting…"
        case nil: return "Rebuild & restart"
        }
    }

    private var runtimeTarget: RuntimeTarget? {
        snapshot.repository(for: environment)?.runtime?.targets.first { $0.id == environment.targetId }
    }

    private var recoveryNotice: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "exclamationmark.shield.fill")
                .font(.title3)
                .foregroundStyle(.orange)
            VStack(alignment: .leading, spacing: 5) {
                Text("Process ownership needs recovery")
                    .font(.headline)
                Text("Switchyard retained every process and resource because it could not verify ownership safely. Stop or rebuild will remain available; Switchyard will signal processes only after ownership is verified.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                if let error = latestEnvironmentFailure?.error {
                    Text("\(error.code): \(error.message)")
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                    if let diagnostic = error.diagnostic, !diagnostic.isEmpty {
                        Text(diagnostic)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                    }
                }
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.orange.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private var latestEnvironmentFailure: OperationModel? {
        snapshot.operations(forEnvironment: environment.id).first { $0.state == .failed }
    }

    private func rebuild(confirmedTargetId: String?) {
        Task {
            await model.rebuildEnvironment(environment, confirmedTargetId: confirmedTargetId)
        }
    }

    private var workloads: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Text("Workloads")
                    .font(.title3.weight(.semibold))
                Text("\(environment.services.count)")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 7)
                    .padding(.vertical, 3)
                    .background(.quaternary, in: Capsule())
            }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            .padding(.top, 20)
            .padding(.bottom, 8)

            if environment.services.isEmpty {
                ContentUnavailableView(
                    "No workloads requested",
                    systemImage: "pause.circle",
                    description: Text("This environment currently owns no workload runs.")
                )
                .padding(.vertical, 18)
            }

            ForEach(Array(environment.services.enumerated()), id: \.element.id) { index, service in
                ServiceInspector(
                    service: service,
                    leases: environment.portLeases(for: service),
                    publishedURL: environment.urls[service.id],
                    changes: snapshot.worktree(for: environment)?.changes?.service(service.id),
                    expanded: serviceExpansionBinding(service.id)
                )
                if index < environment.services.count - 1 {
                    Divider()
                }
            }
        }
        .padding(.bottom, 18)
    }

    private var environmentDetailsHeading: some View {
        Text("Environment details")
            .font(.title3.weight(.semibold))
            .padding(.top, 20)
            .padding(.bottom, 3)
            .accessibilityAddTraits(.isHeader)
    }

    private var worktreeAndRepository: some View {
        FullWidthDisclosure(isExpanded: $worktreeExpanded) {
            Label("Worktree & repository", systemImage: "arrow.triangle.branch")
                .font(.headline)
                .padding(.vertical, 17)
            Spacer()
        } content: {
            if let repository = snapshot.repository(for: environment),
               let worktree = snapshot.worktree(for: environment) {
                HStack(alignment: .top, spacing: 28) {
                    VStack(alignment: .leading, spacing: 7) {
                        Text("Worktree")
                            .font(.callout.weight(.semibold))
                        KeyValueRow(key: "Path", value: worktree.path, monospaced: true, copyable: true)
                        KeyValueRow(key: "Branch", value: worktree.branch ?? "Detached HEAD", monospaced: true, copyable: true)
                        KeyValueRow(key: "Head", value: worktree.headRevision, monospaced: true, copyable: true)
                        KeyValueRow(key: "Role", value: worktree.isPrimary ? "Primary checkout" : "Linked worktree")
                        GitStateDetail(state: worktree.git)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    VStack(alignment: .leading, spacing: 7) {
                        Text("Repository")
                            .font(.callout.weight(.semibold))
                        KeyValueRow(key: "Name", value: repository.displayName)
                        KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true, copyable: true)
                        KeyValueRow(key: "Remote", value: repository.remote, monospaced: true, copyable: true)
                        KeyValueRow(key: "Adapter", value: repository.adapter, monospaced: true)
                        if let runtime = repository.runtime {
                            KeyValueRow(key: "Default target", value: runtime.defaultTargetId)
                            KeyValueRow(key: "Runtime catalog", value: "\(runtime.targets.count) targets · \(runtime.services.count) services")
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.top, 14)
                .padding(.bottom, 17)
            } else {
                Text("The reported repository or worktree is no longer available.")
                    .foregroundStyle(.secondary)
                    .padding(.top, 12)
            }
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
                        CopyValueButton(value: entry.url, label: "URL")
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
        FullWidthDisclosure(isExpanded: $infrastructureExpanded) {
            Label("Infrastructure", systemImage: "externaldrive.connected.to.line.below")
                .font(.headline)
                .padding(.vertical, 17)
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 12) {
                if environment.infrastructureLeases.isEmpty {
                    Text("No infrastructure leases.")
                        .foregroundStyle(.secondary)
                }
                ForEach(environment.infrastructureLeases) { lease in
                    InfrastructureLeaseInspector(lease: lease)
                }
            }
            .padding(.top, 10)
            .padding(.bottom, 17)
        }
    }

    private var activityAndAlerts: some View {
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 10) {
                Text("Operations")
                    .font(.headline)
                OperationTable(
                    operations: snapshot.operations(forEnvironment: environment.id),
                    snapshot: snapshot
                )
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
        FullWidthDisclosure(isExpanded: $identifiersExpanded) {
            Label("Identifiers & state metadata", systemImage: "number")
                .font(.headline)
                .padding(.vertical, 17)
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Environment ID", value: environment.id, monospaced: true, copyable: true)
                KeyValueRow(key: "Target environment", value: targetDisplayName)
                KeyValueRow(key: "Repository ID", value: environment.repositoryId, monospaced: true, copyable: true)
                KeyValueRow(key: "Worktree ID", value: environment.worktreeId, monospaced: true, copyable: true)
                KeyValueRow(key: "Attention alert IDs", value: environment.attentionAlertIds.isEmpty ? "None" : environment.attentionAlertIds.joined(separator: ", "), monospaced: true)
                KeyValueRow(key: "State revision", value: "\(snapshot.snapshotRevision)", monospaced: true)
                KeyValueRow(key: "State generated", value: snapshot.generatedAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 12)
        }
    }

    private var headerSubtitle: String {
        snapshot.repository(for: environment)?.displayName ?? "Unknown repository"
    }

    private var target: RuntimeTarget? {
        guard let repository = snapshot.repository(for: environment) else { return nil }
        let targetID = environment.targetId ?? repository.runtime?.defaultTargetId
        return repository.runtime?.targets.first { $0.id == targetID }
    }

    private var targetDisplayName: String {
        target?.displayName ?? environment.targetId ?? "Unknown"
    }

    private var targetTint: Color {
        switch target?.risk {
        case "production": .red
        case "elevated": .orange
        default: .primary
        }
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
    let changes: ServiceLineChanges?
    @Binding var expanded: Bool

    var body: some View {
        FullWidthDisclosure(isExpanded: $expanded) {
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
                if let changes {
                    LineChangeBadges(committed: changes.committed, uncommitted: changes.uncommitted)
                }
                HealthBadge(health: service.health)
            }
            .padding(.vertical, 14)
        } content: {
            VStack(alignment: .leading, spacing: 14) {
                HStack(alignment: .top, spacing: 24) {
                    VStack(alignment: .leading, spacing: 6) {
                        Text("Lifecycle")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                        KeyValueRow(key: "Workload ID", value: service.id, monospaced: true, copyable: true)
                        KeyValueRow(key: "Desired", value: service.desiredState.label)
                        KeyValueRow(key: "Observed", value: service.observedState.label)
                        KeyValueRow(key: "Health", value: service.health.label)
                        if let observationCode = service.observationCode {
                            KeyValueRow(key: "Observation", value: observationCode, monospaced: true, copyable: true)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    VStack(alignment: .leading, spacing: 6) {
                        Text("Current run")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                        if let run = service.run {
                            KeyValueRow(key: "Run ID", value: run.id, monospaced: true, copyable: true)
                            KeyValueRow(key: "Started", value: run.startedAt.formatted(date: .abbreviated, time: .standard))
                            KeyValueRow(key: "Restarts", value: "\(run.restartCount)", monospaced: true)
                            KeyValueRow(key: "Processes", value: "\(run.processCount)", monospaced: true)
                            KeyValueRow(key: "CPU", value: Format.cpu(run.cpuPercent))
                            KeyValueRow(key: "Memory", value: Format.memory(run.memoryBytes))
                        } else {
                            Text("No workload run is reported.")
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

                if let changes, !changes.committed.isEmpty || !changes.uncommitted.isEmpty {
                    Divider()
                    Text("Attributed branch changes")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    HStack(spacing: 12) {
                        LineChangeBadges(committed: changes.committed, uncommitted: changes.uncommitted)
                        Text("\(changes.committed.files) committed files · \(changes.uncommitted.files) uncommitted files")
                            .font(.caption)
                            .foregroundStyle(.secondary)
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
