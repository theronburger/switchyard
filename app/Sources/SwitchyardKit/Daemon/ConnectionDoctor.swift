import Foundation

/// One diagnostic step in the Connection Doctor.
public struct DoctorCheck: Sendable, Equatable, Identifiable {
    public enum Outcome: Sendable, Equatable {
        case passed(String)
        case warning(String)
        case failed(String)
        case skipped(String)

        public var message: String {
            switch self {
            case .passed(let message), .warning(let message),
                 .failed(let message), .skipped(let message):
                return message
            }
        }
    }

    public let id: String
    public let title: String
    public let outcome: Outcome

    public init(id: String, title: String, outcome: Outcome) {
        self.id = id
        self.title = title
        self.outcome = outcome
    }
}

/// The result of a full Connection Doctor pass.
public struct DoctorReport: Sendable, Equatable {
    public let checks: [DoctorCheck]

    public init(checks: [DoctorCheck]) {
        self.checks = checks
    }

    public var isHealthy: Bool {
        !checks.contains { check in
            if case .failed = check.outcome { return true }
            return false
        }
    }

    public var summaryLine: String {
        let failed = checks.filter { if case .failed = $0.outcome { return true }; return false }
        if failed.isEmpty {
            return "All connection checks passed."
        }
        return "\(failed.count) of \(checks.count) checks failed."
    }
}

public protocol DoctorRunning: Sendable {
    func run() async -> DoctorReport
}

public struct LiveConnectionDoctor: DoctorRunning {
    private let serviceManager: any DaemonServiceManaging
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(
        serviceManager: any DaemonServiceManaging = LaunchAgentServiceManager(),
        connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()
    ) {
        self.serviceManager = serviceManager
        self.connectionFactory = connectionFactory
    }

    public func run() async -> DoctorReport {
        var checks: [DoctorCheck] = []

        do {
            let status = try await serviceManager.inspect()
            checks.append(DoctorCheck(
                id: "registration",
                title: "LaunchAgent registration",
                outcome: status == .enabled
                    ? .passed("The Switchyard user LaunchAgent is loaded.")
                    : .failed(Self.registrationMessage(status))
            ))
        } catch {
            checks.append(DoctorCheck(
                id: "registration",
                title: "LaunchAgent registration",
                outcome: .failed("The Switchyard user LaunchAgent could not be inspected.")
            ))
        }

        let connection: DaemonConnection?
        do {
            let established = try connectionFactory.connect()
            connection = established
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .passed("Daemon \(established.descriptor.daemonVersion) publishes a verified loopback endpoint.")
            ))
            checks.append(DoctorCheck(
                id: "process-identity",
                title: "Daemon process identity",
                outcome: .passed("The endpoint descriptor belongs to the running daemon process.")
            ))
            checks.append(DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .passed("The owner-only daemon token is available.")
            ))
        } catch let error as RuntimeConnectionError {
            connection = nil
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint and credentials",
                outcome: .failed(error.description)
            ))
        } catch {
            connection = nil
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint and credentials",
                outcome: .failed("The daemon runtime connection could not be established.")
            ))
        }

        if let connection {
            var handshakeSucceeded = false
            do {
                let handshake = try await connection.client.handshake()
                handshakeSucceeded = true
                checks.append(DoctorCheck(
                    id: "handshake",
                    title: "Daemon handshake",
                    outcome: .passed("Daemon \(handshake.daemonVersion) answered with contract v\(handshake.schemaVersion).")
                ))
            } catch let error as DaemonClientError {
                checks.append(DoctorCheck(
                    id: "handshake",
                    title: "Daemon handshake",
                    outcome: .failed(error.description)
                ))
            } catch {
                checks.append(DoctorCheck(
                    id: "handshake",
                    title: "Daemon handshake",
                    outcome: .failed("The daemon handshake could not be completed.")
                ))
            }
            if handshakeSucceeded {
                do {
                    _ = try await connection.client.status()
                    checks.append(DoctorCheck(
                        id: "status",
                        title: "Status snapshot",
                        outcome: .passed("The authenticated status contract is available.")
                    ))
                } catch let error as DaemonClientError {
                    checks.append(DoctorCheck(
                        id: "status",
                        title: "Status snapshot",
                        outcome: .failed(error.description)
                    ))
                } catch {
                    checks.append(DoctorCheck(
                        id: "status",
                        title: "Status snapshot",
                        outcome: .failed("The daemon status request could not be completed.")
                    ))
                }
            } else {
                checks.append(DoctorCheck(
                    id: "status",
                    title: "Status snapshot",
                    outcome: .skipped("Skipped because the authenticated daemon handshake failed.")
                ))
            }
        } else {
            checks.append(DoctorCheck(
                id: "handshake",
                title: "Daemon handshake",
                outcome: .skipped("Skipped because the verified runtime connection is unavailable.")
            ))
            checks.append(DoctorCheck(
                id: "status",
                title: "Status snapshot",
                outcome: .skipped("Skipped because the verified runtime connection is unavailable.")
            ))
        }

        return DoctorReport(checks: checks)
    }

    private static func registrationMessage(_ status: DaemonRegistrationStatus) -> String {
        switch status {
        case .enabled:
            return "The Switchyard user LaunchAgent is loaded."
        case .notRegistered:
            return "The Switchyard user LaunchAgent is not installed."
        case .requiresApproval:
            return "macOS has not loaded the Switchyard user LaunchAgent."
        case .notFound:
            return "The installed Switchyard daemon binary is missing."
        case .outdated:
            return "The installed Switchyard helper does not match this app."
        }
    }
}

extension DoctorReport {
    /// Canned report matching a healthy fixture daemon, used while the app is
    /// fixture-driven.
    public static func fixtureHealthy(daemon: DaemonStatus) -> DoctorReport {
        DoctorReport(checks: [
            DoctorCheck(
                id: "registration",
                title: "Login item registration",
                outcome: .passed("Helper is registered and enabled (fixture).")
            ),
            DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .passed("Daemon \(daemon.version) publishes a loopback endpoint (fixture).")
            ),
            DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .passed("Token file is present and owner-only (fixture).")
            ),
            DoctorCheck(
                id: "handshake",
                title: "Daemon handshake",
                outcome: .passed("Daemon \(daemon.version) answered with contract v\(contractSchemaVersion) (fixture).")
            ),
        ])
    }

    /// Canned report for the simulated-failure fixture scenario.
    public static func fixtureUnreachable(reason: String) -> DoctorReport {
        DoctorReport(checks: [
            DoctorCheck(
                id: "registration",
                title: "Login item registration",
                outcome: .passed("Helper is registered and enabled (fixture).")
            ),
            DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .failed(reason)
            ),
            DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .skipped("Skipped because the endpoint descriptor is unavailable.")
            ),
            DoctorCheck(
                id: "handshake",
                title: "Daemon handshake",
                outcome: .skipped("Skipped because the endpoint descriptor is unavailable.")
            ),
        ])
    }
}
