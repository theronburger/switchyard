import SwiftUI
import SwitchyardKit

/// The main window: environment sidebar, overview grid, detail, and doctor.
struct CommandCenterView: View {
    @Bindable var model: AppModel

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 220, ideal: 250)
        } detail: {
            detail
        }
        .navigationTitle("Switchyard")
        .toolbar { toolbarContent }
        .task { model.startPolling() }
        .frame(minWidth: 900, minHeight: 560)
    }

    // MARK: - Sidebar

    private var sidebar: some View {
        List(selection: $model.selection) {
            if let snapshot = model.snapshot {
                ForEach(snapshot.repositories) { repository in
                    Section(repository.displayName) {
                        let environments = snapshot.environments.filter { $0.repositoryId == repository.id }
                        if environments.isEmpty {
                            Text("No environments")
                                .foregroundStyle(.secondary)
                        }
                        ForEach(environments) { environment in
                            sidebarRow(environment, in: snapshot)
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
    }

    private func sidebarRow(_ environment: EnvironmentModel, in snapshot: StatusSnapshot) -> some View {
        HStack(spacing: 8) {
            StatusDot(color: environment.health.tint)
            VStack(alignment: .leading, spacing: 1) {
                Text(environment.displayName)
                if let branch = snapshot.worktree(for: environment)?.branch {
                    Text(branch)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            Spacer()
            let alertCount = snapshot.alerts(forEnvironment: environment.id).count
            if alertCount > 0 {
                Text("\(alertCount)")
                    .font(.caption2.weight(.bold))
                    .padding(.horizontal, 5)
                    .padding(.vertical, 1)
                    .background(.orange.opacity(0.2), in: Capsule())
                    .foregroundStyle(.orange)
            }
        }
    }

    // MARK: - Detail

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
        case nil:
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
        VStack(spacing: 16) {
            ContentUnavailableView {
                Label("No environments yet", systemImage: "square.stack.3d.up.slash")
            } description: {
                Text("Switchyard has not discovered any repositories or worktrees. Once the daemon adopts your Marketplace checkout, environments appear here.")
            }
            Button("Open Connection Doctor") {
                model.selection = .connectionDoctor
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func errorState(_ message: String) -> some View {
        VStack(spacing: 16) {
            ContentUnavailableView {
                Label("Daemon unavailable", systemImage: "bolt.horizontal.circle")
            } description: {
                Text(message)
            }
            HStack {
                Button("Retry") {
                    Task { await model.refresh() }
                }
                Button("Open Connection Doctor") {
                    model.selection = .connectionDoctor
                }
                .buttonStyle(.borderedProminent)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    // MARK: - Toolbar

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
            .help("Reload the status snapshot")
        }
        ToolbarItem(placement: .status) {
            if let refreshed = model.lastRefreshedAt {
                Text("Updated \(Format.relative(refreshed)) · \(model.dataSourceDescription)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
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

/// Overview shown when nothing is selected: aggregate stats plus one card per
/// environment.
struct OverviewView: View {
    @Bindable var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if let summary = model.summary {
                    HStack(spacing: 10) {
                        StatChip(value: "\(summary.environmentCount)", label: "environments")
                        StatChip(value: "\(summary.runningCount)", label: "running", tint: .green)
                        StatChip(
                            value: "\(summary.attentionCount)",
                            label: "need attention",
                            tint: summary.attentionCount > 0 ? .orange : .primary
                        )
                        StatChip(value: Format.cpu(summary.totalCPUPercent), label: "aggregate")
                        StatChip(value: Format.memory(summary.totalMemoryBytes), label: "memory")
                    }
                }
                daemonCard
                if let snapshot = model.snapshot {
                    EnvironmentActionBanner(model: model)
                    StartEnvironmentView(model: model, snapshot: snapshot)
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 340), spacing: 14)], spacing: 14) {
                        ForEach(snapshot.environments) { environment in
                            Button {
                                model.selection = .environment(environment.id)
                            } label: {
                                EnvironmentCard(environment: environment, snapshot: snapshot)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
            .padding(20)
        }
    }

    private var daemonCard: some View {
        SectionCard(title: "Daemon", systemImage: "gearshape.2") {
            HStack(spacing: 10) {
                Image(systemName: model.lifecycleState.systemImage)
                    .foregroundStyle(model.lifecycleState.tint)
                VStack(alignment: .leading, spacing: 2) {
                    Text(model.lifecycleState.displayName)
                        .font(.callout.weight(.medium))
                    Text(model.lifecycleState.summary)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if let daemon = model.snapshot?.daemon {
                    VStack(alignment: .trailing, spacing: 2) {
                        Text("v\(daemon.version)")
                            .font(.caption.monospaced())
                        Text("started \(Format.relative(daemon.startedAt))")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Button("Doctor") {
                    model.selection = .connectionDoctor
                }
                .controlSize(.small)
            }
        }
    }
}

/// Summary card for one environment on the overview grid.
struct EnvironmentCard: View {
    let environment: EnvironmentModel
    let snapshot: StatusSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(environment.displayName)
                        .font(.title3.weight(.semibold))
                    if let branch = snapshot.worktree(for: environment)?.branch {
                        Text(branch)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                Spacer()
                HealthBadge(health: environment.health)
            }
            Divider()
            ForEach(environment.services) { service in
                HStack(spacing: 8) {
                    StatusDot(color: service.health.tint)
                    Text(service.displayName)
                        .font(.callout)
                    Spacer()
                    Text(service.observedState.label)
                        .font(.caption)
                        .foregroundStyle(service.observedState.tint)
                }
            }
            Divider()
            HStack {
                Label(Format.cpu(environment.resources.cpuPercent), systemImage: "cpu")
                Label(Format.memory(environment.resources.memoryBytes), systemImage: "memorychip")
                Spacer()
                let alertCount = snapshot.alerts(forEnvironment: environment.id).count
                if alertCount > 0 {
                    Label("\(alertCount)", systemImage: "exclamationmark.triangle.fill")
                        .foregroundStyle(.orange)
                }
            }
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}
