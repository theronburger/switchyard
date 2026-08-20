import AppKit
import SwiftUI
import SwitchyardKit

struct StartEnvironmentOptions {
    let defaultTargetId: String
    let targets: [RuntimeTarget]
    let services: [RuntimeService]

    init(repository: Repository?) {
        defaultTargetId = repository?.runtime?.defaultTargetId ?? ""
        targets = repository?.runtime?.targets ?? []
        services = repository?.runtime?.services ?? []
    }

    var availableServiceIDs: Set<String> {
        Set(services.filter(\.available).map(\.id))
    }

    func normalizedTarget(current: String, preferred: String?) -> String {
        if targets.contains(where: { $0.id == current }) { return current }
        let candidate = preferred ?? defaultTargetId
        if targets.contains(where: { $0.id == candidate }) { return candidate }
        return targets.first?.id ?? ""
    }

    func normalizedServices(current: Set<String>) -> Set<String> {
        let retained = current.intersection(availableServiceIDs)
        if !retained.isEmpty { return retained }
        let preferred = ["organizer", "nonprofit-service"].filter(availableServiceIDs.contains)
        if !preferred.isEmpty { return Set(preferred) }
        return Set(availableServiceIDs.sorted().prefix(1))
    }
}

struct StartEnvironmentView: View {
    @Bindable var model: AppModel
    let snapshot: StatusSnapshot
    @State private var selectedWorktreeId = ""
    @State private var selectedTargetId = ""
    @State private var selectedServiceIds = Set<String>()
    @State private var showServicePicker = false
    @State private var showTargetConfirmation = false
    private let initialTargetId: String?

    init(
        model: AppModel,
        snapshot: StatusSnapshot,
        initialWorktreeId: String? = nil,
        initialTargetId: String? = nil
    ) {
        self.model = model
        self.snapshot = snapshot
        self.initialTargetId = initialTargetId
        let orderedWorktrees = snapshot.repositories.flatMap { repository in
            repository.worktrees.map { worktree in
                (id: worktree.id, label: "\(repository.displayName) · \(worktree.branch ?? worktree.path)")
            }
        }
        .sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
        let selectedWorktreeId = initialWorktreeId ?? orderedWorktrees.first?.id ?? ""
        let repository = snapshot.repositories.first { repository in
            repository.worktrees.contains { $0.id == selectedWorktreeId }
        }
        let options = StartEnvironmentOptions(repository: repository)
        _selectedWorktreeId = State(initialValue: selectedWorktreeId)
        _selectedTargetId = State(
            initialValue: options.normalizedTarget(current: "", preferred: initialTargetId)
        )
        _selectedServiceIds = State(initialValue: options.normalizedServices(current: []))
    }

    var body: some View {
        SectionCard(title: "Start environment", systemImage: "play.circle") {
            if worktrees.isEmpty {
                Text("No discovered worktrees are available.")
                    .foregroundStyle(.secondary)
            } else {
                Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 10) {
                    GridRow {
                        Text("Worktree")
                            .foregroundStyle(.secondary)
                        Picker("Worktree", selection: $selectedWorktreeId) {
                            ForEach(worktrees) { option in
                                Text(option.label).tag(option.id)
                            }
                        }
                        .labelsHidden()
                        .pickerStyle(.menu)
                    }

                    GridRow {
                        Text("Target")
                            .foregroundStyle(.secondary)
                        Picker("Target environment", selection: $selectedTargetId) {
                            ForEach(targets) { target in
                                Text(target.displayName).tag(target.id)
                            }
                        }
                        .labelsHidden()
                        .pickerStyle(.menu)
                    }

                    GridRow {
                        Text("Services")
                            .foregroundStyle(.secondary)
                        HStack(spacing: 10) {
                            serviceMenu
                            Text(serviceCatalogSummary)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                            Spacer(minLength: 12)
                            startButton
                        }
                    }
                }

                if selectedTarget?.warnOnStart == true {
                    Label(targetWarningMessage, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption.weight(.medium))
                        .foregroundStyle(selectedTarget?.risk == "production" ? .red : .orange)
                }
            }
        }
        .confirmationDialog(
            "Start local services against \(selectedTarget?.displayName ?? "this target")?",
            isPresented: $showTargetConfirmation,
            titleVisibility: .visible
        ) {
            Button("Confirm and start", role: .destructive) {
                submit(confirmedTargetId: selectedTargetId)
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(targetConfirmationMessage)
        }
        .onAppear(perform: configureSelection)
        .onChange(of: worktrees.map(\.id)) { _, _ in
            configureSelection()
        }
        .onChange(of: selectedWorktreeId) { _, _ in
            configureRuntimeSelection()
        }
    }

