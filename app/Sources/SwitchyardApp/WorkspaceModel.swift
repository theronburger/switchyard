import Foundation
import Observation

@MainActor @Observable
final class WorkspaceModel {
    private(set) var snapshot: WorkspaceSnapshot?
    private(set) var connectionError: String?
    private(set) var isConnecting = false
    private(set) var busy: [String: String] = [:]
    private(set) var errors: [String: String] = [:]
    private(set) var logs: [String: WorkspaceLog] = [:]
    private(set) var logErrors: [String: String] = [:]
    private(set) var localChoices: [String: WorkspaceChoices] = [:]
    var selection: String? { didSet { preferences?.set(selection, forKey: preferenceKey + ".selection") } }
    let api: any WorkspaceAPI
    let installation: AppInstallation
    @ObservationIgnored private let preferences: UserDefaults?
    @ObservationIgnored private let preferenceKey: String
    @ObservationIgnored private var poller: Task<Void, Never>?
    @ObservationIgnored private var refreshing = false
    @ObservationIgnored private var retiredInstances = Set<String>()
    @ObservationIgnored private var actionIds: [String: UUID] = [:]
    private var receipts: [String: WorkspaceRun] = [:]
    @ObservationIgnored private var refreshGeneration: UInt64 = 0
    @ObservationIgnored private var choiceSaves: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var choiceVersions: [String: UUID] = [:]
    @ObservationIgnored private var loadingLogs = Set<String>()

    init(api: any WorkspaceAPI, installation: AppInstallation = .resolve(), preferences: UserDefaults? = nil) {
        self.api = api
        self.installation = installation
        self.preferences = preferences
        self.preferenceKey = "workspaces.v3." + Data(installation.root.path.utf8).base64EncodedString()
        self.selection = preferences?.string(forKey: preferenceKey + ".selection")
    }

    var repositories: [RepositorySummary] { snapshot?.repositories ?? [] }
    var workspaces: [Workspace] {
        (snapshot?.workspaces ?? []).sorted { first, second in
            if first.primary != second.primary { return first.primary }
            let firstName = first.branch.isEmpty ? first.path : first.branch
            let secondName = second.branch.isEmpty ? second.path : second.branch
            return firstName == secondName ? first.path < second.path : firstName.localizedStandardCompare(secondName) == .orderedAscending
        }
    }
    var runningCount: Int { workspaces.filter { run(for: $0)?.state == "running" }.count }
    func run(for workspace: Workspace) -> WorkspaceRun? { receipts[workspace.path] ?? workspace.run }
    func workspace(at path: String) -> Workspace? { workspaces.first { $0.path == path } }
    func repository(for workspace: Workspace) -> RepositorySummary? { repositories.first { $0.id == workspace.repositoryId } }

    func choices(for workspace: Workspace, repository: RepositorySummary) -> WorkspaceChoices {
        var result = localChoices[workspace.path] ?? workspace.choices
        let targets = repository.targets ?? []
        if !targets.contains(where: { $0.id == result.target }) { result.target = repository.defaultTarget }
        let services = repository.services ?? []
        result.services = result.services.filter { id in services.contains { $0.id == id } }
        if localChoices[workspace.path] == nil && workspace.choices.target.isEmpty && result.services.isEmpty,
           let first = services.first(where: { $0.kind == "web" }) ?? services.first { result.services = [first.id] }
        return result
    }

    func remember(path: String, choices: WorkspaceChoices) {
        localChoices[path] = choices
        errors[path] = nil
        let version = UUID()
        choiceVersions[path] = version
        let previous = choiceSaves[path]
        choiceSaves[path] = Task { [weak self, api] in
            await previous?.value
            guard let self, self.choiceVersions[path] == version else { return }
            do {
                try await api.choices(path: path, choices: choices)
                await self.refreshAfterMutation()
            } catch { if self.choiceVersions[path] == version { self.errors[path] = "Choices have not been saved. " + error.localizedDescription } }
            if self.choiceVersions[path] == version { self.choiceSaves[path] = nil }
        }
    }

