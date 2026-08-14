import SwiftUI
import SwitchyardKit

struct CommandCenterView: View {
    @Bindable var model: AppModel

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 230, ideal: 270, max: 330)
        } detail: {
            detail
        }
        .navigationTitle(windowTitle)
        .toolbar { toolbarContent }
        .task { model.startPolling() }
        .onReceive(NotificationCenter.default.publisher(for: .switchyardOpenCommandCenter)) { _ in
            CommandCenterWindowPresenter.presentWhenAvailable()
        }
        .frame(minWidth: 1_020, minHeight: 640)
    }

    private var sidebar: some View {
        List(selection: $model.selection) {
            Label("Overview", systemImage: "rectangle.grid.1x2")
                .tag(SidebarSelection.overview)

            if let snapshot = model.snapshot {
                ForEach(snapshot.repositories) { repository in
                    Section {
                        ForEach(repository.worktrees) { worktree in
                            let environment = snapshot.environment(for: worktree)
                            SidebarWorktreeRow(
                                worktree: worktree,
                                environment: environment,
                                alertCount: environment.map { snapshot.alerts(forEnvironment: $0.id).count } ?? 0
                            )
                            .tag(sidebarSelection(repository: repository, worktree: worktree, environment: environment))
                        }
                    } header: {
                        Text(repository.displayName)
                    }
                }

                let detached = snapshot.environments.filter { snapshot.worktree(for: $0) == nil }
                if !detached.isEmpty {
                    Section("Detached environments") {
                        ForEach(detached) { environment in
                            SidebarDetachedEnvironmentRow(environment: environment)
                                .tag(SidebarSelection.environment(environment.id))
                        }
                    }
                }
            }

            Section("Setup") {
                Label {
                    Text("Connection Doctor")
                } icon: {
                    Image(systemName: model.lifecycleState.systemImage)
                        .foregroundStyle(model.lifecycleState.tint)
                }
                .tag(SidebarSelection.connectionDoctor)
            }
        }
        .listStyle(.sidebar)
        .safeAreaInset(edge: .top, spacing: 0) {
            sidebarHeader
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            sidebarFooter
        }
    }

    private var sidebarHeader: some View {
        HStack(spacing: 10) {
            Image(systemName: "point.3.connected.trianglepath.dotted")
                .font(.title2)
                .foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 1) {
                Text("Switchyard")
                    .font(.headline)
                Text(model.dataSourceDescription)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .background(.bar)
        .overlay(alignment: .bottom) { Divider() }
    }

    private var sidebarFooter: some View {
        HStack(spacing: 7) {
            Image(systemName: model.lifecycleState.systemImage)
                .foregroundStyle(model.lifecycleState.tint)
            VStack(alignment: .leading, spacing: 1) {
                Text(model.lifecycleState.displayName)
                    .font(.caption.weight(.medium))
                if let refreshed = model.lastRefreshedAt {
                    Text("Updated \(Format.relative(refreshed))")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .background(.bar)
        .overlay(alignment: .top) { Divider() }
    }

    @ViewBuilder
    private var detail: some View {
        switch model.selection {
        case .connectionDoctor:
            ConnectionDoctorView(model: model)
        case .environment(let id):
            if let snapshot = model.snapshot, let environment = snapshot.environment(withId: id) {
                EnvironmentDetailView(model: model, environment: environment, snapshot: snapshot)
            } else {
                fallback
            }
        case .worktree(let repositoryId, let worktreeId):
            if let snapshot = model.snapshot,
               let repository = snapshot.repository(withId: repositoryId),
               let worktree = repository.worktrees.first(where: { $0.id == worktreeId }) {
                WorktreeDetailView(model: model, repository: repository, worktree: worktree, snapshot: snapshot)
            } else {
                fallback
            }
        case .overview, nil:
            fallback
        }
    }

    @ViewBuilder
    private var fallback: some View {
        switch model.phase {
        case .idle, .loading:
            ProgressView("Loading status…")
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .failed(let message):
            errorState(message)
        case .empty:
            emptyState
        case .loaded:
            OverviewView(model: model)
        }
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("No worktrees discovered", systemImage: "arrow.triangle.branch")
        } description: {
            Text("Open Connection Doctor to inspect repository discovery and daemon setup.")
        } actions: {
            Button("Open Connection Doctor") { model.selection = .connectionDoctor }
                .buttonStyle(.borderedProminent)
        }
    }

    private func errorState(_ message: String) -> some View {
        ContentUnavailableView {
            Label("Daemon unavailable", systemImage: "bolt.horizontal.circle")
        } description: {
            Text(message)
        } actions: {
            Button("Retry") { Task { await model.refresh() } }
            Button("Open Connection Doctor") { model.selection = .connectionDoctor }
                .buttonStyle(.borderedProminent)
        }
    }

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        if model.isFixtureMode {
            ToolbarItem(placement: .navigation) {
                Picker("Fixture", selection: scenarioBinding) {
                    ForEach(FixtureScenario.allCases) { scenario in
                        Text(scenario.displayName).tag(scenario)
                    }
                }
                .pickerStyle(.segmented)
                .help("Development data source: \(model.scenario.blurb)")
            }
        }
        ToolbarItem(placement: .primaryAction) {
            Button {
                Task { await model.refresh() }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }
            .keyboardShortcut("r", modifiers: .command)
            .help("Reload the daemon snapshot")
        }
        ToolbarItem(placement: .status) {
            if let snapshot = model.snapshot {
                Text("Snapshot \(snapshot.snapshotRevision)")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var scenarioBinding: Binding<FixtureScenario> {
        Binding(
            get: { model.scenario },
            set: { scenario in Task { await model.select(scenario: scenario) } }
        )
    }

    private var windowTitle: String {
        switch model.selection {
        case .environment(let id):
            return model.snapshot?.environment(withId: id)?.displayName ?? "Switchyard"
        case .worktree(_, let id):
            return model.snapshot?.worktree(withId: id)?.branch ?? "Worktree"
        case .connectionDoctor: return "Connection Doctor"
        case .overview, nil: return "Switchyard"
        }
    }

    private func sidebarSelection(
        repository: Repository,
        worktree: Worktree,
        environment: EnvironmentModel?
    ) -> SidebarSelection {
        if let environment { return .environment(environment.id) }
        return .worktree(repositoryId: repository.id, worktreeId: worktree.id)
    }
}

private struct SidebarWorktreeRow: View {
    let worktree: Worktree
    let environment: EnvironmentModel?
    let alertCount: Int

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            StatusDot(color: environment?.health.tint ?? .secondary)
                .padding(.top, 5)
            VStack(alignment: .leading, spacing: 2) {
                Text(worktree.branch ?? URL(fileURLWithPath: worktree.path).lastPathComponent)
                    .font(.callout.weight(.medium))
                    .lineLimit(2)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 5)
            if alertCount > 0 {
                Text("\(alertCount)")
                    .font(.caption2.weight(.bold))
                    .padding(.horizontal, 5)
                    .padding(.vertical, 1)
                    .background(.orange.opacity(0.16), in: Capsule())
                    .foregroundStyle(.orange)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private var detail: String {
        guard let environment else { return "Stopped · no owned resources" }
        return "\(environment.observedState.label) · \(environment.totalProcessCount) processes"
    }
}

private struct SidebarDetachedEnvironmentRow: View {
    let environment: EnvironmentModel

    var body: some View {
        Label {
            VStack(alignment: .leading, spacing: 2) {
                Text(environment.displayName)
                Text(environment.observedState.label)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } icon: {
            Image(systemName: "questionmark.folder")
                .foregroundStyle(environment.health.tint)
        }
    }
}

struct OverviewView: View {
    @Bindable var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                overviewHeader
                if let snapshot = model.snapshot {
                    EnvironmentActionBanner(model: model)
                    StartEnvironmentView(model: model, snapshot: snapshot)
                    RepositoryInventoryView(model: model, snapshot: snapshot)
                    HStack(alignment: .top, spacing: 24) {
                        GlobalOperationsView(snapshot: snapshot)
                        GlobalAlertsView(snapshot: snapshot)
                    }
                    SnapshotMetadataView(snapshot: snapshot)
                }
            }
            .padding(28)
            .frame(maxWidth: 1_180, alignment: .leading)
        }
        .frame(maxWidth: .infinity, alignment: .top)
    }

    private var overviewHeader: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: model.lifecycleState.systemImage)
                    .font(.title2)
                    .foregroundStyle(model.lifecycleState.tint)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Switchyard")
                        .font(.largeTitle.bold())
                    Text(model.lifecycleState.summary)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Button("Connection Doctor") { model.selection = .connectionDoctor }
            }
            if let summary = model.summary {
                HStack(spacing: 0) {
                    NativeMetric(value: "\(summary.environmentCount)", label: "Environments")
                    NativeMetric(value: "\(summary.runningCount)", label: "Running", tint: .green)
                    NativeMetric(value: "\(summary.attentionCount)", label: "Attention", tint: summary.attentionCount > 0 ? .orange : .primary)
                    NativeMetric(value: Format.cpu(summary.totalCPUPercent), label: "Aggregate CPU")
                    NativeMetric(value: Format.memory(summary.totalMemoryBytes), label: "Memory")
                }
                .padding(.vertical, 14)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
        }
    }
}

