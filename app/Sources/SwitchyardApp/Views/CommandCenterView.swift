import SwiftUI
import SwitchyardKit

enum CommandCenterLayout {
    static let minimumWidth: CGFloat = 680
    static let minimumHeight: CGFloat = 540
    static let defaultWidth: CGFloat = 1_320
    static let defaultHeight: CGFloat = 820
}

struct CommandCenterView: View {
    @Bindable var model: AppModel
    @State private var showsCleanup = false

    var body: some View {
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 200, ideal: 235, max: 285)
        } detail: {
            detail
        }
        .navigationTitle(windowTitle)
        .toolbar { toolbarContent }
        .sheet(isPresented: $showsCleanup, onDismiss: { model.dismissCleanup() }) {
            CleanupReviewSheet(model: model, isPresented: $showsCleanup)
        }
        .task { model.startPolling() }
        .onReceive(NotificationCenter.default.publisher(for: .switchyardOpenCommandCenter)) { _ in
            CommandCenterWindowPresenter.presentWhenAvailable()
        }
        .alert(
            "Environment action failed",
            isPresented: Binding(
                get: { model.environmentActionFailureMessage != nil },
                set: { presented in
                    if !presented { model.dismissEnvironmentAction() }
                }
            )
        ) {
            Button("OK") { model.dismissEnvironmentAction() }
        } message: {
            Text(model.environmentActionFailureMessage ?? "The environment operation could not complete.")
        }
        .alert(
            "Workspace action failed",
            isPresented: Binding(
                get: { model.workspaceActionFailureMessage != nil },
                set: { presented in
                    if !presented { model.dismissWorkspaceAction() }
                }
            )
        ) {
            Button("OK") { model.dismissWorkspaceAction() }
        } message: {
            Text(model.workspaceActionFailureMessage ?? "The workspace operation could not complete.")
        }
        .frame(minWidth: CommandCenterLayout.minimumWidth, minHeight: CommandCenterLayout.minimumHeight)
    }

    private var sidebar: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 3) {
                SidebarRow(
                    isSelected: model.selection == .overview || model.selection == nil,
                    action: { model.selection = .overview }
                ) {
                    Label("Overview", systemImage: "rectangle.grid.1x2")
                        .font(.callout.weight(.medium))
                }

                if let snapshot = model.snapshot {
                    ForEach(snapshot.repositories) { repository in
                        SidebarRow(
                            isSelected: model.selection == .repository(repository.id),
                            action: { model.selection = .repository(repository.id) }
                        ) {
                            HStack(spacing: 6) {
                                Text(repository.displayName)
                                    .font(.caption.weight(.semibold))
                                    .foregroundStyle(.secondary)
                                Spacer(minLength: 4)
                                if model.acceptanceState(for: repository).requiresAcceptance {
                                    Image(systemName: "exclamationmark.circle.fill")
                                        .font(.caption2)
                                        .foregroundStyle(.orange)
                                        .help("Configuration changes for this repository are waiting for acceptance")
                                }
                                Image(systemName: "gearshape")
                                    .font(.caption2)
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        .padding(.top, 8)
                        .help("Repository settings")

                        ForEach(repository.worktrees) { worktree in
                            let environment = snapshot.environment(for: worktree)
                            let selection = sidebarSelection(
                                repository: repository,
                                worktree: worktree,
                                environment: environment
                            )
                            SidebarRow(
                                isSelected: model.selection == selection,
                                action: { model.selection = selection }
                            ) {
                                SidebarWorktreeRow(
                                    worktree: worktree,
                                    environment: environment,
                                    transition: model.environmentTransition(forWorktreeId: worktree.id),
                                    alertCount: environment.map { snapshot.alerts(forEnvironment: $0.id).count } ?? 0,
                                    loadsLiveJira: !model.isFixtureMode
                                )
                            }
                        }
                    }

                    let detached = snapshot.environments.filter { snapshot.worktree(for: $0) == nil }
                    if !detached.isEmpty {
                        Text("Detached environments")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(.secondary)
                            .padding(.horizontal, 8)
                            .padding(.top, 10)
                            .padding(.bottom, 2)
                        ForEach(detached) { environment in
                            SidebarRow(
                                isSelected: model.selection == .environment(environment.id),
                                action: { model.selection = .environment(environment.id) }
                            ) {
                                SidebarDetachedEnvironmentRow(environment: environment)
                            }
                        }
                    }
                }

                Text("Setup")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.top, 10)
                    .padding(.bottom, 2)
                SidebarRow(
                    isSelected: model.selection == .connectionDoctor,
                    action: { model.selection = .connectionDoctor }
                ) {
                    Label {
                        Text("Connection Doctor")
                    } icon: {
                        Image(systemName: model.lifecycleState.systemImage)
                            .foregroundStyle(model.lifecycleState.tint)
                    }
                    .font(.callout.weight(.medium))
                }
            }
            .padding(8)
        }
        .switchyardScrollbars()
        .background(Color(nsColor: .windowBackgroundColor))
        .safeAreaInset(edge: .top, spacing: 0) {
            sidebarHeader
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            sidebarFooter
        }
    }

    private var sidebarHeader: some View {
        HStack(spacing: 10) {
            SwitchyardBrandMark()
                .frame(width: 24, height: 24)
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
            Text(model.lifecycleState.displayName)
                .font(.caption.weight(.medium))
            if let snapshot = model.snapshot {
                Text("· Updated \(Format.relative(snapshot.generatedAt))")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .help("State revision \(snapshot.snapshotRevision)")
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
        case .repository(let repositoryId):
            if let snapshot = model.snapshot, let repository = snapshot.repository(withId: repositoryId) {
                RepositorySettingsView(model: model, repository: repository, snapshot: snapshot)
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
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                ContentUnavailableView {
                    Label("No repositories configured", systemImage: "arrow.triangle.branch")
                } description: {
                    Text("Add a repository to the private configuration, validate it, and accept the revision. Connection Doctor inspects daemon setup.")
                } actions: {
                    Button("Open Connection Doctor") { model.selection = .connectionDoctor }
                }
                ConfigurationStatusCard(model: model)
            }
            .padding(28)
            .frame(maxWidth: 1_180, alignment: .leading)
        }
        .frame(maxWidth: .infinity, alignment: .top)
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
                showsCleanup = true
                Task { await model.planCleanup() }
            } label: {
                Label("Cleanup…", systemImage: "trash.slash")
            }
            .disabled(model.isFixtureMode || !model.lifecycleState.isOperational)
            .help("Review positively owned resources before removing anything")
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
        case .repository(let id):
            return model.snapshot?.repository(withId: id).map { "\($0.displayName) settings" } ?? "Repository"
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
    let transition: EnvironmentActionStage?
    let alertCount: Int
    let loadsLiveJira: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(treeStatusColor)
                .frame(width: 10, height: 10)
                .padding(.top, 4)
                .help(treeStatusLabel)
            VStack(alignment: .leading, spacing: 2) {
                Text(worktree.branch ?? URL(fileURLWithPath: worktree.path).lastPathComponent)
                    .font(.callout.weight(.medium))
                    .lineLimit(2)
                HStack(spacing: 8) {
                    PullRequestCompactStatus(observation: worktree.pullRequest)
                    JiraIssueCompactStatus(worktree: worktree, loadsLiveData: loadsLiveJira)
                }
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
    }

    private var treeStatusColor: Color {
        if transition != nil { return .blue }
        guard let environment else { return .secondary }
        switch environment.observedState {
        case .starting, .stopping: return .blue
        case .failed, .exited: return .red
        case .orphaned, .degraded, .unverifiable: return .orange
        case .running: return environment.observedState.tint
        case .unknown, .stopped: return .secondary
        }
    }

    private var treeStatusLabel: String {
        if let transition { return transition.rawValue.capitalized }
        return environment?.observedState.label ?? "Stopped"
    }
}

