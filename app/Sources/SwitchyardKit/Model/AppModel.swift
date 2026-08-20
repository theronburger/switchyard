import Foundation
import Observation

public enum SidebarSelection: Hashable, Sendable {
    case overview
    case environment(String)
    case worktree(repositoryId: String, worktreeId: String)
    case connectionDoctor
}

public enum EnvironmentActionKind: String, Sendable, Equatable {
    case start
    case stop
    case rebuild
}

public enum EnvironmentActionStage: String, Sendable, Equatable {
    case starting
    case stopping
    case rebuilding
}

public struct EnvironmentActionSubmission: Sendable, Equatable {
    public let kind: EnvironmentActionKind
    public let stage: EnvironmentActionStage
    public let worktreeId: String
    public let receipt: MutationReceipt

    public init(
        kind: EnvironmentActionKind,
        stage: EnvironmentActionStage,
        worktreeId: String,
        receipt: MutationReceipt
    ) {
        self.kind = kind
        self.stage = stage
        self.worktreeId = worktreeId
        self.receipt = receipt
    }
}

public enum EnvironmentActionState: Sendable, Equatable {
    case idle
    case submitting(
        kind: EnvironmentActionKind,
        stage: EnvironmentActionStage,
        worktreeId: String,
        environmentId: String?
    )
    case accepted(EnvironmentActionSubmission)
    case failed(EnvironmentActionKind, String)

    public var isActive: Bool {
        switch self {
        case .submitting, .accepted:
            return true
        case .idle, .failed:
            return false
        }
    }
}

public enum WorkspaceActionKind: String, Sendable, Equatable {
    case create
    case adopt
    case archive
}

public enum WorkspaceActionState: Sendable, Equatable {
    case idle
    case submitting(WorkspaceActionKind)
    case accepted(WorkspaceActionKind, MutationReceipt)
    case failed(WorkspaceActionKind, String)

    public var isActive: Bool {
        switch self {
        case .submitting, .accepted: true
        case .idle, .failed: false
        }
    }
}

@MainActor
@Observable
public final class AppModel {
    public enum LoadPhase: Equatable, Sendable {
        case idle
        case loading
        case loaded
        case empty
        case failed(String)
    }

    public private(set) var phase: LoadPhase = .idle
    public private(set) var snapshot: StatusSnapshot?
    public private(set) var lastRefreshedAt: Date?
    public private(set) var doctorReport: DoctorReport?
    public private(set) var agentConnectionReport: AgentConnectionReport?
    public private(set) var repairingAgentHosts = Set<AgentHost>()
    public private(set) var lifecycleState: DaemonLifecycleState = .idle
    public private(set) var environmentActionState: EnvironmentActionState = .idle
    public private(set) var workspaceActionState: WorkspaceActionState = .idle
    public private(set) var scenario: FixtureScenario
    public let isFixtureMode: Bool
    public var selection: SidebarSelection?

    @ObservationIgnored private let liveController: (any DaemonLifecycleControlling)?
    @ObservationIgnored private let environmentActions: (any EnvironmentActionSubmitting)?
    @ObservationIgnored private let workspaceActions: (any WorkspaceActionSubmitting)?
    @ObservationIgnored private let agentConnections: (any AgentConnectionManaging)?
    @ObservationIgnored private var fixtureProvider: (any StatusProviding)?
    @ObservationIgnored private let canonicalFixtureURL: URL?
    @ObservationIgnored private let pollingInterval: Duration
    @ObservationIgnored private var pollingTask: Task<Void, Never>?
    @ObservationIgnored private var environmentActionMonitor: Task<Void, Never>?
    @ObservationIgnored private var workspaceActionMonitor: Task<Void, Never>?
    @ObservationIgnored private var isRefreshing = false
    @ObservationIgnored private var routedInitialConnectionSetup = false

    public var dataSourceDescription: String {
        fixtureProvider?.sourceDescription ?? "live daemon"
    }

    public var summary: StatusSummary? { snapshot?.summary }

