import Foundation

public protocol LifecycleSleeping: Sendable {
    func sleep(for duration: Duration) async throws
}

public struct ContinuousLifecycleSleeper: LifecycleSleeping {
    public init() {}

    public func sleep(for duration: Duration) async throws {
        try await Task.sleep(for: duration)
    }
}

public struct EndpointWaitPolicy: Sendable, Equatable {
    public let delays: [Duration]

    public init(delays: [Duration] = [.milliseconds(100), .milliseconds(200), .milliseconds(400), .milliseconds(800)]) {
        self.delays = delays
    }
}

public struct DaemonLifecycleResult: Sendable {
    public let state: DaemonLifecycleState
    public let snapshot: StatusSnapshot?
    public let doctorReport: DoctorReport

    public init(state: DaemonLifecycleState, snapshot: StatusSnapshot?, doctorReport: DoctorReport) {
        self.state = state
        self.snapshot = snapshot
        self.doctorReport = doctorReport
    }
}

public protocol DaemonLifecycleControlling: Sendable {
    func refresh() async -> DaemonLifecycleResult
    func repair() async -> DaemonLifecycleResult
}

public struct DaemonLifecycleController: DaemonLifecycleControlling {
    private let serviceManager: any DaemonServiceManaging
    private let connectionFactory: any RuntimeConnectionEstablishing
    private let doctor: any DoctorRunning
    private let sleeper: any LifecycleSleeping
    private let waitPolicy: EndpointWaitPolicy

    public init(
        serviceManager: any DaemonServiceManaging = LaunchAgentServiceManager(),
        connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory(),
        doctor: (any DoctorRunning)? = nil,
        sleeper: any LifecycleSleeping = ContinuousLifecycleSleeper(),
        waitPolicy: EndpointWaitPolicy = EndpointWaitPolicy()
    ) {
        self.serviceManager = serviceManager
        self.connectionFactory = connectionFactory
        self.doctor = doctor ?? LiveConnectionDoctor(
            serviceManager: serviceManager,
            connectionFactory: connectionFactory
        )
        self.sleeper = sleeper
        self.waitPolicy = waitPolicy
    }

    public func refresh() async -> DaemonLifecycleResult {
        await run(installIfMissing: true, waitForRestartedEndpoint: false)
    }

    public func repair() async -> DaemonLifecycleResult {
        do {
            try await serviceManager.repair()
            return await run(installIfMissing: false, waitForRestartedEndpoint: true)
        } catch {
            return await result(
                state: .unreachable(reason: safeServiceMessage(
                    error,
                    fallback: "The Switchyard helper could not be repaired."
                )),
                snapshot: nil
            )
        }
    }

    private func run(
        installIfMissing: Bool,
        waitForRestartedEndpoint initialWaitForRestartedEndpoint: Bool
    ) async -> DaemonLifecycleResult {
        var machine = DaemonLifecycleMachine()
        var waitForRestartedEndpoint = initialWaitForRestartedEndpoint
        apply(.begin, to: &machine)

        let registration: DaemonRegistrationStatus
        do {
            registration = try await serviceManager.inspect()
        } catch {
            return await result(
                state: .unreachable(reason: "The Switchyard LaunchAgent could not be inspected."),
                snapshot: nil
            )
        }
        apply(.registrationChecked(registration), to: &machine)

        if registration == .requiresApproval {
            return await result(state: machine.state, snapshot: nil)
        }
        if registration == .notRegistered || registration == .notFound || registration == .outdated {
            guard installIfMissing else {
                return await result(state: machine.state, snapshot: nil)
            }
            do {
                try await serviceManager.install()
                waitForRestartedEndpoint = true
                apply(.registrationSubmitted, to: &machine)
                let installedStatus = try await serviceManager.inspect()
                apply(.registrationChecked(installedStatus), to: &machine)
                guard installedStatus == .enabled else {
                    return await result(state: machine.state, snapshot: nil)
                }
            } catch {
                return await result(
                    state: .unreachable(reason: safeServiceMessage(
                        error,
                        fallback: "The Switchyard helper could not be installed."
                    )),
                    snapshot: nil
                )
            }
        }

        return await connect(machine: machine, waitForRestartedEndpoint: waitForRestartedEndpoint)
    }

