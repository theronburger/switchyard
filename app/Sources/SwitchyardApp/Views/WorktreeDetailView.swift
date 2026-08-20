import SwiftUI
import SwitchyardKit

struct WorktreeDetailView: View {
    @Bindable var model: AppModel
    let repository: Repository
    let worktree: Worktree
    let snapshot: StatusSnapshot
    @State private var worktreeExpanded = true
    @State private var repositoryExpanded = false
    @State private var confirmsArchive = false
    @State private var confirmsAdopt = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                header
                Divider()
                StartEnvironmentView(model: model, snapshot: snapshot, initialWorktreeId: worktree.id)
                JiraIssueView(worktree: worktree, loadsLiveData: !model.isFixtureMode)
                GitHubPullRequestView(worktree: worktree)
                serviceChanges
                operations
                workspaceReadiness
                worktreeFacts
                repositoryFacts
            }
            .padding(28)
            .frame(maxWidth: 1_080, alignment: .leading)
            .switchyardScrollbars()
        }
        .frame(maxWidth: .infinity, alignment: .top)
        .confirmationDialog(
            "Archive this managed worktree?",
            isPresented: $confirmsArchive,
            titleVisibility: .visible
        ) {
            Button("Archive worktree", role: .destructive) {
                Task { await model.archiveWorktree(worktree) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Switchyard will refuse if the worktree is dirty, has unpushed commits, or still has an active environment.")
        }
        .confirmationDialog(
            "Adopt this existing worktree?",
            isPresented: $confirmsAdopt,
            titleVisibility: .visible
        ) {
            Button("Adopt worktree") {
                Task { await model.adoptWorktree(worktree) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Switchyard will take ownership only if this is a clean, pushed linked checkout inside the repository's managed worktree folder.")
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: "arrow.triangle.branch")
                    .font(.title2)
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 8) {
                        Text(worktree.branch ?? "Detached HEAD")
                            .font(.largeTitle.bold())
                        CopyValueButton(value: worktree.branch ?? worktree.headRevision, label: "branch")
                    }
                    JiraIssueBadge(worktree: worktree)
                    Text(repository.displayName)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                Spacer()
                StartCodexTaskButton(model: model, worktree: worktree)
                OpenInZedButton(worktree: worktree)
                if worktree.workspace?.ownership == .adopted && !worktree.isPrimary {
                    Button {
                        confirmsAdopt = true
                    } label: {
                        Label("Adopt", systemImage: "checkmark.seal")
                    }
                    .disabled(!model.canSubmitWorkspaceAction)
                }
                if worktree.workspace?.ownership == .managed && !worktree.isPrimary {
                    Button(role: .destructive) {
                        confirmsArchive = true
                    } label: {
                        Label("Archive", systemImage: "archivebox")
                    }
                    .disabled(!model.canSubmitWorkspaceAction)
                }
                Text("Stopped")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }

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
            .padding(12)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))

            ScrollView(.horizontal) {
                HStack(spacing: 0) {
                    NativeMetric(value: worktree.isPrimary ? "Primary" : "Linked", label: "Checkout")
                    NativeMetric(value: Format.shortRevision(worktree.headRevision), label: "Head")
                    NativeMetric(value: worktree.git.isClean ? "Clean" : "Changes", label: "Git state", tint: worktree.git.isClean ? .green : .orange)
                    LineChangeMetric(
                        changes: worktree.changes?.committed,
                        label: "Branch",
                        systemImage: "arrow.triangle.branch"
                    )
                    LineChangeMetric(
                        changes: worktree.changes?.uncommitted,
                        label: "Working tree",
                        systemImage: "laptopcomputer",
                        showsDivider: false
                    )
                }
                .frame(minWidth: 730)
                .switchyardScrollbars()
            }
            .padding(.vertical, 14)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }

    @ViewBuilder
    private var serviceChanges: some View {
        if let changes = worktree.changes {
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("Changes by area")
                        .font(.headline)
                    Spacer()
                    Text("base \(Format.shortRevision(changes.baseRevision))")
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
                let changedServices = repository.runtime?.services.compactMap { service -> (RuntimeService, ServiceLineChanges)? in
                    guard let serviceChanges = changes.service(service.id),
                          !serviceChanges.committed.isEmpty || !serviceChanges.uncommitted.isEmpty else { return nil }
                    return (service, serviceChanges)
                } ?? []
                if changedServices.isEmpty && changes.sharedCommitted.isEmpty && changes.sharedUncommitted.isEmpty {
                    Text("No line changes from the configured base or working tree.")
                        .foregroundStyle(.secondary)
                }
                ForEach(changedServices, id: \.0.id) { service, serviceChanges in
                    HStack(spacing: 10) {
                        Image(systemName: service.kind == "web" ? "macwindow" : "network")
                            .foregroundStyle(.secondary)
                            .frame(width: 18)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(service.displayName)
                                .font(.callout.weight(.medium))
                            Text("\(serviceChanges.committed.files) committed files · \(serviceChanges.uncommitted.files) uncommitted files")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        LineChangeBadges(committed: serviceChanges.committed, uncommitted: serviceChanges.uncommitted)
                    }
                    .padding(.vertical, 3)
                }
                if !changes.sharedCommitted.isEmpty || !changes.sharedUncommitted.isEmpty {
                    HStack(spacing: 10) {
                        Image(systemName: "square.stack.3d.up")
                            .foregroundStyle(.secondary)
                            .frame(width: 18)
                        VStack(alignment: .leading, spacing: 1) {
                            Text("Shared")
                                .font(.callout.weight(.medium))
                            Text("\(changes.sharedCommitted.files) committed files · \(changes.sharedUncommitted.files) uncommitted files")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        Spacer()
                        LineChangeBadges(committed: changes.sharedCommitted, uncommitted: changes.sharedUncommitted)
                    }
                    .padding(.vertical, 3)
                }
            }
            .padding(16)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }

    private var worktreeFacts: some View {
        FullWidthDisclosure(isExpanded: $worktreeExpanded) {
            Label("Complete worktree state", systemImage: "arrow.triangle.branch")
                .font(.headline)
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Path", value: worktree.path, monospaced: true, copyable: true)
                KeyValueRow(key: "Branch", value: worktree.branch ?? "Detached HEAD", monospaced: true, copyable: true)
                KeyValueRow(key: "Head revision", value: worktree.headRevision, monospaced: true, copyable: true)
                KeyValueRow(key: "Worktree ID", value: worktree.id, monospaced: true, copyable: true)
                KeyValueRow(key: "Primary checkout", value: worktree.isPrimary ? "Yes" : "No")
                Divider()
                GitStateDetail(state: worktree.git)
                if let changes = worktree.changes {
                    Divider()
                    KeyValueRow(key: "Comparison base", value: changes.baseRevision, monospaced: true, copyable: true)
                    HStack {
                        Text("Line changes").foregroundStyle(.secondary)
                        Spacer()
                        LineChangeBadges(committed: changes.committed, uncommitted: changes.uncommitted)
                    }
                }
            }
            .padding(.top, 12)
        }
    }

    private var workspaceReadiness: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Label("Workspace readiness", systemImage: "checkmark.seal")
                    .font(.headline)
                Spacer()
                if let workspace = worktree.workspace {
                    Text(workspace.state.rawValue.capitalized)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(workspace.state == .ready ? .green : .secondary)
                } else {
                    Text("Not prepared")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                }
            }
            if let workspace = worktree.workspace {
                KeyValueRow(key: "Ownership", value: workspace.ownership.rawValue.capitalized)
                KeyValueRow(key: "Prepared", value: Format.relative(workspace.preparedAt))
                KeyValueRow(key: "Input fingerprint", value: workspace.fingerprint, monospaced: true, copyable: true)
                ForEach(workspace.toolchains) { toolchain in
                    KeyValueRow(
                        key: "Toolchain · \(toolchain.id)",
                        value: "\(toolchain.resolvedVersion) (requested \(toolchain.requestedVersion))",
                        monospaced: true
                    )
                }
            } else {
                Text("The next environment start will verify toolchains and hydrate this worktree before building services.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private var repositoryFacts: some View {
        FullWidthDisclosure(isExpanded: $repositoryExpanded) {
            Label("Repository identity", systemImage: "externaldrive")
                .font(.headline)
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Name", value: repository.displayName)
                KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true, copyable: true)
                KeyValueRow(key: "Remote", value: repository.remote, monospaced: true, copyable: true)
                KeyValueRow(key: "Profile key", value: repository.adapter, monospaced: true)
                KeyValueRow(key: "Repository ID", value: repository.id, monospaced: true, copyable: true)
                HStack {
                    Text("Configuration").foregroundStyle(.secondary)
                    Spacer()
                    RepositoryAcceptanceBadge(state: model.acceptanceState(for: repository))
                    Button("Settings") { model.selection = .repository(repository.id) }
                        .buttonStyle(.borderless)
                }
                .font(.callout)
                if let runtime = repository.runtime {
                    KeyValueRow(key: "Default target", value: runtime.defaultTargetId)
                    KeyValueRow(key: "Targets", value: runtime.targets.map(\.displayName).joined(separator: ", "))
                    KeyValueRow(key: "Services", value: "\(runtime.services.count) catalogued")
                }
            }
            .padding(.top, 12)
        }
    }

    private var operations: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Operations")
                .font(.headline)
            OperationTable(
                operations: snapshot.environment(for: worktree).map {
                    snapshot.operations(forEnvironment: $0.id)
                } ?? [],
                snapshot: snapshot
            )
        }
        .padding(.bottom, 2)
    }
}

struct GitStateDetail: View {
    let state: WorktreeState

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack {
                Text("Git state").foregroundStyle(.secondary)
                Spacer()
                GitStateChips(state: state)
            }
            KeyValueRow(key: "Tracked changes", value: state.hasTrackedChanges ? "Yes" : "No")
            KeyValueRow(key: "Untracked files", value: state.hasUntrackedFiles ? "Yes" : "No")
            KeyValueRow(key: "Unpushed commits", value: state.hasUnpushedCommits ? "Yes" : "No")
            KeyValueRow(key: "Locked", value: state.locked ? "Yes" : "No")
            KeyValueRow(key: "Prunable", value: state.prunable ? "Yes" : "No")
        }
        .font(.callout)
    }
}