    public var submittedOperation: Operation? {
        guard case .accepted(let submission) = environmentActionState else { return nil }
        return snapshot?.operations.first { $0.id == submission.receipt.operationId }
    }

    public var canSubmitEnvironmentAction: Bool {
        !isFixtureMode && lifecycleState.isOperational && !environmentActionState.isActive
    }

    public var canSubmitWorkspaceAction: Bool {
        !isFixtureMode && lifecycleState.isOperational && !workspaceActionState.isActive
    }

    public var workspaceActionFailureMessage: String? {
        guard case .failed(_, let message) = workspaceActionState else { return nil }
        return message
    }

    public var environmentActionFailureMessage: String? {
        guard case .failed(_, let message) = environmentActionState else { return nil }
        return message
    }

    public func environmentTransition(forWorktreeId worktreeId: String) -> EnvironmentActionStage? {
        switch environmentActionState {
        case .submitting(_, let stage, let submittedWorktreeId, _):
            return submittedWorktreeId == worktreeId ? stage : nil
        case .accepted(let submission):
            return submission.worktreeId == worktreeId ? submission.stage : nil
        case .idle, .failed:
            return nil
        }
    }

    public func environmentActionKind(forWorktreeId worktreeId: String) -> EnvironmentActionKind? {
        switch environmentActionState {
        case .submitting(let kind, _, let submittedWorktreeId, _):
            return submittedWorktreeId == worktreeId ? kind : nil
        case .accepted(let submission):
            return submission.worktreeId == worktreeId ? submission.kind : nil
        case .idle, .failed:
            return nil
        }
    }

    public var canRepairAllConnections: Bool {
        guard repairingAgentHosts.isEmpty, lifecycleState != .repairing else { return false }
        return lifecycleState.canRepair || agentConnectionReport?.statuses.contains(where: \.canRepair) == true
    }

    public init(
        liveController: any DaemonLifecycleControlling = DaemonLifecycleController(),
        environmentActions: any EnvironmentActionSubmitting = LiveEnvironmentActionClient(),
        workspaceActions: any WorkspaceActionSubmitting = LiveWorkspaceActionClient(),
        agentConnections: (any AgentConnectionManaging)? = AgentConnectionManager(),
        pollingInterval: Duration = .seconds(5)
    ) {
        self.scenario = .canonical
        self.isFixtureMode = false
        self.liveController = liveController
        self.environmentActions = environmentActions
        self.workspaceActions = workspaceActions
        self.agentConnections = agentConnections
        self.canonicalFixtureURL = nil
        self.pollingInterval = pollingInterval
    }

    public init(scenario: FixtureScenario, canonicalFixtureURL: URL? = nil) {
        self.scenario = scenario
        self.isFixtureMode = true
        self.liveController = nil
        self.environmentActions = nil
        self.workspaceActions = nil
        self.agentConnections = nil
        self.canonicalFixtureURL = canonicalFixtureURL
        self.pollingInterval = .seconds(5)
        self.fixtureProvider = FixtureStatusProvider(scenario: scenario, canonicalURL: canonicalFixtureURL)
    }

    public convenience init(
        configuration: AppLaunchConfiguration,
        pollingInterval: Duration = .seconds(5)
    ) {
        switch configuration {
        case .live:
            let channel = SwitchyardChannel.resolve()
            let paths = LaunchAgentPaths.standard(channel: channel)
            let location = DaemonEndpointLocation.standard(channel: channel)
            let serviceManager = LaunchAgentServiceManager(paths: paths, channel: channel)
            let connectionFactory = RuntimeConnectionFactory(location: location)
            let lifecycle = DaemonLifecycleController(
                serviceManager: serviceManager,
                connectionFactory: connectionFactory,
                doctor: LiveConnectionDoctor(
                    serviceManager: serviceManager,
                    connectionFactory: connectionFactory
                )
            )
            let agentConnections: (any AgentConnectionManaging)? = channel.permitsAgentRepair
                ? AgentConnectionManager()
                : nil
            self.init(
                liveController: lifecycle,
                environmentActions: LiveEnvironmentActionClient(connectionFactory: connectionFactory),
                workspaceActions: LiveWorkspaceActionClient(connectionFactory: connectionFactory),
                agentConnections: agentConnections,
                pollingInterval: pollingInterval
            )
        case .fixture(let scenario):
            self.init(scenario: scenario)
        }
    }

