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

/// Runs real diagnostics against the daemon's on-disk runtime files and, when
/// they are present, the handshake itself. The transport is injectable so the
/// doctor is verifiable without a live daemon.
public struct LiveConnectionDoctor: Sendable {
    public let location: DaemonEndpointLocation
    private let transport: any DaemonTransport

    public init(
        location: DaemonEndpointLocation = .standard(),
        transport: any DaemonTransport = URLSessionDaemonTransport()
    ) {
        self.location = location
        self.transport = transport
    }

    public func run() async -> DoctorReport {
        var checks: [DoctorCheck] = []

        checks.append(DoctorCheck(
            id: "registration",
            title: "Login item registration",
            outcome: .skipped("SMAppService wiring arrives with the packaged app bundle.")
        ))

        let descriptor: EndpointDescriptor?
        do {
            let loaded = try EndpointDescriptorLoader().load(from: location.descriptorURL)
            descriptor = loaded
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .passed("Daemon \(loaded.daemonVersion) publishes loopback port \(loaded.port).")
            ))
        } catch let error as EndpointDescriptorError {
            descriptor = nil
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .failed(error.description)
            ))
        } catch {
            descriptor = nil
            checks.append(DoctorCheck(
                id: "endpoint-descriptor",
                title: "Endpoint descriptor",
                outcome: .failed(String(describing: error))
            ))
        }

        let token: BearerToken?
        do {
            token = try BearerToken.load(from: location.tokenURL)
            checks.append(DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .passed("Token file is present and owner-only.")
            ))
        } catch let error as BearerTokenError {
            token = nil
            checks.append(DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .failed(error.description)
            ))
        } catch {
            token = nil
            checks.append(DoctorCheck(
                id: "token",
                title: "Daemon token",
                outcome: .failed(String(describing: error))
            ))
        }

        if let descriptor, let token {
            do {
                let client = try DaemonClient(descriptor: descriptor, token: token, transport: transport)
                let handshake = try await client.handshake()
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
                    outcome: .failed(String(describing: error))
                ))
            }
        } else {
            checks.append(DoctorCheck(
                id: "handshake",
                title: "Daemon handshake",
                outcome: .skipped("Skipped because the endpoint descriptor or token is unavailable.")
            ))
        }

        return DoctorReport(checks: checks)
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
