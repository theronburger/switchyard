import AppKit
import SwiftUI
import SwitchyardKit
import UniformTypeIdentifiers

/// Global private-configuration state: exact accepted revision and digest,
/// the pending candidate, and the validate/accept controls (D-025).
struct ConfigurationStatusCard: View {
    @Bindable var model: AppModel
    var showsAddRepository = true
    var showsEntries = true
    @State private var presentsAddRepository = false
    @State private var showsCandidateDetail = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Label("Private configuration", systemImage: "doc.badge.gearshape")
                    .font(.title3.weight(.semibold))
                Spacer()
                if let presentation = model.configurationPresentation {
                    ConfigurationStateBadge(state: presentation.status.state)
                }
            }
            content
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .sheet(isPresented: $presentsAddRepository) {
            RepositoryEntrySheet(model: model, mode: .add, isPresented: $presentsAddRepository)
        }
        .alert(
            "Configuration action failed",
            isPresented: Binding(
                get: { model.configurationState.failureMessage != nil },
                set: { presented in if !presented { model.dismissConfigurationFailure() } }
            )
        ) {
            Button("OK") { model.dismissConfigurationFailure() }
        } message: {
            Text(model.configurationState.failureMessage ?? "The configuration request could not complete.")
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.configurationState {
        case .idle, .loading:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text(model.canReadConfiguration ? "Reading configuration state…" : "Configuration state is available once the daemon is reachable.")
                    .foregroundStyle(.secondary)
            }
        case .loaded, .validating, .accepting, .editing, .failed:
            if let presentation = model.configurationPresentation {
                Text(presentation.summary)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Text(presentation.desiredFileSummary)
                    .font(.callout)
                    .foregroundStyle(presentation.status.desired?.problem == nil ? Color.secondary : Color.orange)
                    .fixedSize(horizontal: false, vertical: true)
                VStack(alignment: .leading, spacing: 7) {
                    KeyValueRow(key: "Accepted revision", value: "\(presentation.status.acceptedRevision)", monospaced: true)
                    if let digest = presentation.status.acceptedDigest, !digest.isEmpty {
                        KeyValueRow(key: "Accepted digest", value: digest, monospaced: true, copyable: true)
                    }
                    if let candidate = presentation.status.candidate {
                        Divider()
                        KeyValueRow(key: "Pending candidate", value: candidate.digest, monospaced: true, copyable: true)
                        KeyValueRow(key: "Candidate source digest", value: candidate.sourceDigest, monospaced: true, copyable: true)
                        KeyValueRow(key: "Compiler", value: candidate.compilerVersion, monospaced: true)
                        KeyValueRow(key: "Staged", value: candidate.stagedAt.formatted(date: .abbreviated, time: .standard))
                        FullWidthDisclosure(isExpanded: $showsCandidateDetail) {
                            Text("Revision preview · \(pluralized(candidate.repositoryDigests.count, "repository", "repositories")) · \(pluralized(candidate.executableDigests.count, "executable"))")
                                .font(.callout.weight(.medium))
                            Spacer()
                        } content: {
                            CandidatePreview(candidate: candidate, publishedKeys: model.publishedProfileKeys)
                                .padding(.top, 8)
                        }
                    }
                }
                KeyValueRow(key: "Desired file", value: PrivateConfigurationLocation.standard().file.path, monospaced: true, copyable: true)
                if let desired = presentation.status.desired, let digest = desired.sourceDigest {
                    KeyValueRow(key: "Desired file digest", value: digest, monospaced: true, copyable: true)
                }
                if showsEntries, let desired = presentation.status.desired, !desired.repositories.isEmpty {
                    Divider()
                    Text("Repository entries")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                    ForEach(desired.repositories) { entry in
                        DesiredRepositoryRow(model: model, entry: entry)
                    }
                }
            } else {
                Text("The daemon has not answered a configuration read yet.")
                    .foregroundStyle(.secondary)
            }
            controls
        }
    }

    private var controls: some View {
        HStack(spacing: 10) {
            if showsAddRepository {
                Button {
                    presentsAddRepository = true
                } label: {
                    Label("Add Repository…", systemImage: "plus.rectangle.on.folder")
                }
                .disabled(!model.canEditRepositoryConfiguration)
                .help("Describe a checkout; the daemon writes the entry into its private configuration.yaml and stages a candidate for acceptance")
            }
            RevealConfigurationButton()
            Spacer()
            if model.configurationState.isBusy {
                ProgressView().controlSize(.small)
            }
            Button {
                Task { await model.validateConfiguration() }
            } label: {
                Label("Validate", systemImage: "checkmark.shield")
            }
            .disabled(!model.canMutateConfiguration)
            .help("Validate configuration.yaml against accepted revision \(model.configurationPresentation?.expectedRevision ?? 0) and stage an exact candidate")
            if let presentation = model.configurationPresentation, let candidate = presentation.status.candidate {
                Button {
                    Task { await model.acceptConfiguration(candidateDigest: candidate.digest) }
                } label: {
                    Label("Accept revision", systemImage: "checkmark.seal.fill")
                }
                .buttonStyle(.borderedProminent)
                .disabled(!model.canMutateConfiguration || !presentation.canAccept)
                .help("Accept \(candidate.digest) at expected revision \(presentation.expectedRevision). The daemon restarts into the new revision.")
            }
        }
    }
}

