import Foundation
import Observation

public enum SidebarSelection: Hashable, Sendable {
    case environment(String)
    case connectionDoctor
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
    public private(set) var lifecycleState: DaemonLifecycleState = .idle
    public private(set) var scenario: FixtureScenario
    public let isFixtureMode: Bool
    public var selection: SidebarSelection?

    @ObservationIgnored private let liveController: (any DaemonLifecycleControlling)?
    @ObservationIgnored private var fixtureProvider: (any StatusProviding)?
    @ObservationIgnored private let canonicalFixtureURL: URL?
    @ObservationIgnored private let pollingInterval: Duration
    @ObservationIgnored private var pollingTask: Task<Void, Never>?
    @ObservationIgnored private var isRefreshing = false

    public var dataSourceDescription: String {
        fixtureProvider?.sourceDescription ?? "live daemon"
    }

    public var summary: StatusSummary? { snapshot?.summary }

    public init(
        liveController: any DaemonLifecycleControlling = DaemonLifecycleController(),
        pollingInterval: Duration = .seconds(5)
    ) {
        self.scenario = .canonical
        self.isFixtureMode = false
        self.liveController = liveController
        self.canonicalFixtureURL = nil
        self.pollingInterval = pollingInterval
    }

    public init(scenario: FixtureScenario, canonicalFixtureURL: URL? = nil) {
        self.scenario = scenario
        self.isFixtureMode = true
        self.liveController = nil
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
            self.init(liveController: DaemonLifecycleController(), pollingInterval: pollingInterval)
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
        clearMissingSelection()
    }

    public func repairAll() async {
        guard lifecycleState.canRepair else { return }
        if let liveController {
            lifecycleState = .repairing
            phase = .loading
            apply(await liveController.repair())
        } else {
            lifecycleState = .repairing
            try? await Task.sleep(for: .milliseconds(100))
            await refresh()
        }
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
}
