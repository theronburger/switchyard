import Foundation
import SwitchyardKit

// Dependency-free conformance and lifecycle suite. This machine's Command
// Line Tools toolchain ships without XCTest, so verification runs as a plain
// executable: `SwitchyardContractCheck contracts/v1/fixtures/status.json`.

struct CheckError: Error, CustomStringConvertible {
    let description: String

    init(_ description: String) {
        self.description = description
    }
}

func expect(_ condition: Bool, _ message: String) throws {
    if !condition {
        throw CheckError(message)
    }
}

@MainActor
final class Runner {
    private(set) var passed = 0
    private(set) var failures: [String] = []

    func check(_ name: String, _ body: () throws -> Void) {
        do {
            try body()
            passed += 1
        } catch {
            failures.append("\(name): \(error)")
        }
    }

    func checkAsync(_ name: String, _ body: () async throws -> Void) async {
        do {
            try await body()
            passed += 1
        } catch {
            failures.append("\(name): \(error)")
        }
    }
}

struct MockTransport: DaemonTransport {
    let handler: @Sendable (URLRequest) throws -> (Data, HTTPURLResponse)

    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        try handler(request)
    }
}

func httpResponse(for request: URLRequest, status: Int) -> HTTPURLResponse {
    HTTPURLResponse(
        url: request.url ?? URL(fileURLWithPath: "/"),
        statusCode: status,
        httpVersion: "HTTP/1.1",
        headerFields: [
            "Cache-Control": "no-store",
            "X-Content-Type-Options": "nosniff",
        ]
    )!
}

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: SwitchyardContractCheck STATUS_FIXTURE\n".utf8))
    exit(2)
}

let fixtureURL = URL(fileURLWithPath: CommandLine.arguments[1])
let fixtureData = try Data(contentsOf: fixtureURL)
let runner = Runner()

// MARK: - Contract decoding

runner.check("canonical fixture decodes with expected fields") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    try expect(snapshot.schemaVersion == contractSchemaVersion, "unexpected schema version \(snapshot.schemaVersion)")
    try expect(snapshot.snapshotRevision == 42, "unexpected snapshot revision")
    try expect(snapshot.daemon.state == .ready, "daemon state did not decode")
    try expect(snapshot.repositories.count == 1, "expected one repository")
    let repository = snapshot.repositories[0]
    try expect(repository.adapter == "marketplace", "repository adapter did not decode")
    try expect(repository.worktrees.count == 1, "expected one worktree")
    try expect(repository.worktrees[0].git.isClean, "canonical worktree should be clean")

    guard let environment = snapshot.environments.first else {
        throw CheckError("canonical environment is missing")
    }
    try expect(environment.displayName == "DEMO-830", "canonical environment is missing")
    try expect(environment.revision == 17, "environment revision did not decode")
    try expect(environment.health == .degraded, "canonical environment health did not decode")
    try expect(environment.desiredState == .running, "desired state did not decode")
    try expect(environment.observedState == .running, "observed state did not decode")
    try expect(environment.services.count == 2, "expected two services")

    guard let organizer = environment.services.first(where: { $0.id == "organizer" }) else {
        throw CheckError("organizer service is missing")
    }
    try expect(organizer.run?.cpuPercent == 8.2, "organizer run cpu did not decode")
    try expect(organizer.run?.processCount == 7, "organizer run process count did not decode")

    guard let nonprofit = environment.services.first(where: { $0.id == "nonprofit-service" }) else {
        throw CheckError("nonprofit service is missing")
    }
    try expect(nonprofit.observedState == .exited, "nonprofit observed state did not decode")
    try expect(nonprofit.health == .unhealthy, "nonprofit health did not decode")
    try expect(nonprofit.run?.restartCount == 2, "nonprofit restart count did not decode")

    try expect(environment.portLeases.count == 3, "expected three port leases")
    try expect(environment.portLease(withId: "port_organizer_http")?.port == 7005, "organizer port did not decode")
    try expect(environment.portLeases(for: nonprofit).count == 2, "nonprofit port leases did not resolve")
    try expect(environment.infrastructureLeases.first?.ownership == "owned", "infrastructure ownership did not decode")
    try expect(environment.urls.count == 2, "expected two URLs")
    try expect(environment.sortedURLs.first?.service == "nonprofit-service", "URL ordering is not deterministic")
    try expect(environment.resources.memoryBytes == 1_400_000_000, "aggregate memory did not decode")

    try expect(snapshot.operations.first?.state == .succeeded, "operation state did not decode")
    try expect(snapshot.alerts.first?.code == "SERVICE_EXITED", "alert code did not decode")
    try expect(snapshot.alerts.first?.severity == .error, "alert severity did not decode")
}