private struct SidebarRow<Content: View>: View {
    let isSelected: Bool
    let action: () -> Void
    let content: Content
    @State private var hovered = false

    init(isSelected: Bool, action: @escaping () -> Void, @ViewBuilder content: () -> Content) {
        self.isSelected = isSelected
        self.action = action
        self.content = content()
    }

    var body: some View {
        content
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 8)
            .padding(.vertical, 7)
            .background(background, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 7, style: .continuous))
            .onTapGesture(perform: action)
            .onHover { hovered = $0 }
            .accessibilityAddTraits(.isButton)
            .accessibilityAddTraits(isSelected ? .isSelected : [])
    }

    private var background: Color {
        if isSelected { return Color.secondary.opacity(0.18) }
        if hovered { return Color.secondary.opacity(0.09) }
        return .clear
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
    @State private var showsCreateWorktree = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                overviewHeader
                if let snapshot = model.snapshot {
                    StartEnvironmentView(model: model, snapshot: snapshot)
                    ConfigurationStatusCard(model: model)
                    RepositoryInventoryView(model: model, snapshot: snapshot)
                    PullRequestOverviewView(model: model, snapshot: snapshot)
                    GlobalOperationsView(snapshot: snapshot)
                    GlobalAlertsView(snapshot: snapshot)
                    SnapshotMetadataView(snapshot: snapshot)
                }
            }
            .padding(28)
            .frame(maxWidth: 1_180, alignment: .leading)
            .switchyardScrollbars()
        }
        .frame(maxWidth: .infinity, alignment: .top)
        .sheet(isPresented: $showsCreateWorktree) {
            if let repositories = model.snapshot?.repositories {
                CreateWorktreeSheet(
                    model: model,
                    repositories: repositories,
                    isPresented: $showsCreateWorktree
                )
            }
        }
    }

    private var overviewHeader: some View {
        VStack(alignment: .leading, spacing: 16) {
            // At the window's minimum width the title and both actions do not
            // fit on one line; fall back to stacking the actions under the
            // title instead of hyphenating "Switchyard" and truncating buttons.
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .center, spacing: 12) {
                    overviewTitle
                    Spacer(minLength: 24)
                    overviewActions
                }
                VStack(alignment: .leading, spacing: 12) {
                    overviewTitle
                    overviewActions
                }
            }
            if let summary = model.summary {
                ScrollView(.horizontal) {
                    HStack(spacing: 0) {
                        NativeMetric(value: "\(summary.environmentCount)", label: "Environments")
                        NativeMetric(value: "\(summary.runningCount)", label: "Running", tint: .green)
                        NativeMetric(value: "\(summary.attentionCount)", label: "Attention", tint: summary.attentionCount > 0 ? .orange : .primary)
                        NativeMetric(value: Format.cpu(summary.totalCPUPercent), label: "Aggregate CPU")
                        NativeMetric(value: Format.memory(summary.totalMemoryBytes), label: "Memory", showsDivider: false)
                    }
                    .frame(minWidth: 650)
                    .switchyardScrollbars()
                }
                .padding(.vertical, 14)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
        }
    }
}

