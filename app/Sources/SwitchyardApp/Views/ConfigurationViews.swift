import AppKit
import SwiftUI
import SwitchyardKit
import UniformTypeIdentifiers

/// Global private-configuration state: exact accepted revision and digest,
/// the pending candidate, and the validate/accept controls (D-025).
struct ConfigurationStatusCard: View {
    @Bindable var model: AppModel
    var showsAddRepository = true
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
            AddRepositorySheet(model: model, isPresented: $presentsAddRepository)
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
        case .loaded, .validating, .accepting, .failed:
            if let presentation = model.configurationPresentation {
                Text(presentation.summary)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                VStack(alignment: .leading, spacing: 7) {
                    KeyValueRow(key: "Accepted revision", value: "\(presentation.status.acceptedRevision)", monospaced: true)
                    if let digest = presentation.status.acceptedDigest, !digest.isEmpty {
                        KeyValueRow(key: "Accepted digest", value: digest, monospaced: true, copyable: true)
                    }
                    if let candidate = presentation.status.candidate {
                        Divider()
                        KeyValueRow(key: "Pending candidate", value: candidate.digest, monospaced: true, copyable: true)
                        KeyValueRow(key: "Desired file digest", value: candidate.sourceDigest, monospaced: true, copyable: true)
                        KeyValueRow(key: "Compiler", value: candidate.compilerVersion, monospaced: true)
                        KeyValueRow(key: "Staged", value: candidate.stagedAt.formatted(date: .abbreviated, time: .standard))
                        FullWidthDisclosure(isExpanded: $showsCandidateDetail) {
                            Text("Revision preview · \(candidate.repositoryDigests.count) repositories · \(candidate.executableDigests.count) executables")
                                .font(.callout.weight(.medium))
                            Spacer()
                        } content: {
                            CandidatePreview(candidate: candidate, publishedKeys: model.publishedProfileKeys)
                                .padding(.top, 8)
                        }
                    }
                }
                KeyValueRow(key: "Desired file", value: PrivateConfigurationLocation.standard().file.path, monospaced: true, copyable: true)
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
                .disabled(!model.canReadConfiguration)
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

/// Collects generic repository inputs, renders the exact YAML entry, and
/// hands off to validation and acceptance. The app never writes into the
/// selected checkout and, until the daemon offers a CAS write endpoint, does
/// not edit configuration.yaml either.
struct AddRepositorySheet: View {
    @Bindable var model: AppModel
    @Binding var isPresented: Bool
    @State private var draft = RepositoryConfigurationDraft()
    @State private var copied = false

    private var problems: [RepositoryConfigurationDraft.Problem] {
        draft.problems(existingKeys: model.configuredRepositoryKeys)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Add repository")
                    .font(.title2.bold())
                Text("Describe the checkout once. Switchyard stores repository behavior only in its private configuration and never adds files to the repository.")
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Form {
                HStack {
                    TextField("Repository root", text: $draft.rootPath, prompt: Text("/absolute/path/to/checkout"))
                        .textFieldStyle(.roundedBorder)
                    Button("Choose…") { chooseRoot() }
                }
                TextField("Repository key", text: $draft.key, prompt: Text("stable-opaque-key"))
                    .textFieldStyle(.roundedBorder)
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
                    Text(ConfigurationAcceptancePresentation.writeGap)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
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

            HStack {
                Button("Cancel") { isPresented = false }
                RevealConfigurationButton()
                Spacer()
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(draft.yamlSnippet, forType: .string)
                    copied = true
                } label: {
                    Label(copied ? "Copied entry" : "Copy entry", systemImage: copied ? "checkmark" : "doc.on.doc")
                }
                .disabled(!problems.isEmpty)
                Button {
                    Task {
                        await model.validateConfiguration()
                        if model.configurationState.failureMessage == nil { isPresented = false }
                    }
                } label: {
                    if model.configurationState.isBusy {
                        ProgressView().controlSize(.small)
                    } else {
                        Text("Validate configuration")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(!problems.isEmpty || !model.canMutateConfiguration)
                .help("Validate configuration.yaml at revision \(model.configurationPresentation?.expectedRevision ?? 0), then accept the staged candidate from the overview")
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
                ConfigurationStatusCard(model: model, showsAddRepository: false)
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
            KeyValueRow(key: "Profile key", value: repository.adapter, monospaced: true, copyable: true)
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
               let digest = candidate.repositoryDigests[repository.adapter] {
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
            Text("Edit Configuration, Disable, and Remove change the private desired file. The daemon has no repository-level write endpoint yet, so edit the entry under repositories: \(repository.adapter) in configuration.yaml, then validate and accept the new revision. A disabled repository keeps stop and cleanup but refuses new starts; removal is rejected while any resource still references the binding.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
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