runner.check("canonical fixture derivations are correct") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    let summary = snapshot.summary
    try expect(summary.environmentCount == 1, "environment count")
    try expect(summary.runningCount == 1, "running count")
    try expect(summary.unhealthyCount == 0, "unhealthy count counts only unhealthy environments")
    try expect(summary.attentionCount == 1, "degraded environment needs attention")
    try expect(summary.activeAlertCount == 1, "active alert count")
    try expect(summary.totalCPUPercent == 8.2, "aggregate cpu")
    try expect(summary.totalMemoryBytes == 1_400_000_000, "aggregate memory")

    guard let environment = snapshot.environments.first else {
        throw CheckError("canonical environment is missing")
    }
    try expect(snapshot.repository(for: environment)?.displayName == "marketplace", "repository lookup")
    try expect(snapshot.worktree(for: environment)?.branch == "feature/demo-environment", "worktree lookup")
    try expect(snapshot.alerts(forEnvironment: environment.id).count == 1, "environment alert lookup")
    try expect(snapshot.operations(forEnvironment: environment.id).count == 1, "environment operation lookup")
    try expect(environment.totalProcessCount == 7, "total process count")
}

runner.check("unknown enum values decode forward-compatibly") {
    let decoder = ContractDecoder()
    func decodesToUnknown<Value: ForwardCompatibleDecodable & Equatable>(_ type: Value.Type) throws {
        let value = try decoder.decode(type, from: Data("\"future-value\"".utf8))
        try expect(value == Value.unknown, "\(type) did not map an unknown raw value to .unknown")
    }
    try decodesToUnknown(DaemonState.self)
    try decodesToUnknown(DesiredState.self)
    try decodesToUnknown(ObservedState.self)
    try decodesToUnknown(Health.self)
    try decodesToUnknown(OperationState.self)
    try decodesToUnknown(AlertSeverity.self)
    try decodesToUnknown(AlertStatus.self)
}

runner.check("additive fields are ignored") {
    let json = """
    {
      "schemaVersion": 1,
      "snapshotRevision": 3,
      "generatedAt": "2026-08-14T08:00:00Z",
      "futureTopLevelField": {"nested": true},
      "daemon": {
        "instanceId": "daemon_future",
        "version": "9.9.9",
        "state": "hibernating",
        "startedAt": "2026-08-14T07:59:00Z",
        "futureDaemonField": [1, 2, 3]
      },
      "repositories": [],
      "environments": [],
      "operations": [],
      "alerts": []
    }
    """
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: Data(json.utf8))
    try expect(snapshot.snapshotRevision == 3, "revision did not decode alongside unknown fields")
    try expect(snapshot.daemon.state == .unknown, "unknown daemon state did not map to .unknown")
}

runner.check("fractional RFC 3339 timestamps decode") {
    let json = """
    {
      "schemaVersion": 1,
      "endpoint": "http://127.0.0.1:49402",
      "daemonInstanceId": "daemon_fractional",
      "daemonVersion": "0.1.0-dev",
      "pid": 42,
      "processStartedAt": "2026-08-14T10:00:00.123456Z",
      "generatedAt": "2026-08-14T12:00:00.123456789+02:00"
    }
    """
    let descriptor = try ContractDecoder().decode(EndpointDescriptor.self, from: Data(json.utf8))
    try expect(
        abs(descriptor.generatedAt.timeIntervalSince(descriptor.processStartedAt)) < 0.000_001,
        "equivalent fractional timestamps decoded differently"
    )
}

runner.check("environment context footer decodes with caps") {
    let json = """
    {
      "revision": 42,
      "environmentId": "env_01J5EXAMPLE",
      "desiredState": "running",
      "observedState": "running",
      "health": "degraded",
      "urls": {"organizer": "http://127.0.0.1:7005"},
      "attentionCount": 5,
      "attention": [
        {"severity": "error", "code": "SERVICE_CRASHED", "summary": "one"},
        {"severity": "warning", "code": "PORT_CONFLICT", "summary": "two"},
        {"severity": "info", "code": "SLOW_START", "summary": "three"}
      ],
      "truncated": true
    }
    """
    let context = try ContractDecoder().decode(EnvironmentContext.self, from: Data(json.utf8))
    try expect(context.attention.count == 3, "attention entries did not decode")
    try expect(context.attentionCount == 5, "attention count did not decode")
    try expect(context.truncated, "truncated flag did not decode")
    try expect(context.health == .degraded, "context health did not decode")
}