struct ConfigurationStateBadge: View {
    let state: ConfigurationState

    var body: some View {
        Text(label)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(tint.opacity(0.16), in: Capsule())
            .foregroundStyle(tint)
    }

    private var label: String {
        switch state {
        case .accepted: "Accepted"
        case .pending: "Pending acceptance"
        case .missing: "Not accepted"
        case .unknown: "Unknown"
        }
    }

    private var tint: Color {
        switch state {
        case .accepted: .green
        case .pending: .orange
        case .missing: .secondary
        case .unknown: .red
        }
    }
}

struct RepositoryAcceptanceBadge: View {
    let state: RepositoryAcceptanceState

    var body: some View {
        Text(state.label)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(tint.opacity(0.16), in: Capsule())
            .foregroundStyle(tint)
    }

    private var tint: Color {
        switch state {
        case .accepted: .green
        case .pendingChange, .pendingAddition: .orange
        case .unknown: .secondary
        }
    }
}

private struct CandidatePreview: View {
    let candidate: ConfigurationCandidate
    let publishedKeys: Set<String>

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("Repository subtrees")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            ForEach(candidate.repositoryDigests.keys.sorted(), id: \.self) { key in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(key).font(.callout.monospaced())
                    RepositoryAcceptanceBadge(state: publishedKeys.contains(key) ? .pendingChange : .pendingAddition)
                    Spacer(minLength: 12)
                    Text(candidate.repositoryDigests[key] ?? "")
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .textSelection(.enabled)
                }
            }
            if !candidate.executableDigests.isEmpty {
                Divider()
                Text("Executable fingerprints authorized by acceptance")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                ForEach(candidate.executableDigests.keys.sorted(), id: \.self) { path in
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(path)
                            .font(.callout.monospaced())
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer(minLength: 12)
                        Text(candidate.executableDigests[path] ?? "")
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .textSelection(.enabled)
                    }
                }
            }
        }
    }
}

struct RevealConfigurationButton: View {
    var body: some View {
        Button {
            let location = PrivateConfigurationLocation.standard()
            if FileManager.default.fileExists(atPath: location.file.path) {
                NSWorkspace.shared.activateFileViewerSelecting([location.file])
            } else {
                NSWorkspace.shared.activateFileViewerSelecting([location.directory])
            }
        } label: {
            Label("Reveal configuration.yaml", systemImage: "folder")
        }
        .help("Reveal the private desired configuration under Application Support")
    }
}

/// One desired-file entry with its acceptance state and lifecycle actions.
/// Every action is a daemon mutation against the exact revision and file
/// digest the app last observed; none of them touches the checkout.
struct DesiredRepositoryRow: View {
    @Bindable var model: AppModel
    let entry: ConfigurationRepositoryEntry
    @State private var presentsEdit = false
    @State private var confirmsRemoval = false