    private func connect(
        machine initialMachine: DaemonLifecycleMachine,
        waitForRestartedEndpoint: Bool
    ) async -> DaemonLifecycleResult {
        var machine = initialMachine
        var connection: DaemonConnection
        if waitForRestartedEndpoint {
            do {
                connection = try await waitForEndpoint(retryTransientIdentity: true)
            } catch let error as RuntimeConnectionError {
                apply(.endpointInvalid(reason: error.description), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            } catch {
                apply(.endpointInvalid(reason: "The restarted Switchyard daemon did not publish a verified endpoint in time."), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            }
        } else {
            do {
                connection = try connectionFactory.connect()
            } catch let error as RuntimeConnectionError where error.descriptorIsMissing {
                apply(.endpointMissing, to: &machine)
                do {
                    try await serviceManager.kickstart()
                    apply(.daemonStarted, to: &machine)
                } catch {
                    apply(.daemonStartFailed(reason: "macOS could not start the Switchyard daemon."), to: &machine)
                    return await result(state: machine.state, snapshot: nil)
                }
                do {
                    connection = try await waitForEndpoint(retryTransientIdentity: true)
                } catch let error as RuntimeConnectionError {
                    apply(.endpointInvalid(reason: error.description), to: &machine)
                    return await result(state: machine.state, snapshot: nil)
                } catch {
                    apply(.endpointInvalid(reason: "The Switchyard daemon did not publish a verified endpoint in time."), to: &machine)
                    return await result(state: machine.state, snapshot: nil)
                }
            } catch let error as RuntimeConnectionError {
                apply(.endpointInvalid(reason: error.description), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            } catch {
                apply(.endpointInvalid(reason: "The Switchyard daemon runtime could not be verified."), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            }
        }

        apply(.endpointFound(connection.descriptor), to: &machine)
        do {
            let handshake = try await connection.client.handshake()
            let session = DaemonSession(
                instanceId: handshake.daemonInstanceId,
                daemonVersion: handshake.daemonVersion,
                endpoint: connection.descriptor
            )
            apply(.handshakeSucceeded(session), to: &machine)
            do {
                let snapshot = try await connection.client.status()
                return await result(state: machine.state, snapshot: snapshot)
            } catch let error as DaemonClientError {
                apply(.connectionLost(reason: error.description), to: &machine)
                apply(.endpointInvalid(reason: error.description), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            } catch {
                apply(.connectionLost(reason: "The daemon status request failed."), to: &machine)
                apply(.endpointInvalid(reason: "The daemon status request failed."), to: &machine)
                return await result(state: machine.state, snapshot: nil)
            }
        } catch let error as DaemonClientError {
            switch error {
            case .unauthorized:
                apply(.handshakeUnauthorized, to: &machine)
            case .upgradeRequired:
                apply(.handshakeUpgradeRequired(message: error.description), to: &machine)
            default:
                apply(.connectionFailed(reason: error.description), to: &machine)
            }
            return await result(state: machine.state, snapshot: nil)
        } catch {
            apply(.connectionFailed(reason: "The daemon handshake failed."), to: &machine)
            return await result(state: machine.state, snapshot: nil)
        }
    }

    private func waitForEndpoint(retryTransientIdentity: Bool) async throws -> DaemonConnection {
        var lastError: Error = RuntimeConnectionError.descriptorUnavailable
        for delay in waitPolicy.delays {
            try await sleeper.sleep(for: delay)
            do {
                return try connectionFactory.connect()
            } catch {
                lastError = error
                if let runtimeError = error as? RuntimeConnectionError {
                    let retryable = retryTransientIdentity
                        ? runtimeError.retryableWhileDaemonStarts
                        : runtimeError.descriptorIsMissing
                    if !retryable { throw runtimeError }
                }
            }
        }
        throw lastError
    }

    private func result(state: DaemonLifecycleState, snapshot: StatusSnapshot?) async -> DaemonLifecycleResult {
        DaemonLifecycleResult(
            state: state,
            snapshot: snapshot,
            doctorReport: await doctor.run()
        )
    }

    private func apply(_ event: DaemonLifecycleEvent, to machine: inout DaemonLifecycleMachine) {
        do {
            try machine.handle(event)
        } catch {
            assertionFailure("daemon lifecycle controller emitted an invalid transition: \(error)")
        }
    }

    private func safeServiceMessage(_ error: Error, fallback: String) -> String {
        if let sourceError = error as? DaemonBinarySourceError {
            return sourceError.description
        }
        if let serviceError = error as? DaemonServiceError {
            return serviceError.description
        }
        if let commandError = error as? ExactCommandError {
            return commandError.description
        }
        return fallback
    }
}