// MARK: - Endpoint descriptor and token

let sampleDescriptor = EndpointDescriptor(
    schemaVersion: 1,
    transport: "http",
    host: "127.0.0.1",
    port: 49402,
    daemonVersion: "0.1.0-dev",
    instanceId: "daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
    pid: 4242,
    createdAt: Date(timeIntervalSince1970: 1_784_000_000)
)
let sampleTokenRaw = "fixture-value-not-a-secret"

runner.check("endpoint descriptor validates invariants") {
    try sampleDescriptor.validate()
    let url = try sampleDescriptor.loopbackBaseURL()
    try expect(url.absoluteString == "http://127.0.0.1:49402", "unexpected base URL \(url.absoluteString)")

    func expectValidationError(
        _ descriptor: EndpointDescriptor,
        _ expected: EndpointDescriptorError
    ) throws {
        do {
            try descriptor.validate()
            throw CheckError("expected \(expected) but validation passed")
        } catch let error as EndpointDescriptorError {
            try expect(error == expected, "expected \(expected), got \(error)")
        }
    }

    try expectValidationError(
        EndpointDescriptor(schemaVersion: 2, transport: "http", host: "127.0.0.1", port: 1, daemonVersion: "x", instanceId: "y"),
        .unsupportedSchemaVersion(2)
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: 1, transport: "unix", host: "127.0.0.1", port: 1, daemonVersion: "x", instanceId: "y"),
        .unsupportedTransport("unix")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: 1, transport: "http", host: "192.168.1.5", port: 1, daemonVersion: "x", instanceId: "y"),
        .nonLoopbackHost("192.168.1.5")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: 1, transport: "http", host: "localhost", port: 1, daemonVersion: "x", instanceId: "y"),
        .nonLoopbackHost("localhost")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: 1, transport: "http", host: "127.0.0.1", port: 0, daemonVersion: "x", instanceId: "y"),
        .invalidPort(0)
    )
    try expect(!EndpointDescriptor.isLoopback("127.5.5.5"), "only the canonical IPv4 loopback is allowed")
    try expect(!EndpointDescriptor.isLoopback("::1"), "IPv6 loopback is outside the frozen contract")
    try expect(!EndpointDescriptor.isLoopback("127.0.0.999"), "invalid octets are rejected")
}

runner.check("endpoint descriptor loader enforces owner-only files") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let descriptorJSON = """
    {
      "schemaVersion": 1,
      "endpoint": "http://127.0.0.1:49402",
      "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
      "daemonVersion": "0.1.0-dev",
      "pid": 4242,
      "processStartedAt": "2026-08-14T09:00:00Z",
      "generatedAt": "2026-08-14T09:00:01Z"
    }
    """

    let privateURL = directory.appending(path: "endpoint.json")
    fileManager.createFile(
        atPath: privateURL.path,
        contents: Data(descriptorJSON.utf8),
        attributes: [.posixPermissions: 0o600]
    )
    let loaded = try EndpointDescriptorLoader().load(from: privateURL)
    try expect(loaded.port == 49402, "descriptor port did not load")
    try expect(loaded.instanceId == "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "descriptor instance did not load")

    let openURL = directory.appending(path: "endpoint-open.json")
    fileManager.createFile(
        atPath: openURL.path,
        contents: Data(descriptorJSON.utf8),
        attributes: [.posixPermissions: 0o644]
    )
    do {
        _ = try EndpointDescriptorLoader().load(from: openURL)
        throw CheckError("expected insecurePermissions for a 0644 descriptor")
    } catch let error as EndpointDescriptorError {
        try expect(error == .insecurePermissions(octal: "644"), "expected insecurePermissions(644), got \(error)")
    }

    let missingURL = directory.appending(path: "missing.json")
    do {
        _ = try EndpointDescriptorLoader().load(from: missingURL)
        throw CheckError("expected unreadable for a missing descriptor")
    } catch let error as EndpointDescriptorError {
        try expect(error == .unreadable(missingURL.path), "expected unreadable, got \(error)")
    }
}