    public func select(scenario: FixtureScenario) async {
        guard isFixtureMode, scenario != self.scenario else { return }
        self.scenario = scenario
        fixtureProvider = FixtureStatusProvider(scenario: scenario, canonicalURL: canonicalFixtureURL)
        await refresh()
    }

    public func startPolling() {
        guard pollingTask == nil else { return }
        pollingTask = Task { [weak self] in
            guard let self else { return }
            if self.phase == .idle {
                await self.refresh()
            }
            guard !self.isFixtureMode else { return }
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: self.pollingInterval)
                } catch {
                    return
                }
                await self.refresh()
            }
        }
    }

    public func refresh() async {
        guard !isRefreshing else { return }
        isRefreshing = true
        defer { isRefreshing = false }
        if snapshot == nil {
            phase = .loading
        }
        if let fixtureProvider {
            await refreshFixture(using: fixtureProvider)
        } else if let liveController {
            apply(await liveController.refresh())
        }
        if agentConnectionReport == nil, let agentConnections {
            agentConnectionReport = await agentConnections.inspect()
        }
        if !routedInitialConnectionSetup {
            routedInitialConnectionSetup = true
            if lifecycleState.canRepair || agentConnectionReport?.statuses.contains(where: \.canRepair) == true {
                selection = .connectionDoctor
            }
        }
        clearMissingSelection()
    }

    public func repairAll() async {
        guard canRepairAllConnections else { return }
        if let liveController {
            if lifecycleState.canRepair {
                lifecycleState = .repairing
                phase = .loading
                apply(await liveController.repair())
            }
            if let agentConnections {
                repairingAgentHosts = Set(AgentHost.allCases)
                agentConnectionReport = await Task.detached {
                    await agentConnections.repairAll()
                }.value
                repairingAgentHosts.removeAll()
            }
        } else {
            lifecycleState = .repairing
            try? await Task.sleep(for: .milliseconds(100))
            await refresh()
        }
    }

    public func runConnectionChecks() async {
        await refresh()
        if let agentConnections {
            agentConnectionReport = await agentConnections.inspect()
        }
    }

    public func repairAgentConnection(_ host: AgentHost) async {
        guard !repairingAgentHosts.contains(host),
              agentConnectionReport?.status(for: host)?.canRepair == true,
              let agentConnections else { return }
        repairingAgentHosts.insert(host)
        agentConnectionReport = await Task.detached {
            await agentConnections.repair(host)
        }.value
        repairingAgentHosts.remove(host)
    }

    public func startEnvironment(
        worktreeId: String,
        targetId: String = "testing",
        confirmedTargetId: String? = nil,
        serviceIds: [String]
    ) async {
        guard canSubmitEnvironmentAction, let environmentActions else { return }
        let requestId = "app_\(UUID().uuidString.lowercased())"
        let request = StartEnvironmentRequest(
            requestId: requestId,
            idempotencyKey: "start_\(UUID().uuidString.lowercased())",
            worktreeId: worktreeId,
            targetId: targetId,
            confirmedTargetId: confirmedTargetId,
            serviceIds: serviceIds
        )
        environmentActionState = .submitting(
            kind: .start,
            stage: .starting,
            worktreeId: worktreeId,
            environmentId: snapshot?.environments.first(where: { $0.worktreeId == worktreeId })?.id
        )
        do {
            let receipt = try await environmentActions.startEnvironment(request)
            let submission = EnvironmentActionSubmission(
                kind: .start,
                stage: .starting,
                worktreeId: worktreeId,
                receipt: receipt
            )
            environmentActionState = .accepted(submission)
            await refresh()
            monitorEnvironmentAction(submission)
        } catch {
            environmentActionState = .failed(.start, Self.actionFailureMessage(error))
        }
    }

    public func stopEnvironment(_ environment: Environment) async {
        guard canSubmitEnvironmentAction,
              environment.allowsStopRequest,
              let environmentActions else { return }
        let requestId = "app_\(UUID().uuidString.lowercased())"
        let request = StopEnvironmentRequest(
            requestId: requestId,
            idempotencyKey: "stop_\(UUID().uuidString.lowercased())",
            expectedEnvironmentRevision: environment.revision
        )
        environmentActionState = .submitting(
            kind: .stop,
            stage: .stopping,
            worktreeId: environment.worktreeId,
            environmentId: environment.id
        )
        do {
            let receipt = try await environmentActions.stopEnvironment(id: environment.id, request: request)
            let submission = EnvironmentActionSubmission(
                kind: .stop,
                stage: .stopping,
                worktreeId: environment.worktreeId,
                receipt: receipt
            )
            environmentActionState = .accepted(submission)
            await refresh()
            monitorEnvironmentAction(submission)
        } catch {
            environmentActionState = .failed(.stop, Self.actionFailureMessage(error))
        }
    }

    public func rebuildEnvironment(
        _ environment: Environment,
        confirmedTargetId: String? = nil
    ) async {
        guard canSubmitEnvironmentAction,
              environment.allowsRebuildRequest,
              let environmentActions else { return }

        environmentActionMonitor?.cancel()
        let worktreeId = environment.worktreeId
        let targetId = environment.targetId ?? "testing"
        let serviceIds = environment.services.map(\.id)
        environmentActionState = .submitting(
            kind: .rebuild,
            stage: .stopping,
            worktreeId: worktreeId,
            environmentId: environment.id
        )

        do {
            let stopReceipt = try await environmentActions.stopEnvironment(
                id: environment.id,
                request: StopEnvironmentRequest(
                    requestId: "app_\(UUID().uuidString.lowercased())",
                    idempotencyKey: "rebuild_stop_\(UUID().uuidString.lowercased())",
                    expectedEnvironmentRevision: environment.revision
                )
            )
            environmentActionState = .accepted(EnvironmentActionSubmission(
                kind: .rebuild,
                stage: .stopping,
                worktreeId: worktreeId,
                receipt: stopReceipt
            ))
            try await waitForOperation(stopReceipt)

            environmentActionState = .submitting(
                kind: .rebuild,
                stage: .rebuilding,
                worktreeId: worktreeId,
                environmentId: environment.id
            )
            let stoppedRevision = snapshot?.environment(withId: environment.id)?.revision
            let startReceipt = try await environmentActions.startEnvironment(StartEnvironmentRequest(
                requestId: "app_\(UUID().uuidString.lowercased())",
                idempotencyKey: "rebuild_start_\(UUID().uuidString.lowercased())",
                expectedEnvironmentRevision: stoppedRevision,
                worktreeId: worktreeId,
                targetId: targetId,
                confirmedTargetId: confirmedTargetId,
                serviceIds: serviceIds
            ))
            environmentActionState = .accepted(EnvironmentActionSubmission(
                kind: .rebuild,
                stage: .starting,
                worktreeId: worktreeId,
                receipt: startReceipt
            ))
            try await waitForOperation(startReceipt)
            environmentActionState = .idle
        } catch {
            environmentActionState = .failed(.rebuild, Self.actionFailureMessage(error))
        }
    }

    public func dismissEnvironmentAction() {
        guard !environmentActionState.isActive else { return }
        environmentActionState = .idle
    }

    public func createWorktree(repositoryId: String, branch: String, startPoint: String? = nil) async {
        guard canSubmitWorkspaceAction, let workspaceActions else { return }
        workspaceActionState = .submitting(.create)
        do {
            let receipt = try await workspaceActions.createWorktree(CreateWorktreeRequest(
                requestId: "app_\(UUID().uuidString.lowercased())",
                idempotencyKey: "workspace_create_\(UUID().uuidString.lowercased())",
                repositoryId: repositoryId,
                branch: branch,
                startPoint: startPoint
            ))
            workspaceActionState = .accepted(.create, receipt)
            monitorWorkspaceAction(.create, receipt: receipt)
        } catch {
            workspaceActionState = .failed(.create, Self.actionFailureMessage(error))
        }
    }

    public func archiveWorktree(_ worktree: Worktree) async {
        guard canSubmitWorkspaceAction, let workspaceActions else { return }
        workspaceActionState = .submitting(.archive)
        do {
            let receipt = try await workspaceActions.archiveWorktree(ArchiveWorktreeRequest(
                requestId: "app_\(UUID().uuidString.lowercased())",
                idempotencyKey: "workspace_archive_\(UUID().uuidString.lowercased())",
                worktreeId: worktree.id
            ))
            workspaceActionState = .accepted(.archive, receipt)
            monitorWorkspaceAction(.archive, receipt: receipt)
        } catch {
            workspaceActionState = .failed(.archive, Self.actionFailureMessage(error))
        }
    }

    public func adoptWorktree(_ worktree: Worktree) async {
        guard canSubmitWorkspaceAction, let workspaceActions else { return }
        workspaceActionState = .submitting(.adopt)
        do {
            let receipt = try await workspaceActions.adoptWorktree(AdoptWorktreeRequest(
                requestId: "app_\(UUID().uuidString.lowercased())",
                idempotencyKey: "workspace_adopt_\(UUID().uuidString.lowercased())",
                worktreeId: worktree.id
            ))
            workspaceActionState = .accepted(.adopt, receipt)
            monitorWorkspaceAction(.adopt, receipt: receipt)
        } catch {
            workspaceActionState = .failed(.adopt, Self.actionFailureMessage(error))
        }
    }

    public func dismissWorkspaceAction() {
        guard !workspaceActionState.isActive else { return }
        workspaceActionState = .idle
    }

    private func monitorWorkspaceAction(_ kind: WorkspaceActionKind, receipt: MutationReceipt) {
        workspaceActionMonitor?.cancel()
        workspaceActionMonitor = Task { [weak self] in
            guard let self else { return }
            do {
                try await self.waitForOperation(receipt)
                self.workspaceActionState = .idle
                self.selection = .overview
                await self.refresh()
            } catch is CancellationError {
                return
            } catch {
                self.workspaceActionState = .failed(kind, Self.actionFailureMessage(error))
            }
        }
    }

    private func monitorEnvironmentAction(_ submission: EnvironmentActionSubmission) {
        environmentActionMonitor?.cancel()
        environmentActionMonitor = Task { [weak self] in
            guard let self else { return }
            do {
                try await self.waitForOperation(submission.receipt)
                guard case .accepted(let current) = self.environmentActionState,
                      current.receipt.operationId == submission.receipt.operationId else { return }
                self.environmentActionState = .idle
            } catch is CancellationError {
                return
            } catch {
                guard case .accepted(let current) = self.environmentActionState,
                      current.receipt.operationId == submission.receipt.operationId else { return }
                self.environmentActionState = .failed(submission.kind, Self.actionFailureMessage(error))
            }
        }
    }

    private func waitForOperation(_ receipt: MutationReceipt) async throws {
        for _ in 0..<900 {
            try Task.checkCancellation()
            await refresh()
            if let operation = snapshot?.operations.first(where: { $0.id == receipt.operationId }) {
                switch operation.state {
                case .succeeded:
                    if let expectedRunId = receipt.runId {
                        guard operation.runId == expectedRunId,
                              let environmentId = receipt.environmentId,
                              let environment = snapshot?.environment(withId: environmentId),
                              !environment.services.isEmpty,
                              environment.services.allSatisfy({ $0.run?.id == expectedRunId }) else {
                            throw EnvironmentWorkflowError.runIdentityMismatch
                        }
                    }
                    return
                case .failed, .cancelled:
                    throw EnvironmentWorkflowError.operationFailed(
                        operation.error?.message ?? "The environment operation did not complete."
                    )
                case .unknown, .pending, .running:
                    break
                }
            }
            try await Task.sleep(for: .seconds(1))
        }
        throw EnvironmentWorkflowError.timedOut
    }

    private func refreshFixture(using provider: any StatusProviding) async {
        do {
            let loaded = try await provider.loadStatus()
            snapshot = loaded
            lastRefreshedAt = Date()
            phase = loaded.repositories.isEmpty && loaded.environments.isEmpty ? .empty : .loaded
            lifecycleState = Self.scripted(readyFor: loaded.daemon).state
            doctorReport = .fixtureHealthy(daemon: loaded.daemon)
        } catch {
            let message = (error as? LocalizedError)?.errorDescription ?? String(describing: error)
            snapshot = nil
            lastRefreshedAt = Date()
            phase = .failed(message)
            lifecycleState = Self.scripted(unreachableBecause: message).state
            doctorReport = .fixtureUnreachable(reason: message)
        }
    }

    private func apply(_ result: DaemonLifecycleResult) {
        lifecycleState = result.state
        snapshot = result.snapshot
        doctorReport = result.doctorReport
        lastRefreshedAt = Date()
        if let snapshot = result.snapshot {
            phase = snapshot.repositories.isEmpty && snapshot.environments.isEmpty ? .empty : .loaded
        } else {
            phase = .failed(result.state.summary)
        }
    }

    private func clearMissingSelection() {
        if case .environment(let id) = selection, snapshot?.environment(withId: id) == nil {
            selection = nil
        }
        if case .worktree(_, let id) = selection, snapshot?.worktree(withId: id) == nil {
            selection = nil
        }
    }

    private static func scripted(_ events: [DaemonLifecycleEvent]) -> DaemonLifecycleMachine {
        var machine = DaemonLifecycleMachine()
        for event in events {
            do {
                try machine.handle(event)
            } catch {
                assertionFailure("scripted lifecycle event was rejected: \(error)")
            }
        }
        return machine
    }

    private static func scripted(readyFor daemon: DaemonStatus) -> DaemonLifecycleMachine {
        let endpoint = EndpointDescriptor(
            schemaVersion: EndpointDescriptor.supportedSchemaVersion,
            transport: EndpointDescriptor.supportedTransport,
            host: "127.0.0.1",
            port: 49402,
            daemonVersion: daemon.version,
            instanceId: daemon.instanceId,
            createdAt: daemon.startedAt
        )
        let session = DaemonSession(
            instanceId: daemon.instanceId,
            daemonVersion: daemon.version,
            endpoint: endpoint
        )
        return scripted([
            .begin,
            .registrationChecked(.enabled),
            .endpointFound(endpoint),
            .handshakeSucceeded(session),
        ])
    }

    private static func scripted(unreachableBecause reason: String) -> DaemonLifecycleMachine {
        scripted([
            .begin,
            .registrationChecked(.enabled),
            .endpointMissing,
            .daemonStartFailed(reason: reason),
        ])
    }

    private static func actionFailureMessage(_ error: Error) -> String {
        if let daemonError = error as? DaemonClientError {
            return daemonError.description
        }
        if let connectionError = error as? RuntimeConnectionError {
            return connectionError.description
        }
        if let workflowError = error as? EnvironmentWorkflowError {
            return workflowError.errorDescription ?? "The environment operation did not complete."
        }
        return "The environment action could not be submitted."
    }
}

private enum EnvironmentWorkflowError: LocalizedError {
    case operationFailed(String)
    case runIdentityMismatch
    case timedOut

    var errorDescription: String? {
        switch self {
        case .operationFailed(let message):
            return message
        case .runIdentityMismatch:
            return "The completed operation did not publish the environment run it accepted. Refresh status before retrying."
        case .timedOut:
            return "The environment operation did not finish within 15 minutes."
        }
    }
}