extension OverviewView {
    fileprivate var overviewTitle: some View {
        HStack(alignment: .center, spacing: 12) {
            Image(systemName: model.lifecycleState.systemImage)
                .font(.title2)
                .foregroundStyle(model.lifecycleState.tint)
            VStack(alignment: .leading, spacing: 2) {
                Text("Switchyard")
                    .font(.largeTitle.bold())
                    .lineLimit(1)
                    .fixedSize()
                Text(model.lifecycleState.summary)
                    .foregroundStyle(.secondary)
            }
        }
    }

    fileprivate var overviewActions: some View {
        HStack(spacing: 8) {
            Button {
                showsCreateWorktree = true
            } label: {
                Label("New worktree", systemImage: "plus")
            }
            .disabled(!model.canSubmitWorkspaceAction || model.snapshot?.repositories.isEmpty != false)
            Button("Connection Doctor") { model.selection = .connectionDoctor }
        }
        .fixedSize()
    }
}

private struct CreateWorktreeSheet: View {
    @Bindable var model: AppModel
    let repositories: [Repository]
    @Binding var isPresented: Bool
    @State private var repositoryId: String
    @State private var branch = ""
    @State private var startPoint = ""

    init(model: AppModel, repositories: [Repository], isPresented: Binding<Bool>) {
        self.model = model
        self.repositories = repositories
        self._isPresented = isPresented
        self._repositoryId = State(initialValue: repositories.first?.id ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Create managed worktree")
                    .font(.title2.bold())
                Text("Switchyard creates the branch, owns the worktree, and prepares it on first start.")
                    .foregroundStyle(.secondary)
            }
            Form {
                Picker("Repository", selection: $repositoryId) {
                    ForEach(repositories) { repository in
                        Text(repository.displayName).tag(repository.id)
                    }
                }
                TextField("Branch", text: $branch, prompt: Text("feature/PROJ-000-description"))
                    .textFieldStyle(.roundedBorder)
                TextField("Base (optional)", text: $startPoint, prompt: Text("origin/main"))
                    .textFieldStyle(.roundedBorder)
            }
            .formStyle(.grouped)
            HStack {
                Button("Cancel") { isPresented = false }
                Spacer()
                Button {
                    let selectedRepository = repositoryId
                    let selectedBranch = branch.trimmingCharacters(in: .whitespacesAndNewlines)
                    let selectedBase = startPoint.trimmingCharacters(in: .whitespacesAndNewlines)
                    Task {
                        await model.createWorktree(
                            repositoryId: selectedRepository,
                            branch: selectedBranch,
                            startPoint: selectedBase.isEmpty ? nil : selectedBase
                        )
                        if model.workspaceActionFailureMessage == nil { isPresented = false }
                    }
                } label: {
                    if model.workspaceActionState.isActive {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Create worktree")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(branch.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ||
                    repositoryId.isEmpty || !model.canSubmitWorkspaceAction)
            }
        }
        .padding(24)
        .frame(width: 520)
    }
}

private struct PullRequestOverviewView: View {
    @Bindable var model: AppModel
    let snapshot: StatusSnapshot