runner.check("runtime file reader rejects symlinks non-files and oversized data") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let privateURL = directory.appending(path: "private")
    fileManager.createFile(
        atPath: privateURL.path,
        contents: Data("private".utf8),
        attributes: [.posixPermissions: 0o600]
    )
    let contents = try readSecureRuntimeFile(at: privateURL, maximumBytes: 64)
    try expect(contents == Data("private".utf8), "secure file contents changed")

    let symlinkURL = directory.appending(path: "link")
    try fileManager.createSymbolicLink(at: symlinkURL, withDestinationURL: privateURL)
    do {
        _ = try readSecureRuntimeFile(at: symlinkURL, maximumBytes: 64)
        throw CheckError("runtime symlink was accepted")
    } catch let error as SecureFileError {
        try expect(error.problem == .symlink, "expected symlink refusal, got \(error)")
    }

    do {
        _ = try readSecureRuntimeFile(at: directory, maximumBytes: 64)
        throw CheckError("runtime directory was accepted as a file")
    } catch let error as SecureFileError {
        try expect(error.problem == .notRegular, "expected non-file refusal, got \(error)")
    }

    do {
        _ = try readSecureRuntimeFile(at: privateURL, maximumBytes: 4)
        throw CheckError("oversized runtime file was accepted")
    } catch let error as SecureFileError {
        try expect(error.problem == .oversized(limitBytes: 4), "expected size refusal, got \(error)")
    }
}

runner.check("bearer token stays redacted and owner-only") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    try expect("\(token)" == "BearerToken(redacted)", "token description leaks")
    try expect(!String(describing: token).contains(sampleTokenRaw), "token description contains the secret")
    try expect(token.authorizationHeaderValue == "Bearer \(sampleTokenRaw)", "authorization header is wrong")

    do {
        _ = try BearerToken(rawValue: "short")
        throw CheckError("expected invalidFormat")
    } catch let error as BearerTokenError {
        try expect(error == .invalidFormat, "expected invalidFormat, got \(error)")
    }
    do {
        _ = try BearerToken(rawValue: "has an interior space padded long enough")
        throw CheckError("expected containsWhitespace")
    } catch let error as BearerTokenError {
        try expect(error == .containsWhitespace, "expected containsWhitespace, got \(error)")
    }

    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let privateURL = directory.appending(path: "daemon.token")
    fileManager.createFile(
        atPath: privateURL.path,
        contents: Data("\(sampleTokenRaw)\n".utf8),
        attributes: [.posixPermissions: 0o600]
    )
    let loadedToken = try BearerToken.load(from: privateURL)
    try expect(loadedToken == token, "trailing newline should be trimmed on load")

    let openURL = directory.appending(path: "open.token")
    fileManager.createFile(
        atPath: openURL.path,
        contents: Data(sampleTokenRaw.utf8),
        attributes: [.posixPermissions: 0o640]
    )
    do {
        _ = try BearerToken.load(from: openURL)
        throw CheckError("expected insecurePermissions for a 0640 token")
    } catch let error as BearerTokenError {
        try expect(error == .insecurePermissions(octal: "640"), "expected insecurePermissions(640), got \(error)")
    }
}

// MARK: - Daemon client

await runner.checkAsync("daemon client authenticates and decodes status") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let statusData = fixtureData
    let transport = MockTransport { request in
        try expect(request.url?.path() == "/v1/status", "unexpected path \(request.url?.path() ?? "nil")")
        try expect(request.url?.host() == "127.0.0.1", "request must stay on loopback")
        try expect(request.url?.port == 49402, "request must target the descriptor port")
        try expect(
            request.value(forHTTPHeaderField: "Authorization") == "Bearer \(sampleTokenRaw)",
            "authorization header is missing or wrong"
        )
        try expect(
            request.url?.absoluteString.contains(sampleTokenRaw) == false,
            "token leaked into the URL"
        )
        try expect(request.value(forHTTPHeaderField: "X-Switchyard-Request-Id") != nil, "request id header is missing")
        return (statusData, httpResponse(for: request, status: 200))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    let snapshot = try await client.status()
    try expect(snapshot.snapshotRevision == 42, "status snapshot did not decode through the client")
}

