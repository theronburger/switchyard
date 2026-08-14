import Foundation
import Observation

/// What the command-center sidebar can select.
public enum SidebarSelection: Hashable, Sendable {
    case environment(String)
    case connectionDoctor
}

/// The app's single observable source of truth: the latest contract snapshot,
/// the daemon lifecycle state, and the Connection Doctor report.
///
/// While Switchyard is fixture-driven the lifecycle machine is scripted from
/// the active scenario; the same machine later consumes real SMAppService,
/// descriptor, and handshake observations without changing shape.
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
    public private(set) var scenario: FixtureScenario
    public var selection: SidebarSelection?

    private var machine = DaemonLifecycleMachine()
    private var provider: any StatusProviding
    private let canonicalFixtureURL: URL?

    public var lifecycleState: DaemonLifecycleState { machine.state }
    public var dataSourceDescription: String { provider.sourceDescription }
    public var summary: StatusSummary? { snapshot?.summary }

    public init(scenario: FixtureScenario = .canonical, canonicalFixtureURL: URL? = nil) {
        self.scenario = scenario
        self.canonicalFixtureURL = canonicalFixtureURL
        self.provider = FixtureStatusProvider(scenario: scenario, canonicalURL: canonicalFixtureURL)
    }

    public func select(scenario: FixtureScenario) async {
        guard scenario != self.scenario else { return }
        self.scenario = scenario
        provider = FixtureStatusProvider(scenario: scenario, canonicalURL: canonicalFixtureURL)
        await refresh()
    }

    public func refresh() async {
        phase = .loading
        do {
            let loaded = try await provider.loadStatus()
            snapshot = loaded
            lastRefreshedAt = Date()
            phase = loaded.repositories.isEmpty && loaded.environments.isEmpty ? .empty : .loaded
            machine = Self.scripted(readyFor: loaded.daemon)
            doctorReport = .fixtureHealthy(daemon: loaded.daemon)
        } catch {
            let message = (error as? LocalizedError)?.errorDescription ?? String(describing: error)
            snapshot = nil
            lastRefreshedAt = Date()
            phase = .failed(message)
            machine = Self.scripted(unreachableBecause: message)
            doctorReport = .fixtureUnreachable(reason: message)
        }
        if case .environment(let id) = selection, snapshot?.environment(withId: id) == nil {
            selection = nil
        }
    }

    /// Repair All. In fixture mode this walks the real state machine through
    /// its repair path, then reloads; live wiring replaces only the middle.
    public func repairAll() async {
        guard machine.state.canRepair else { return }
        apply(.repairRequested)
        try? await Task.sleep(for: .milliseconds(400))
        apply(.repairCompleted)
        await refresh()
    }

    private func apply(_ event: DaemonLifecycleEvent) {
        do {
            try machine.handle(event)
        } catch {
            assertionFailure("scripted lifecycle event was rejected: \(error)")
        }
    }

    // MARK: - Scripted fixture lifecycles

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
        // A plausible ephemeral-port descriptor standing in for the file the
        // daemon will publish (D-015).
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