    private var rows: [(Repository, Worktree, PullRequestObservation)] {
        snapshot.repositories.flatMap { repository in
            repository.worktrees.compactMap { worktree in
                worktree.pullRequest.map { (repository, worktree, $0) }
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Pull requests")
                    .font(.title3.weight(.semibold))
                Text("\(rows.count(where: { $0.2.status == .found }))")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 7)
                    .padding(.vertical, 3)
                    .background(.quaternary, in: Capsule())
                Spacer()
                Text("Activity-aware GitHub checks")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if rows.isEmpty {
                Text("Waiting for the first GitHub observation.")
                    .foregroundStyle(.secondary)
            }
            ForEach(Array(rows.enumerated()), id: \.element.1.id) { index, row in
                Button {
                    if let environment = snapshot.environment(for: row.1) {
                        model.selection = .environment(environment.id)
                    } else {
                        model.selection = .worktree(repositoryId: row.0.id, worktreeId: row.1.id)
                    }
                } label: {
                    HStack(spacing: 10) {
                        Image(systemName: "arrow.triangle.branch")
                            .foregroundStyle(.secondary)
                            .frame(width: 17)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(row.1.branch ?? "Detached HEAD")
                                .font(.callout.weight(.medium))
                                .lineLimit(1)
                            Text(row.0.displayName)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        if row.2.status == .found, let pullRequest = row.2.pullRequest {
                            Text(pullRequest.draft ? "Draft" : pullRequest.state.rawValue.capitalized)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        } else if row.2.status == .none {
                            Text("No PR").font(.caption).foregroundStyle(.secondary)
                        }
                        PullRequestCompactStatus(observation: row.2)
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                if index < rows.count - 1 { Divider() }
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
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
                RepositoryInventoryCard(model: model, repository: repository, snapshot: snapshot)
            }
        }
    }
}

private struct RepositoryInventoryCard: View {
    @Bindable var model: AppModel
    let repository: Repository
    let snapshot: StatusSnapshot
    @State private var expanded = false

    var body: some View {
        FullWidthDisclosure(isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 2) {
                Text(repository.displayName)
                    .font(.headline)
                Text("\(pluralized(repository.worktrees.count, "worktree")) · profile \(repository.profileKey)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            RepositoryAcceptanceBadge(state: model.acceptanceState(for: repository))
            Button {
                model.selection = .repository(repository.id)
            } label: {
                Label("Settings", systemImage: "gearshape")
            }
            .buttonStyle(.borderless)
            .help("Open repository settings")
        } content: {
            VStack(alignment: .leading, spacing: 10) {
                KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true)
                KeyValueRow(key: "Remote", value: repository.remote, monospaced: true)
                KeyValueRow(key: "Profile key", value: repository.profileKey, monospaced: true)
                KeyValueRow(key: "Repository ID", value: repository.id, monospaced: true)
                if let runtime = repository.runtime {
                    KeyValueRow(key: "Default target", value: runtime.defaultTargetId)
                    KeyValueRow(
                        key: "Runtime catalog",
                        value: "\(runtime.targets.count) targets · \(runtime.services.count) services"
                    )
                }
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
                                JiraIssueBadge(worktree: worktree)
                                Text(worktree.path)
                                    .font(.caption.monospaced())
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                            }
                            Spacer()
                            if let changes = worktree.changes {
                                LineChangeBadges(committed: changes.committed, uncommitted: changes.uncommitted)
                            }
                            PullRequestCompactStatus(observation: worktree.pullRequest)
                            Text(snapshot.environment(for: worktree)?.observedState.label ?? "Stopped")
                                .font(.caption)
                                .foregroundStyle(snapshot.environment(for: worktree)?.observedState.tint ?? .secondary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.top, 12)
            .padding(.bottom, 2)
        }
        .padding(14)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

private struct GlobalOperationsView: View {
    let snapshot: StatusSnapshot

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("All operations")
                .font(.headline)
            OperationTable(operations: snapshot.operations, snapshot: snapshot)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.bottom, 2)
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
    @State private var expanded = false

    var body: some View {
        FullWidthDisclosure(isExpanded: $expanded) {
            Label("Daemon & state metadata", systemImage: "waveform.path.ecg")
                .font(.headline)
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Daemon state", value: snapshot.daemon.state.rawValue)
                KeyValueRow(key: "Daemon version", value: snapshot.daemon.version, monospaced: true)
                KeyValueRow(key: "Daemon instance", value: snapshot.daemon.instanceId, monospaced: true)
                KeyValueRow(key: "Daemon started", value: snapshot.daemon.startedAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Schema", value: "\(snapshot.schemaVersion)", monospaced: true)
                KeyValueRow(key: "State revision", value: "\(snapshot.snapshotRevision)", monospaced: true)
                KeyValueRow(key: "Generated", value: snapshot.generatedAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 10)
        }
        .font(.callout)
        .padding(.vertical, 4)
    }
}