await runner.checkAsync("daemon client maps unauthorized responses") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let transport = MockTransport { request in
        (Data(), httpResponse(for: request, status: 401))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    do {
        _ = try await client.handshake()
        throw CheckError("expected unauthorized")
    } catch let error as DaemonClientError {
        guard case .unauthorized = error else {
            throw CheckError("expected unauthorized, got \(error)")
        }
    }
}

await runner.checkAsync("daemon client maps the stable upgrade-required error") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let body = """
    {"error": {"code": "UPGRADE_REQUIRED", "message": "daemon 0.1.0 requires app 0.2.0", "retryable": false}}
    """
    let transport = MockTransport { request in
        (Data(body.utf8), httpResponse(for: request, status: 409))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    do {
        _ = try await client.handshake()
        throw CheckError("expected upgradeRequired")
    } catch let error as DaemonClientError {
        guard case .upgradeRequired(let message) = error else {
            throw CheckError("expected upgradeRequired, got \(error)")
        }
        try expect(message == "daemon 0.1.0 requires app 0.2.0", "upgrade message did not survive")
    }
}

await runner.checkAsync("daemon client surfaces contract errors") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let body = """
    {"error": {"code": "ENVIRONMENT_BUSY", "message": "operation in flight", "retryable": true}}
    """
    let transport = MockTransport { request in
        (Data(body.utf8), httpResponse(for: request, status: 429))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    do {
        _ = try await client.status()
        throw CheckError("expected contract error")
    } catch let error as DaemonClientError {
        guard case .contract(let contractError) = error else {
            throw CheckError("expected contract error, got \(error)")
        }
        try expect(contractError.code == "ENVIRONMENT_BUSY", "contract error code did not decode")
        try expect(contractError.retryable, "contract error retryable flag did not decode")
    }
}

// MARK: - Daemon lifecycle state machine

let sampleSession = DaemonSession(
    instanceId: "daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
    daemonVersion: "0.1.0-dev",
    endpoint: sampleDescriptor
)

runner.check("lifecycle happy path reaches ready") {
    var machine = DaemonLifecycleMachine()
    try expect(machine.state == .idle, "machine must start idle")
    try machine.handle(.begin)
    try machine.handle(.registrationChecked(.enabled))
    try expect(machine.state == .locatingEndpoint, "enabled registration should locate the endpoint")
    try machine.handle(.endpointFound(sampleDescriptor))
    try expect(machine.state == .connecting(sampleDescriptor), "descriptor should move to connecting")
    try machine.handle(.handshakeSucceeded(sampleSession))
    try expect(machine.state == .ready(sampleSession), "handshake should reach ready")
    try expect(machine.state.isOperational, "ready must be operational")
    try expect(machine.state.canRepair, "ready still offers Repair All")

    // Ephemeral port: a lost connection re-locates rather than reusing the
    // old descriptor.
    try machine.handle(.connectionLost(reason: "socket closed"))
    try expect(machine.state == .locatingEndpoint, "lost connection should re-locate the endpoint")
}

runner.check("lifecycle install, approval, and kickstart paths") {
    var machine = DaemonLifecycleMachine()
    try machine.handle(.begin)
    try machine.handle(.registrationChecked(.notRegistered))
    try expect(machine.state == .registrationRequired, "missing registration should require install")
    try expect(machine.state.needsUserAction, "registrationRequired needs user action")
    try machine.handle(.registrationSubmitted)
    try machine.handle(.registrationChecked(.requiresApproval))
    try expect(machine.state == .approvalRequired, "approval must surface")
    try machine.handle(.approvalGranted)
    try machine.handle(.registrationChecked(.enabled))
    try machine.handle(.endpointMissing)
    try expect(machine.state == .startingDaemon, "missing endpoint should kick launchd")
    try machine.handle(.daemonStarted)
    try expect(machine.state == .locatingEndpoint, "kicked daemon should be re-located")
    try machine.handle(.endpointInvalid(reason: "stale descriptor"))
    try expect(machine.state == .unreachable(reason: "stale descriptor"), "invalid descriptor should be unreachable")
}

