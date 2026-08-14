import Foundation

/// Registration status of the daemon's launch agent, shaped so
/// `SMAppService.Status` maps onto it one-to-one when real wiring lands
/// (D-005, D-014).
public enum DaemonRegistrationStatus: String, Sendable, Equatable, CaseIterable {
    case notRegistered
    case requiresApproval
    case enabled
    case notFound
}

/// An authenticated, version-compatible connection to a live daemon.
public struct DaemonSession: Sendable, Equatable {
    public let instanceId: String
    public let daemonVersion: String
    public let endpoint: EndpointDescriptor

    public init(instanceId: String, daemonVersion: String, endpoint: EndpointDescriptor) {
        self.instanceId = instanceId
        self.daemonVersion = daemonVersion
        self.endpoint = endpoint
    }
}

public enum DaemonLifecycleState: Sendable, Equatable {
    case idle
    case checkingRegistration
    case registrationRequired
    case approvalRequired
    case startingDaemon
    case locatingEndpoint
    case connecting(EndpointDescriptor)
    case ready(DaemonSession)
    case unauthorized
    case upgradeRequired(message: String)
    case unreachable(reason: String)
    case repairing

    /// Repair All is offered from every resting state, including `ready`
    /// (reinstall/upgrade), but never mid-transition.
    public var canRepair: Bool {
        switch self {
        case .registrationRequired, .approvalRequired, .unauthorized,
             .upgradeRequired, .unreachable, .ready:
            return true
        case .idle, .checkingRegistration, .startingDaemon,
             .locatingEndpoint, .connecting, .repairing:
            return false
        }
    }

    /// States that block normal use until the user acts in the app.
    public var needsUserAction: Bool {
        switch self {
        case .registrationRequired, .approvalRequired, .unauthorized,
             .upgradeRequired, .unreachable:
            return true
        default:
            return false
        }
    }

    public var isOperational: Bool {
        if case .ready = self { return true }
        return false
    }

    /// Stable case name for logs and named transition errors.
    public var name: String {
        switch self {
        case .idle: return "idle"
        case .checkingRegistration: return "checkingRegistration"
        case .registrationRequired: return "registrationRequired"
        case .approvalRequired: return "approvalRequired"
        case .startingDaemon: return "startingDaemon"
        case .locatingEndpoint: return "locatingEndpoint"
        case .connecting: return "connecting"
        case .ready: return "ready"
        case .unauthorized: return "unauthorized"
        case .upgradeRequired: return "upgradeRequired"
        case .unreachable: return "unreachable"
        case .repairing: return "repairing"
        }
    }

    public var displayName: String {
        switch self {
        case .idle: return "Not started"
        case .checkingRegistration: return "Checking installation"
        case .registrationRequired: return "Helper not installed"
        case .approvalRequired: return "Approval needed"
        case .startingDaemon: return "Starting daemon"
        case .locatingEndpoint: return "Locating daemon"
        case .connecting: return "Connecting"
        case .ready: return "Connected"
        case .unauthorized: return "Not authorized"
        case .upgradeRequired: return "Update required"
        case .unreachable: return "Daemon unreachable"
        case .repairing: return "Repairing"
        }
    }

    public var summary: String {
        switch self {
        case .idle:
            return "The app has not checked the daemon yet."
        case .checkingRegistration:
            return "Checking whether the Switchyard helper is installed as a login item."
        case .registrationRequired:
            return "The background helper is not installed. Repair All installs and registers it."
        case .approvalRequired:
            return "macOS needs your approval before the helper can run. Open Login Items settings to allow Switchyard."
        case .startingDaemon:
            return "Asking launchd to start the Switchyard daemon."
        case .locatingEndpoint:
            return "Looking for the daemon's published loopback endpoint."
        case .connecting(let endpoint):
            return "Authenticating with the daemon on 127.0.0.1 port \(endpoint.port)."
        case .ready(let session):
            return "Connected to daemon \(session.daemonVersion)."
        case .unauthorized:
            return "The daemon rejected this app's credentials. Repair All rotates the local token."
        case .upgradeRequired(let message):
            return message
        case .unreachable(let reason):
            return reason
        case .repairing:
            return "Reinstalling and restarting the Switchyard helper."
        }
    }
}