    private var isPublished: Bool { model.publishedProfileKeys.contains(entry.key) }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 8) {
                    Text(entry.displayName).font(.callout.weight(.medium))
                    Text(entry.key).font(.caption.monospaced()).foregroundStyle(.secondary)
                    if !entry.enabled {
                        Text("Disabled")
                            .font(.caption2.weight(.semibold))
                            .padding(.horizontal, 6).padding(.vertical, 2)
                            .background(Color.secondary.opacity(0.16), in: Capsule())
                            .foregroundStyle(.secondary)
                    }
                    RepositoryAcceptanceBadge(
                        state: model.configurationPresentation?.repositoryState(profileKey: entry.key, isPublished: isPublished) ?? .unknown
                    )
                }
                Text(entry.root)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: 12)
            RepositoryEntryActions(
                model: model, entry: entry, presentsEdit: $presentsEdit, confirmsRemoval: $confirmsRemoval
            )
        }
        .sheet(isPresented: $presentsEdit) {
            RepositoryEntrySheet(model: model, mode: .edit(entry), isPresented: $presentsEdit)
        }
        .confirmationDialog(
            "Remove \(entry.displayName) from the private configuration?",
            isPresented: $confirmsRemoval,
            titleVisibility: .visible
        ) {
            Button("Remove entry", role: .destructive) {
                Task { await model.removeRepositoryConfiguration(key: entry.key) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("The daemon removes only the repositories: \(entry.key) entry from configuration.yaml and stages a candidate you still have to accept. The checkout at \(entry.root) is not touched. Removal is refused until the disabled entry is accepted and no environment or managed worktree still belongs to it.")
        }
    }
}

struct RepositoryEntryActions: View {
    @Bindable var model: AppModel
    let entry: ConfigurationRepositoryEntry
    @Binding var presentsEdit: Bool
    @Binding var confirmsRemoval: Bool

    var body: some View {
        HStack(spacing: 8) {
            Button("Edit…") { presentsEdit = true }
                .help("Change display name, remote, default base, or managed worktrees root. The root is bound to the key; remove and add to repoint.")
            Button(entry.enabled ? "Disable" : "Enable") {
                Task { await model.setRepositoryEnabled(key: entry.key, enabled: !entry.enabled) }
            }
            .help(entry.enabled
                ? "Stage a revision that keeps stop and cleanup for this repository but refuses new preparation and starts"
                : "Stage a revision that allows preparation and starts for this repository again")
            Button("Remove…", role: .destructive) { confirmsRemoval = true }
                .disabled(entry.enabled)
                .help(entry.enabled ? "Disable and accept that revision before removing" : "Remove the entry from configuration.yaml")
        }
        .controlSize(.small)
        .disabled(!model.canEditRepositoryConfiguration)
    }
}

/// Collects generic repository inputs and asks the daemon to write the entry.
/// The app never writes into the selected checkout or into configuration.yaml;
/// the daemon edits its private desired file under compare-and-swap and
/// stages a candidate the owner accepts from the configuration card.
struct RepositoryEntrySheet: View {
    enum Mode: Equatable {
        case add
        case edit(ConfigurationRepositoryEntry)

        var isEdit: Bool {
            if case .edit = self { return true }
            return false
        }
    }

    @Bindable var model: AppModel
    let mode: Mode
    @Binding var isPresented: Bool
    @State private var draft: RepositoryConfigurationDraft
    @State private var showsPreview = false

    init(model: AppModel, mode: Mode, isPresented: Binding<Bool>) {
        self.model = model
        self.mode = mode
        self._isPresented = isPresented
        switch mode {
        case .add:
            _draft = State(initialValue: RepositoryConfigurationDraft())
        case .edit(let entry):
            _draft = State(initialValue: RepositoryConfigurationDraft(entry: entry))
        }
    }

    private var problems: [RepositoryConfigurationDraft.Problem] {
        var existing = model.configuredRepositoryKeys
        if case .edit(let entry) = mode { existing.remove(entry.key) }
        return draft.problems(existingKeys: existing, requiresExistingRoot: !mode.isEdit)
    }