runner.check("lifecycle repair paths") {
    var machine = DaemonLifecycleMachine(state: .unreachable(reason: "daemon missing"))
    try machine.handle(.repairRequested)
    try expect(machine.state == .repairing, "repair should start")
    try expect(!machine.state.canRepair, "repairing cannot start another repair")
    try machine.handle(.repairCompleted)
    try expect(machine.state == .checkingRegistration, "repair completion restarts the flow")

    var failing = DaemonLifecycleMachine(state: .repairing)
    try failing.handle(.repairFailed(reason: "install rejected"))
    try expect(failing.state == .unreachable(reason: "install rejected"), "failed repair is unreachable")

    var unauthorized = DaemonLifecycleMachine(state: .connecting(sampleDescriptor))
    try unauthorized.handle(.handshakeUnauthorized)
    try expect(unauthorized.state == .unauthorized, "unauthorized handshake state")
    try unauthorized.handle(.repairRequested)
    try expect(unauthorized.state == .repairing, "unauthorized offers repair")

    var upgrade = DaemonLifecycleMachine(state: .connecting(sampleDescriptor))
    try upgrade.handle(.handshakeUpgradeRequired(message: "daemon is newer than the app"))
    try expect(upgrade.state == .upgradeRequired(message: "daemon is newer than the app"), "upgrade state")
    try expect(upgrade.state.needsUserAction, "upgradeRequired needs user action")
}

runner.check("lifecycle rejects invalid transitions with named errors") {
    func expectInvalid(
        from state: DaemonLifecycleState,
        on event: DaemonLifecycleEvent,
        stateName: String,
        eventName: String
    ) throws {
        let result: Result<DaemonLifecycleState, Error> = Result {
            try DaemonLifecycleMachine.transition(from: state, on: event)
        }
        switch result {
        case .success:
            throw CheckError("expected invalid transition \(stateName) + \(eventName)")
        case .failure(let error):
            guard let lifecycleError = error as? DaemonLifecycleError else {
                throw error
            }
            try expect(
                lifecycleError == .invalidTransition(state: stateName, event: eventName),
                "expected invalidTransition(\(stateName), \(eventName)), got \(lifecycleError)"
            )
        }
    }

    try expectInvalid(
        from: .idle,
        on: .handshakeSucceeded(sampleSession),
        stateName: "idle",
        eventName: "handshakeSucceeded"
    )
    try expectInvalid(
        from: .repairing,
        on: .repairRequested,
        stateName: "repairing",
        eventName: "repairRequested"
    )
    try expectInvalid(
        from: .connecting(sampleDescriptor),
        on: .repairRequested,
        stateName: "connecting",
        eventName: "repairRequested"
    )
    try expectInvalid(
        from: .ready(sampleSession),
        on: .begin,
        stateName: "ready",
        eventName: "begin"
    )
    try expectInvalid(
        from: .locatingEndpoint,
        on: .approvalGranted,
        stateName: "locatingEndpoint",
        eventName: "approvalGranted"
    )
}

runner.check("lifecycle states describe themselves") {
    let states: [DaemonLifecycleState] = [
        .idle, .checkingRegistration, .registrationRequired, .approvalRequired,
        .startingDaemon, .locatingEndpoint, .connecting(sampleDescriptor),
        .ready(sampleSession), .unauthorized,
        .upgradeRequired(message: "update"), .unreachable(reason: "gone"), .repairing,
    ]
    for state in states {
        try expect(!state.name.isEmpty, "state name missing")
        try expect(!state.displayName.isEmpty, "display name missing for \(state.name)")
        try expect(!state.summary.isEmpty, "summary missing for \(state.name)")
    }
}

// MARK: - Connection Doctor

await runner.checkAsync("live doctor reports missing runtime files") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let location = DaemonEndpointLocation(
        descriptorURL: directory.appending(path: "endpoint.json"),
        tokenURL: directory.appending(path: "daemon.token")
    )
    let doctor = LiveConnectionDoctor(
        location: location,
        transport: MockTransport { request in (Data(), httpResponse(for: request, status: 200)) }
    )
    let report = await doctor.run()
    try expect(!report.isHealthy, "missing files must be unhealthy")
    let descriptorCheck = report.checks.first { $0.id == "endpoint-descriptor" }
    guard case .failed = descriptorCheck?.outcome else {
        throw CheckError("descriptor check should fail when the file is missing")
    }
    let handshakeCheck = report.checks.first { $0.id == "handshake" }
    guard case .skipped = handshakeCheck?.outcome else {
        throw CheckError("handshake should be skipped without descriptor and token")
    }
}