    private var serviceMenu: some View {
        Button {
            showServicePicker.toggle()
        } label: {
            Label("\(selectedServiceIds.count) selected", systemImage: "server.rack")
                .padding(.horizontal, 6)
        }
        .buttonStyle(.borderless)
        .fixedSize()
        .popover(isPresented: $showServicePicker, arrowEdge: .bottom) {
            ServicePickerPopover(
                repository: selectedRepository,
                worktree: selectedWorktree,
                target: selectedTarget,
                services: services,
                selectedServiceIDs: $selectedServiceIds
            )
        }
    }

    private var startButton: some View {
        Button {
            if selectedTarget?.warnOnStart == true {
                showTargetConfirmation = true
            } else {
                submit(confirmedTargetId: nil)
            }
        } label: {
            if model.environmentTransition(forWorktreeId: selectedWorktreeId) != nil {
                Label("Starting…", systemImage: "arrow.triangle.2.circlepath")
            } else {
                Label("Start", systemImage: "play.fill")
            }
        }
        .buttonStyle(.borderedProminent)
        .tint(selectedTarget?.warnOnStart == true ? (selectedTarget?.risk == "production" ? .red : .orange) : .accentColor)
        .disabled(
            selectedWorktreeId.isEmpty ||
                selectedTargetId.isEmpty ||
                selectedServiceIds.isEmpty ||
                !model.canSubmitEnvironmentAction
        )
    }

    private var worktrees: [WorktreeOption] {
        snapshot.repositories.flatMap { repository in
            repository.worktrees.map { worktree in
                WorktreeOption(
                    id: worktree.id,
                    label: "\(repository.displayName) · \(worktree.branch ?? worktree.path)"
                )
            }
        }
        .sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
    }

    private var selectedRepository: Repository? {
        snapshot.repositories.first { repository in
            repository.worktrees.contains { $0.id == selectedWorktreeId }
        }
    }

    private var selectedWorktree: Worktree? {
        selectedRepository?.worktrees.first { $0.id == selectedWorktreeId }
    }

    private var targets: [RuntimeTarget] {
        options.targets
    }

    private var selectedTarget: RuntimeTarget? {
        targets.first { $0.id == selectedTargetId }
    }

    private var services: [RuntimeService] {
        options.services
    }

    private var options: StartEnvironmentOptions {
        StartEnvironmentOptions(repository: selectedRepository)
    }

    private var serviceCatalogSummary: String {
        "\(services.count(where: \.available)) available · \(services.count) catalogued"
    }

    private var targetWarningMessage: String {
        "\(selectedTarget?.displayName ?? "This target") requires confirmation on every start."
    }

    private var targetConfirmationMessage: String {
        "This cluster may read from or write to remote \(selectedTarget?.displayName ?? "target") systems. Confirmation applies only to this start."
    }

    private func configureSelection() {
        if !worktrees.contains(where: { $0.id == selectedWorktreeId }) {
            selectedWorktreeId = worktrees.first?.id ?? ""
        }
        configureRuntimeSelection()
    }

    private func configureRuntimeSelection() {
        selectedTargetId = options.normalizedTarget(current: selectedTargetId, preferred: initialTargetId)
        selectedServiceIds = options.normalizedServices(current: selectedServiceIds)
    }

    private func submit(confirmedTargetId: String?) {
        let orderedServices = services.map(\.id).filter(selectedServiceIds.contains)
        Task {
            await model.startEnvironment(
                worktreeId: selectedWorktreeId,
                targetId: selectedTargetId,
                confirmedTargetId: confirmedTargetId,
                serviceIds: orderedServices
            )
        }
    }
}

private struct WorktreeOption: Identifiable {
    let id: String
    let label: String
}