    private var expectedRevision: Int64 { model.configurationPresentation?.expectedRevision ?? 0 }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 4) {
                Text(mode.isEdit ? "Edit repository" : "Add repository")
                    .font(.title2.bold())
                Text(mode.isEdit
                    ? "Change the generic identity fields of this entry. Services, commands, and values in configuration.yaml stay exactly as they are."
                    : "Describe the checkout once. Switchyard stores repository behavior only in its private configuration and never adds files to the repository.")
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Form {
                HStack {
                    TextField("Repository root", text: $draft.rootPath, prompt: Text("/absolute/path/to/checkout"))
                        .textFieldStyle(.roundedBorder)
                        .disabled(mode.isEdit)
                    if !mode.isEdit {
                        Button("Choose…") { chooseRoot() }
                    }
                }
                TextField("Repository key", text: $draft.key, prompt: Text("stable-opaque-key"))
                    .textFieldStyle(.roundedBorder)
                    .disabled(mode.isEdit)
                TextField("Display name", text: $draft.displayName, prompt: Text("Shown in the sidebar"))
                    .textFieldStyle(.roundedBorder)
                TextField("Remote", text: $draft.remote, prompt: Text("origin"))
                    .textFieldStyle(.roundedBorder)
                TextField("Default base", text: $draft.defaultBase, prompt: Text("origin/main"))
                    .textFieldStyle(.roundedBorder)
                TextField("Managed worktrees root", text: $draft.managedWorktreesRoot, prompt: Text("/absolute/path/outside/the/checkout"))
                    .textFieldStyle(.roundedBorder)
                Toggle("Enabled", isOn: $draft.enabled)
            }
            .formStyle(.grouped)
            .frame(maxHeight: 330)
            if mode.isEdit {
                Text("The key and root are bound together for the life of the entry. To point a key at a different checkout, remove it and add a new one.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if !problems.isEmpty {
                VStack(alignment: .leading, spacing: 3) {
                    ForEach(problems, id: \.description) { problem in
                        Label(problem.description, systemImage: "exclamationmark.circle")
                            .font(.caption)
                            .foregroundStyle(.orange)
                    }
                }
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    Text(ConfigurationAcceptancePresentation.editFlow)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    FullWidthDisclosure(isExpanded: $showsPreview) {
                        Text("Entry the daemon will write")
                            .font(.caption.weight(.medium))
                        Spacer()
                    } content: {
                        ScrollView {
                            Text(draft.yamlSnippet)
                                .font(.caption.monospaced())
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .padding(8)
                        }
                        .frame(height: 120)
                        .background(.background, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                    }
                }
            }

            HStack {
                Button("Cancel") { isPresented = false }
                RevealConfigurationButton()
                Spacer()
                Button {
                    Task {
                        if await model.saveRepositoryConfiguration(draft) { isPresented = false }
                    }
                } label: {
                    if model.configurationState.isBusy {
                        ProgressView().controlSize(.small)
                    } else {
                        Text(mode.isEdit ? "Stage changes" : "Add repository")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(!problems.isEmpty || !model.canEditRepositoryConfiguration)
                .help("Write the entry through the daemon at revision \(expectedRevision) and stage a candidate; accept it from the configuration card")
            }
        }
        .padding(24)
        .frame(width: 640)
    }

    private func chooseRoot() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Choose"
        panel.message = "Choose the primary checkout of the repository."
        guard panel.runModal() == .OK, let url = panel.url else { return }
        let suggestion = RepositoryConfigurationDraft.suggested(forRootPath: url.path)
        draft.rootPath = suggestion.rootPath
        if draft.key.isEmpty { draft.key = suggestion.key }
        if draft.displayName.isEmpty { draft.displayName = suggestion.displayName }
        if draft.managedWorktreesRoot.isEmpty { draft.managedWorktreesRoot = suggestion.managedWorktreesRoot }
    }
}