await runner.checkAsync("live doctor passes with valid files and a responsive daemon") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let descriptorJSON = """
    {"schemaVersion": 1, "endpoint": "http://127.0.0.1:49402",
     "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "daemonVersion": "0.1.0-dev", "pid": 4242,
     "processStartedAt": "2026-08-14T09:00:00Z", "generatedAt": "2026-08-14T09:00:01Z"}
    """
    let location = DaemonEndpointLocation(
        descriptorURL: directory.appending(path: "endpoint.json"),
        tokenURL: directory.appending(path: "daemon.token")
    )
    fileManager.createFile(
        atPath: location.descriptorURL.path,
        contents: Data(descriptorJSON.utf8),
        attributes: [.posixPermissions: 0o600]
    )
    fileManager.createFile(
        atPath: location.tokenURL.path,
        contents: Data(sampleTokenRaw.utf8),
        attributes: [.posixPermissions: 0o600]
    )

    let handshakeBody = """
    {"schemaVersion": 1, "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "daemonVersion": "0.1.0-dev",
     "supportedSchemaVersions": [1]}
    """
    let doctor = LiveConnectionDoctor(
        location: location,
        transport: MockTransport { request in
            (Data(handshakeBody.utf8), httpResponse(for: request, status: 200))
        }
    )
    let report = await doctor.run()
    try expect(report.isHealthy, "doctor should be healthy: \(report.checks.map { "\($0.id)=\($0.outcome)" })")
    let handshakeCheck = report.checks.first { $0.id == "handshake" }
    guard case .passed = handshakeCheck?.outcome else {
        throw CheckError("handshake should pass against the mock daemon")
    }
}

// MARK: - AppModel scenarios

await runner.checkAsync("app model renders the canonical fixture") {
    let model = AppModel(scenario: .canonical, canonicalFixtureURL: fixtureURL)
    await model.refresh()
    try expect(model.phase == .loaded, "canonical scenario should load, got \(model.phase)")
    try expect(model.lifecycleState.isOperational, "canonical scenario should reach ready")
    guard case .ready(let session) = model.lifecycleState else {
        throw CheckError("expected ready lifecycle state")
    }
    try expect(
        session.instanceId == model.snapshot?.daemon.instanceId,
        "scripted session must match the fixture daemon"
    )
    try expect(model.summary?.attentionCount == 1, "canonical summary attention count")
    try expect(model.doctorReport?.isHealthy == true, "canonical doctor report should be healthy")
}

await runner.checkAsync("app model renders the empty scenario") {
    let model = AppModel(scenario: .empty, canonicalFixtureURL: fixtureURL)
    await model.refresh()
    try expect(model.phase == .empty, "empty scenario should report empty, got \(model.phase)")
    try expect(model.lifecycleState.isOperational, "empty scenario still has a healthy daemon")
    try expect(model.summary?.environmentCount == 0, "empty summary should be zero")
}

await runner.checkAsync("app model surfaces the failure scenario and repairs") {
    let model = AppModel(scenario: .failure, canonicalFixtureURL: fixtureURL)
    await model.refresh()
    guard case .failed(let message) = model.phase else {
        throw CheckError("failure scenario should fail, got \(model.phase)")
    }
    try expect(!message.isEmpty, "failure message should be user-readable")
    try expect(model.lifecycleState.name == "unreachable", "failure scenario should be unreachable")
    try expect(model.lifecycleState.canRepair, "unreachable must offer Repair All")
    try expect(model.doctorReport?.isHealthy == false, "failure doctor report should be unhealthy")

    await model.repairAll()
    try expect(
        model.lifecycleState.name == "unreachable",
        "repair against a still-broken daemon lands back in unreachable"
    )

    await model.select(scenario: .canonical)
    try expect(model.phase == .loaded, "switching to canonical should recover")
    try expect(model.lifecycleState.isOperational, "recovered scenario should be ready")
}

// MARK: - Report

if runner.failures.isEmpty {
    print("SwitchyardContractCheck: \(runner.passed) checks passed")
    exit(0)
} else {
    for failure in runner.failures {
        FileHandle.standardError.write(Data("contract check failed: \(failure)\n".utf8))
    }
    FileHandle.standardError.write(
        Data("SwitchyardContractCheck: \(runner.failures.count) of \(runner.passed + runner.failures.count) checks failed\n".utf8)
    )
    exit(1)
}