    func start() {
        guard poller == nil else { return }
        poller = Task { [weak self] in
            guard let self else { return }
            await self.connect()
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
                await self.refresh()
            }
        }
    }

    func connect(repair: Bool = false) async {
        guard !isConnecting else { return }
        isConnecting = true
        defer { isConnecting = false }
        do {
            let installation = installation
            try await Task.detached { try installation.ensureDaemon(repair: repair) }.value
            for attempt in 0..<15 {
                await refresh()
                if connectionError == nil { return }
                if attempt < 14 { try await Task.sleep(for: .milliseconds(300)) }
            }
        } catch { connectionError = error.localizedDescription }
    }

    func refreshAfterMutation() async {
        refreshGeneration &+= 1
        await refresh()
    }

    func refresh() async {
        guard !refreshing else { return }
        refreshing = true
        defer { refreshing = false }
        while !Task.isCancelled {
            let generation = refreshGeneration
            do {
                let loaded = try await api.status()
                guard generation == refreshGeneration else { continue }
                guard accept(loaded) else { return }
                connectionError = nil
            } catch {
                guard generation == refreshGeneration else { continue }
                connectionError = error.localizedDescription
            }
            return
        }
    }

    @discardableResult
    func accept(_ loaded: WorkspaceSnapshot) -> Bool {
        guard loaded.version == 3, !retiredInstances.contains(loaded.instanceId) else { return false }
        if let previous = snapshot {
            if previous.instanceId == loaded.instanceId {
                guard loaded.revision >= previous.revision else { return false }
            } else {
                retiredInstances.insert(previous.instanceId)
                receipts.removeAll()
                logs.removeAll()
                actionIds.removeAll()
                busy.removeAll()
            }
        }
        snapshot = loaded
        receipts.removeAll()
        for workspace in loaded.workspaces ?? [] {
            if localChoices[workspace.path] == workspace.choices { localChoices[workspace.path] = nil }
        }
        return true
    }

    func runWorkspace(path: String, choices: WorkspaceChoices, confirmedTarget: String? = nil, prepareOnly: Bool = false) async {
        guard busy[path] == nil, prepareOnly || !choices.services.isEmpty else { return }
        let request = RunRequest(path: path, requestId: UUID().uuidString.lowercased(), target: choices.target,
                                 services: choices.services, confirmedTarget: confirmedTarget, prepareOnly: prepareOnly)
        remember(path: path, choices: choices)
        await changeRun(path: path, title: prepareOnly ? "Preparing…" : "Starting…") { [api, choiceSave = choiceSaves[path]] id in
            await choiceSave?.value
            guard self.actionIds[path] == id else { throw CancellationError() }
            return try await api.run(request)
        }
    }

    func stopWorkspace(path: String) async {
        guard busy[path] != "Stopping…" else { return }
        await changeRun(path: path, title: "Stopping…") { [api] _ in try await api.stop(path: path) }
    }

    func restartWorkspace(path: String, choices: WorkspaceChoices, confirmedTarget: String? = nil) async {
        guard busy[path] == nil, !choices.services.isEmpty else { return }
        remember(path: path, choices: choices)
        await changeRun(path: path, title: "Restarting…") { [api, choiceSave = choiceSaves[path]] id in
            await choiceSave?.value
            guard self.actionIds[path] == id else { throw CancellationError() }
            _ = try await api.stop(path: path)
            guard self.actionIds[path] == id else { throw CancellationError() }
            return try await api.run(RunRequest(path: path, requestId: UUID().uuidString.lowercased(), target: choices.target,
                                              services: choices.services, confirmedTarget: confirmedTarget, prepareOnly: false))
        }
    }

    private func changeRun(path: String, title: String, action: (UUID) async throws -> WorkspaceRun) async {
        let id = UUID()
        let instance = snapshot?.instanceId
        actionIds[path] = id
        busy[path] = title
        errors[path] = nil
        defer { if actionIds[path] == id { busy[path] = nil; actionIds[path] = nil } }
        do {
            let receipt = try await action(id)
            guard actionIds[path] == id, snapshot?.instanceId == instance else { return }
            guard receipt.path == path else { throw WorkspaceAPIError(message: "The helper answered for a different workspace.") }
            receipts[path] = receipt
            errors[path] = nil
            if logs[path]?.runId != receipt.id { logs[path] = nil }
            await refreshAfterMutation()
        } catch is CancellationError {
            return
        } catch { if actionIds[path] == id, snapshot?.instanceId == instance { errors[path] = error.localizedDescription } }
    }

    func loadLogs(path: String) async {
        guard !loadingLogs.contains(path) else { return }
        loadingLogs.insert(path)
        defer { loadingLogs.remove(path) }
        do {
            let result = try await api.logs(path: path)
            guard let workspace = workspace(at: path), run(for: workspace)?.id == result.runId else { return }
            logs[path] = result
            logErrors[path] = nil
        } catch { logErrors[path] = error.localizedDescription }
    }

    func prune(path: String) async -> Bool {
        guard busy[path] == nil else { return false }
        busy[path] = "Deleting…"
        errors[path] = nil
        defer { busy[path] = nil }
        do {
            try await api.prune(path: path)
            if selection == path { selection = nil }
            localChoices[path] = nil
            receipts[path] = nil
            await refreshAfterMutation()
            return true
        } catch { errors[path] = error.localizedDescription; return false }
    }
}