/// Repository settings: identity, acceptance state, runtime catalog, and the
/// configuration lifecycle actions for one configured repository.
struct RepositorySettingsView: View {
    @Bindable var model: AppModel
    let repository: Repository
    let snapshot: StatusSnapshot
    @State private var showsCleanup = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                header
                ConfigurationStatusCard(model: model, showsAddRepository: false, showsEntries: false)
                identity
                runtimeCatalog
                lifecycle
            }
            .padding(28)
            .frame(maxWidth: 1_080, alignment: .leading)
            .switchyardScrollbars()
        }
        .frame(maxWidth: .infinity, alignment: .top)
        .sheet(isPresented: $showsCleanup, onDismiss: { model.dismissCleanup() }) {
            CleanupReviewSheet(model: model, isPresented: $showsCleanup)
        }
    }

    private var header: some View {
        HStack(alignment: .center, spacing: 12) {
            Image(systemName: "externaldrive.badge.gearshape")
                .font(.title2)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 3) {
                Text(repository.displayName)
                    .font(.largeTitle.bold())
                Text("Repository settings")
                    .foregroundStyle(.secondary)
            }
            Spacer()
            RepositoryAcceptanceBadge(state: model.acceptanceState(for: repository))
        }
    }

    private var identity: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("Identity").font(.headline)
            KeyValueRow(key: "Profile key", value: repository.profileKey, monospaced: true, copyable: true)
            KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true, copyable: true)
            KeyValueRow(key: "Remote", value: repository.remote, monospaced: true, copyable: true)
            KeyValueRow(key: "Repository ID", value: repository.id, monospaced: true, copyable: true)
            KeyValueRow(key: "Worktrees", value: "\(repository.worktrees.count)")
            if let observation = repository.observation {
                KeyValueRow(
                    key: "Observed",
                    value: observation.observedAt.map { Format.relative($0) } ?? "never"
                )
                if observation.stale {
                    KeyValueRow(key: "Freshness", value: "Stale · \(observation.errorCode ?? "unknown")")
                }
            }
            if let candidate = model.configurationState.status?.candidate,
               let digest = candidate.repositoryDigests[repository.profileKey] {
                KeyValueRow(key: "Candidate subtree digest", value: digest, monospaced: true, copyable: true)
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    @ViewBuilder
    private var runtimeCatalog: some View {
        VStack(alignment: .leading, spacing: 7) {
            Text("Compiled runtime catalog").font(.headline)
            if let runtime = repository.runtime {
                KeyValueRow(key: "Default target", value: runtime.defaultTargetId, monospaced: true)
                ForEach(runtime.targets) { target in
                    KeyValueRow(
                        key: "Target · \(target.id)",
                        value: "\(target.displayName) · \(target.risk)\(target.warnOnStart ? " · confirm on start" : "")"
                    )
                }
                Divider()
                ForEach(runtime.services) { service in
                    KeyValueRow(
                        key: "Service · \(service.id)",
                        value: service.available ? "\(service.displayName) · \(service.kind)" : "\(service.displayName) · unavailable"
                    )
                }
            } else {
                Text("The accepted revision publishes no runtime catalog for this repository.")
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private var lifecycle: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Configuration lifecycle").font(.headline)
            if let entry = model.desiredEntry(for: repository) {
                Text("Edit, Disable, Enable, and Remove ask the daemon to rewrite only the repositories: \(entry.key) entry in its private configuration.yaml, compared against the exact revision and file digest shown above, and stage a candidate you accept from the card. A disabled repository keeps stop and cleanup but refuses new preparation and starts. Removal is available once the disabled entry is accepted and no environment or managed worktree still belongs to the repository.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                DesiredRepositoryRow(model: model, entry: entry)
            } else {
                Text(model.configurationPresentation?.desiredFileSummary.appending(" This repository's entry is not editable here: the daemon publishes it from the accepted revision, but configuration.yaml does not currently contain a readable repositories: \(repository.profileKey) entry.") ?? "Repository edits are available once the daemon publishes the desired file state.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: 10) {
                RevealConfigurationButton()
                Button {
                    showsCleanup = true
                    Task { await model.planCleanup(scope: CleanupScope(kind: "repository", id: repository.id)) }
                } label: {
                    Label("Review cleanup for this repository…", systemImage: "trash.slash")
                }
                .disabled(model.isFixtureMode || !model.lifecycleState.isOperational)
                .help("Plan and review positively owned resources scoped to this repository before removing anything")
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}