private struct RepositoryInventoryView: View {
    @Bindable var model: AppModel
    let snapshot: StatusSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Repositories & worktrees")
                .font(.title3.weight(.semibold))
            ForEach(snapshot.repositories) { repository in
                DisclosureGroup {
                    VStack(alignment: .leading, spacing: 10) {
                        KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true)
                        KeyValueRow(key: "Remote", value: repository.remote, monospaced: true)
                        KeyValueRow(key: "Adapter", value: repository.adapter, monospaced: true)
                        KeyValueRow(key: "Repository ID", value: repository.id, monospaced: true)
                        Divider()
                        ForEach(repository.worktrees) { worktree in
                            Button {
                                if let environment = snapshot.environment(for: worktree) {
                                    model.selection = .environment(environment.id)
                                } else {
                                    model.selection = .worktree(repositoryId: repository.id, worktreeId: worktree.id)
                                }
                            } label: {
                                HStack(spacing: 10) {
                                    Image(systemName: "arrow.triangle.branch")
                                        .foregroundStyle(.secondary)
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(worktree.branch ?? "Detached HEAD")
                                            .font(.callout.weight(.medium))
                                        Text(worktree.path)
                                            .font(.caption.monospaced())
                                            .foregroundStyle(.secondary)
                                            .lineLimit(1)
                                    }
                                    Spacer()
                                    Text(snapshot.environment(for: worktree)?.observedState.label ?? "Stopped")
                                        .font(.caption)
                                        .foregroundStyle(snapshot.environment(for: worktree)?.observedState.tint ?? .secondary)
                                }
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    .padding(.top, 10)
                } label: {
                    HStack {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(repository.displayName)
                                .font(.headline)
                            Text("\(repository.worktrees.count) worktrees · \(repository.adapter) adapter")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
                .padding(14)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
        }
    }
}

private struct GlobalOperationsView: View {
    let snapshot: StatusSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("All operations")
                .font(.headline)
            if snapshot.operations.isEmpty {
                Text("No recorded operations.").foregroundStyle(.secondary)
            }
            ForEach(snapshot.operations) { operation in
                OperationSummaryRow(operation: operation)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct GlobalAlertsView: View {
    let snapshot: StatusSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("All alerts")
                .font(.headline)
            if snapshot.alerts.isEmpty {
                Label("No alerts recorded", systemImage: "checkmark.circle.fill")
                    .foregroundStyle(.green)
            }
            ForEach(snapshot.alerts) { alert in
                AlertSummaryRow(alert: alert)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct SnapshotMetadataView: View {
    let snapshot: StatusSnapshot

    var body: some View {
        DisclosureGroup("Daemon & snapshot metadata") {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Daemon state", value: snapshot.daemon.state.rawValue)
                KeyValueRow(key: "Daemon version", value: snapshot.daemon.version, monospaced: true)
                KeyValueRow(key: "Daemon instance", value: snapshot.daemon.instanceId, monospaced: true)
                KeyValueRow(key: "Daemon started", value: snapshot.daemon.startedAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Schema", value: "\(snapshot.schemaVersion)", monospaced: true)
                KeyValueRow(key: "Snapshot revision", value: "\(snapshot.snapshotRevision)", monospaced: true)
                KeyValueRow(key: "Generated", value: snapshot.generatedAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 10)
        }
        .font(.callout)
        .padding(.vertical, 4)
    }
}