struct ServicePickerPopover: View {
    let repository: Repository?
    let worktree: Worktree?
    let target: RuntimeTarget?
    let services: [RuntimeService]
    @Binding var selectedServiceIDs: Set<String>

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 3) {
                Text("Services")
                    .font(.headline)
                Text("\(services.count(where: \.available)) available · \(services.count) catalogued")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 12)

            Divider()

            ScrollView {
                LazyVStack(spacing: 2) {
                    ForEach(services) { service in
                        if service.available {
                            availableRow(service)
                        } else {
                            UnavailableServiceRow(
                                service: service,
                                prompt: ServiceIsolationPrompt.make(
                                    repository: repository,
                                    worktree: worktree,
                                    target: target,
                                    service: service
                                )
                            )
                        }
                    }
                }
                .padding(8)
                .switchyardScrollbars()
            }
            .frame(height: min(CGFloat(services.count) * 42, 460))
        }
        .frame(width: 430)
    }

    private func availableRow(_ service: RuntimeService) -> some View {
        Button {
            if selectedServiceIDs.contains(service.id) {
                selectedServiceIDs.remove(service.id)
            } else {
                selectedServiceIDs.insert(service.id)
            }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: selectedServiceIDs.contains(service.id) ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selectedServiceIDs.contains(service.id) ? Color.accentColor : .secondary)
                serviceIdentity(service)
                Spacer()
                if let changes = worktree?.changes?.service(service.id) {
                    LineChangeBadges(committed: changes.committed, uncommitted: changes.uncommitted)
                }
                Text("Available")
                    .font(.caption)
                    .foregroundStyle(.green)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 7)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func serviceIdentity(_ service: RuntimeService) -> some View {
        HStack(spacing: 8) {
            Image(systemName: service.kind == "web" ? "macwindow" : "network")
                .frame(width: 16)
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 1) {
                Text(service.displayName)
                    .font(.callout.weight(.medium))
                Text(service.id)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private struct UnavailableServiceRow: View {
    let service: RuntimeService
    let prompt: String
    @State private var showInfo = false

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "circle")
                .foregroundStyle(.tertiary)
            HStack(spacing: 8) {
                Image(systemName: service.kind == "web" ? "macwindow" : "network")
                    .frame(width: 16)
                    .foregroundStyle(.tertiary)
                VStack(alignment: .leading, spacing: 1) {
                    Text(service.displayName)
                        .font(.callout.weight(.medium))
                    Text(service.id)
                        .font(.caption2.monospaced())
                        .foregroundStyle(.tertiary)
                }
            }
            Spacer()
            Text("Not yet isolated")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button {
                showInfo.toggle()
            } label: {
                Image(systemName: "info.circle")
                    .accessibilityLabel("Why \(service.displayName) is unavailable")
            }
            .buttonStyle(.borderless)
            .help(service.unavailableReason ?? "This service does not yet have a safe isolated launch plan.")
            .popover(isPresented: $showInfo, arrowEdge: .trailing) {
                UnavailableServiceInfo(service: service, prompt: prompt)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 7)
    }
}

struct UnavailableServiceInfo: View {
    let service: RuntimeService
    let prompt: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(service.displayName, systemImage: "info.circle.fill")
                .font(.headline)
            Text(service.unavailableReason ?? "This service does not yet have a safe isolated launch plan.")
                .font(.callout)
            Text("Switchyard needs an exact command, isolated ports and routing, owned runtime resources, readiness checks, and lifecycle tests before it can safely start this service.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Button {
                let pasteboard = NSPasteboard.general
                pasteboard.clearContents()
                copied = pasteboard.setString(prompt, forType: .string)
            } label: {
                Label(copied ? "Prompt copied" : "Copy agent prompt", systemImage: copied ? "checkmark" : "doc.on.doc")
            }
            .buttonStyle(.borderedProminent)
        }
        .padding(16)
        .frame(width: 360)
    }
}

struct ServiceIsolationPrompt {
    static func make(
        repository: Repository?,
        worktree: Worktree?,
        target: RuntimeTarget?,
        service: RuntimeService
    ) -> String {
        let repositoryName = repository?.displayName ?? "the configured repository"
        let repositoryPath = repository?.rootPath ?? "unknown"
        let worktreePath = worktree?.path ?? "unknown"
        let branch = worktree?.branch ?? "detached or unknown"
        let targetID = target?.id ?? repository?.runtime?.defaultTargetId ?? "unknown"
        return """
        Add safe Switchyard isolation support for the service `\(service.id)` (\(service.displayName), kind: \(service.kind)).

        Context:
        - Repository: \(repositoryName)
        - Repository root: \(repositoryPath)
        - Worktree: \(worktreePath)
        - Branch: \(branch)
        - Current target context: \(targetID)

        Work in the private Switchyard repository profile under Application Support, never by adding tracked setup files to the repository. Inspect the accepted profile's existing service, target, port, infrastructure, and artifact definitions first. Declare a complete service entry with an exact executable and exact argv, leased per-worktree ports and published routes, owned process or container resources, readiness and health probes, dependencies, and any private artifacts, then validate and accept the new configuration revision. Never bypass Switchyard with direct persistent service or broad Docker/process commands, and preserve warn-on-start confirmation for protected targets. Do not mark the service available until the isolated lifecycle is verified end to end.
        """
    }
}