public enum DaemonLifecycleEvent: Sendable, Equatable {
    case begin
    case registrationChecked(DaemonRegistrationStatus)
    case registrationSubmitted
    case approvalGranted
    case endpointFound(EndpointDescriptor)
    case endpointMissing
    case endpointInvalid(reason: String)
    case daemonStarted
    case daemonStartFailed(reason: String)
    case handshakeSucceeded(DaemonSession)
    case handshakeUnauthorized
    case handshakeUpgradeRequired(message: String)
    case connectionFailed(reason: String)
    case connectionLost(reason: String)
    case repairRequested
    case repairCompleted
    case repairFailed(reason: String)

    /// Stable case name for logs and named transition errors.
    public var name: String {
        switch self {
        case .begin: return "begin"
        case .registrationChecked: return "registrationChecked"
        case .registrationSubmitted: return "registrationSubmitted"
        case .approvalGranted: return "approvalGranted"
        case .endpointFound: return "endpointFound"
        case .endpointMissing: return "endpointMissing"
        case .endpointInvalid: return "endpointInvalid"
        case .daemonStarted: return "daemonStarted"
        case .daemonStartFailed: return "daemonStartFailed"
        case .handshakeSucceeded: return "handshakeSucceeded"
        case .handshakeUnauthorized: return "handshakeUnauthorized"
        case .handshakeUpgradeRequired: return "handshakeUpgradeRequired"
        case .connectionFailed: return "connectionFailed"
        case .connectionLost: return "connectionLost"
        case .repairRequested: return "repairRequested"
        case .repairCompleted: return "repairCompleted"
        case .repairFailed: return "repairFailed"
        }
    }
}

public enum DaemonLifecycleError: Error, Equatable, CustomStringConvertible {
    case invalidTransition(state: String, event: String)

    public var description: String {
        switch self {
        case .invalidTransition(let state, let event):
            return "invalid daemon lifecycle transition: \(event) is not valid while \(state)"
        }
    }
}

/// Explicit daemon lifecycle/repair state machine (D-005, D-014).
///
/// Pure and synchronous: callers observe SMAppService, the descriptor files,
/// and the handshake, then feed events in. Every transition is either in the
/// table below or a named `DaemonLifecycleError.invalidTransition`.
public struct DaemonLifecycleMachine: Sendable, Equatable {
    public private(set) var state: DaemonLifecycleState

    public init(state: DaemonLifecycleState = .idle) {
        self.state = state
    }

    @discardableResult
    public mutating func handle(_ event: DaemonLifecycleEvent) throws -> DaemonLifecycleState {
        state = try Self.transition(from: state, on: event)
        return state
    }

    public static func transition(
        from state: DaemonLifecycleState,
        on event: DaemonLifecycleEvent
    ) throws -> DaemonLifecycleState {
        switch (state, event) {
        case (.idle, .begin):
            return .checkingRegistration

        case (.checkingRegistration, .registrationChecked(let status)):
            switch status {
            case .enabled:
                return .locatingEndpoint
            case .requiresApproval:
                return .approvalRequired
            case .notRegistered, .notFound:
                return .registrationRequired
            }

        case (.registrationRequired, .registrationSubmitted):
            return .checkingRegistration
        case (.approvalRequired, .approvalGranted):
            return .checkingRegistration

        case (.locatingEndpoint, .endpointFound(let descriptor)):
            return .connecting(descriptor)
        case (.locatingEndpoint, .endpointMissing):
            return .startingDaemon
        case (.locatingEndpoint, .endpointInvalid(let reason)):
            return .unreachable(reason: reason)

        case (.startingDaemon, .daemonStarted):
            return .locatingEndpoint
        case (.startingDaemon, .daemonStartFailed(let reason)):
            return .unreachable(reason: reason)

        case (.connecting, .handshakeSucceeded(let session)):
            return .ready(session)
        case (.connecting, .handshakeUnauthorized):
            return .unauthorized
        case (.connecting, .handshakeUpgradeRequired(let message)):
            return .upgradeRequired(message: message)
        case (.connecting, .connectionFailed(let reason)):
            return .unreachable(reason: reason)

        // The port is ephemeral, so a lost connection re-locates the
        // endpoint rather than assuming the old descriptor is still valid.
        case (.ready, .connectionLost):
            return .locatingEndpoint

        case (_, .repairRequested) where state.canRepair:
            return .repairing
        case (.repairing, .repairCompleted):
            return .checkingRegistration
        case (.repairing, .repairFailed(let reason)):
            return .unreachable(reason: reason)

        default:
            throw DaemonLifecycleError.invalidTransition(state: state.name, event: event.name)
        }
    }
}
