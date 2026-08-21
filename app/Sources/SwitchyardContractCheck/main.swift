import Foundation
import SwitchyardKit

// Dependency-free conformance and lifecycle suite. This machine's Command
// Line Tools toolchain ships without XCTest, so verification runs as a plain
// executable: `SwitchyardContractCheck contracts/v2/fixtures/status.json`.

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

final class LockedRecorder<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [Value] = []

    func append(_ value: Value) {
        lock.lock()
        storage.append(value)
        lock.unlock()
    }

    var values: [Value] {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }
}

final class LockedBox<Value>: @unchecked Sendable {
    private let lock = NSLock()
    private var storage: Value

    init(_ value: Value) {
        storage = value
    }

    func read() -> Value {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }

    func update(_ body: (inout Value) -> Void) {
        lock.lock()
        body(&storage)
        lock.unlock()
    }
}

struct StubDescriptorProvider: EndpointDescriptorProviding {
    let descriptor: EndpointDescriptor
    let events: LockedRecorder<String>?

    func loadDescriptor(from url: URL) throws -> EndpointDescriptor {
        events?.append("descriptor")
        return descriptor
    }
}

struct StubProcessIdentityProvider: ProcessIdentityProviding {
    let identity: ProcessIdentity
    let events: LockedRecorder<String>?

    func processIdentity(forPID pid: Int) throws -> ProcessIdentity {
        events?.append("identity")
        return identity
    }
}

struct StubTokenProvider: BearerTokenProviding {
    let token: BearerToken
    let events: LockedRecorder<String>?

    func loadToken(from url: URL) throws -> BearerToken {
        events?.append("token")
        return token
    }
}

actor StubServiceManager: DaemonServiceManaging {
    var status: DaemonRegistrationStatus
    private(set) var calls: [String] = []

    init(status: DaemonRegistrationStatus) {
        self.status = status
    }

    func inspect() async throws -> DaemonRegistrationStatus {
        calls.append("inspect")
        return status
    }

    func install() async throws {
        calls.append("install")
        status = .enabled
    }

    func kickstart() async throws {
        calls.append("kickstart")
    }

    func repair() async throws {
        calls.append("repair")
        status = .enabled
    }
}

struct StubDoctor: DoctorRunning {
    let report: DoctorReport

    func run() async -> DoctorReport { report }
}

final class StubConnectionFactory: RuntimeConnectionEstablishing, @unchecked Sendable {
    private let lock = NSLock()
    private var outcomes: [Result<DaemonConnection, RuntimeConnectionError>]

    init(_ outcomes: [Result<DaemonConnection, RuntimeConnectionError>]) {
        self.outcomes = outcomes
    }

    func connect() throws -> DaemonConnection {
        lock.lock()
        let outcome = outcomes.count > 1 ? outcomes.removeFirst() : outcomes[0]
        lock.unlock()
        return try outcome.get()
    }
}

struct StubSleeper: LifecycleSleeping {
    let calls: LockedRecorder<Duration>

    func sleep(for duration: Duration) async throws {
        calls.append(duration)
    }
}

final class RecordingExactRunner: ExactArgvRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var storage: [ExactCommand] = []
    let handler: @Sendable (ExactCommand) throws -> ExactCommandResult

    init(handler: @escaping @Sendable (ExactCommand) throws -> ExactCommandResult) {
        self.handler = handler
    }

    func run(_ command: ExactCommand) throws -> ExactCommandResult {
        lock.lock()
        storage.append(command)
        lock.unlock()
        return try handler(command)
    }

    var commands: [ExactCommand] {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }
}

struct StubBinaryProvider: DaemonBinaryProviding {
    let binary: DaemonBinary

    func daemonBinary() throws -> DaemonBinary { binary }
}

struct StubLifecycleController: DaemonLifecycleControlling {
    let refreshResult: DaemonLifecycleResult
    let repairResult: DaemonLifecycleResult

    func refresh() async -> DaemonLifecycleResult { refreshResult }
    func repair() async -> DaemonLifecycleResult { repairResult }
}

actor StubEnvironmentActions: EnvironmentActionSubmitting {
    let receipt: MutationReceipt
    private(set) var starts: [StartEnvironmentRequest] = []
    private(set) var stops: [(String, StopEnvironmentRequest)] = []

    init(receipt: MutationReceipt) {
        self.receipt = receipt
    }

    func startEnvironment(_ request: StartEnvironmentRequest) async throws -> MutationReceipt {
        starts.append(request)
        return receipt
    }

    func stopEnvironment(id: String, request: StopEnvironmentRequest) async throws -> MutationReceipt {
        stops.append((id, request))
        return receipt
    }
}

actor StubWorkspaceActions: WorkspaceActionSubmitting {
    let receipt: MutationReceipt
    private(set) var creates: [CreateWorktreeRequest] = []
    private(set) var adopts: [AdoptWorktreeRequest] = []
    private(set) var archives: [ArchiveWorktreeRequest] = []
    private(set) var prepares: [PrepareWorktreeRequest] = []

    init(receipt: MutationReceipt) {
        self.receipt = receipt
    }

    func createWorktree(_ request: CreateWorktreeRequest) async throws -> MutationReceipt {
        creates.append(request)
        return receipt
    }

    func archiveWorktree(_ request: ArchiveWorktreeRequest) async throws -> MutationReceipt {
        archives.append(request)
        return receipt
    }

    func adoptWorktree(_ request: AdoptWorktreeRequest) async throws -> MutationReceipt {
        adopts.append(request)
        return receipt
    }

    func prepareWorktree(_ request: PrepareWorktreeRequest) async throws -> MutationReceipt {
        prepares.append(request)
        return receipt
    }
}

actor StubAgentConnections: AgentConnectionManaging {
    private var report: AgentConnectionReport
    private(set) var inspections = 0
    private(set) var repairedHosts: [AgentHost] = []
    private(set) var repairedAll = 0

    init(report: AgentConnectionReport) {
        self.report = report
    }

    func inspect() async -> AgentConnectionReport {
        inspections += 1
        return report
    }

    func repair(_ host: AgentHost) async -> AgentConnectionReport {
        repairedHosts.append(host)
        report = AgentConnectionReport(statuses: report.statuses.map { status in
            status.host == host
                ? AgentConnectionStatus(host: host, state: .connected, detail: "connected")
                : status
        })
        return report
    }

    func repairAll() async -> AgentConnectionReport {
        repairedAll += 1
        report = AgentConnectionReport(statuses: AgentHost.allCases.map {
            AgentConnectionStatus(host: $0, state: .connected, detail: "connected")
        })
        return report
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

func testLaunchAgentPaths(in directory: URL) -> LaunchAgentPaths {
    LaunchAgentPaths(
        installedBinaryURL: directory.appending(path: "Application Support/Switchyard/bin/switchyard"),
        commandLinkURL: directory.appending(path: ".local/bin/sy"),
        launchAgentURL: directory.appending(path: "Library/LaunchAgents/com.theronburger.switchyard.daemon.plist"),
        standardOutputURL: directory.appending(path: "Application Support/Switchyard/logs/stdout.log"),
        standardErrorURL: directory.appending(path: "Application Support/Switchyard/logs/stderr.log")
    )
}

func writeTestFile(_ data: Data, to url: URL, permissions: Int) throws {
    let fileManager = FileManager.default
    try fileManager.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    try data.write(to: url)
    try fileManager.setAttributes([.posixPermissions: permissions], ofItemAtPath: url.path)
}

func agentTestPaths(
    in directory: URL,
    codexExecutableURL: URL?,
    claudeExecutableURL: URL? = nil
) throws -> AgentConnectionPaths {
    let helper = directory.appending(path: "installed/switchyard")
    let codexConfig = directory.appending(path: "codex/config.toml")
    let claudeConfig = directory.appending(path: "claude/.claude.json")
    try writeTestFile(Data("helper".utf8), to: helper, permissions: 0o700)
    try FileManager.default.createDirectory(
        at: codexConfig.deletingLastPathComponent(),
        withIntermediateDirectories: true
    )
    try FileManager.default.createDirectory(
        at: claudeConfig.deletingLastPathComponent(),
        withIntermediateDirectories: true
    )
    return AgentConnectionPaths(
        switchyardExecutableURL: helper,
        codexExecutableURL: codexExecutableURL,
        codexConfigURL: codexConfig,
        claudeConfigURL: claudeConfig,
        claudeExecutableURL: claudeExecutableURL,
        claudeConfigDirectoryURL: claudeConfig.deletingLastPathComponent()
    )
}

func codexServerList(command: String) throws -> Data {
    try JSONSerialization.data(withJSONObject: [
        [
            "name": "foreign-http",
            "enabled": true,
            "transport": [
                "type": "streamable_http",
                "url": "https://example.invalid/mcp",
            ],
        ],
        [
            "name": "switchyard",
            "enabled": true,
            "transport": [
                "type": "stdio",
                "command": command,
                "args": ["mcp"],
                "env": NSNull(),
                "env_vars": [],
                "cwd": NSNull(),
            ],
        ],
    ])
}

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: SwitchyardContractCheck STATUS_FIXTURE\n".utf8))
    exit(2)
}

let fixtureURL = URL(fileURLWithPath: CommandLine.arguments[1])
let fixtureData = try Data(contentsOf: fixtureURL)
let runner = Runner()

// MARK: - Contract decoding

runner.check("mutation fixtures decode across the Swift boundary") {
    let fixtures = fixtureURL.deletingLastPathComponent()
    let decoder = ContractDecoder()
    let start = try decoder.decode(
        StartEnvironmentRequest.self,
        from: Data(contentsOf: fixtures.appending(path: "start-environment-request.json"))
    )
    try expect(start.schemaVersion == contractSchemaVersion, "start request schema changed")
    try expect(start.worktreeId.hasPrefix("worktree_"), "start worktree id did not decode")
    try expect(start.targetId == "testing", "start target did not decode")
    try expect(start.confirmedTargetId == nil, "safe fixture unexpectedly requires confirmation")
    try expect(start.serviceIds == ["storefront", "billing-service"], "start service selection changed")

    let stop = try decoder.decode(
        StopEnvironmentRequest.self,
        from: Data(contentsOf: fixtures.appending(path: "stop-environment-request.json"))
    )
    try expect(stop.expectedEnvironmentRevision == 3, "stop expected revision did not decode")

    let receipt = try decoder.decode(
        MutationReceipt.self,
        from: Data(contentsOf: fixtures.appending(path: "mutation-receipt.json"))
    )
    try expect(receipt.operationId.hasPrefix("operation_"), "operation receipt did not decode")
    try expect(receipt.runId == "run_01J5EZ6XDJQ3RT09H42TJFWCNR", "receipt run did not decode")
    try expect(receipt.environmentId == "environment_daad7f2bc132", "receipt environment changed")
}

runner.check("configuration, profile-action, and diagnostics fixtures decode across the Swift boundary") {
    let fixtures = fixtureURL.deletingLastPathComponent()
    let decoder = ContractDecoder()

    let configuration = try decoder.decode(
        ConfigurationStatus.self,
        from: Data(contentsOf: fixtures.appending(path: "configuration-status.json"))
    )
    try expect(configuration.state == .pending, "configuration fixture state changed")
    try expect(configuration.acceptedRevision == 3, "configuration fixture revision changed")
    try expect(configuration.candidate?.repositoryDigests["sample"] != nil, "candidate repository digest missing")
    try expect(configuration.desired?.repositories.map(\.key) == ["sample"], "desired repositories changed")
    try expect(configuration.desired?.sourceDigest?.hasPrefix("sha256:") == true, "desired digest missing")

    let actions = try decoder.decode(
        ProfileActionList.self,
        from: Data(contentsOf: fixtures.appending(path: "profile-action-list.json"))
    )
    try expect(actions.actions.count == 3, "profile action count changed")
    try expect(actions.actions[0].kind == .lifecycle && actions.actions[0].lifecycle == .prepare, "lifecycle action changed")
    try expect(actions.actions[1].kind == .command && actions.actions[1].lifecycle == nil, "command action changed")
    try expect(actions.actions[2].risk == .remoteWrite && actions.actions[2].requiresConfirmation, "remote-write action changed")
    try expect(actions.actions.allSatisfy { $0.scope != .unknown && $0.risk != .unknown }, "action vocabulary drifted")

    let run = try decoder.decode(
        RunProfileActionRequest.self,
        from: Data(contentsOf: fixtures.appending(path: "run-profile-action-request.json"))
    )
    try expect(run.actionId == "publish-preview" && run.confirmedActionId == run.actionId, "run request confirmation changed")
    try expect(run.worktreeId != nil && run.environmentId == nil && run.serviceId == nil, "run request scope changed")
    let encodedRun = try JSONEncoder().encode(RunProfileActionRequest(
        requestId: run.requestId, idempotencyKey: run.idempotencyKey, repositoryId: run.repositoryId,
        actionId: run.actionId, worktreeId: run.worktreeId, confirmedActionId: run.confirmedActionId
    ))
    let reRun = try decoder.decode(RunProfileActionRequest.self, from: encodedRun)
    try expect(reRun == run, "run request does not round-trip")

    let diagnostics = try decoder.decode(
        OperationDiagnostics.self,
        from: Data(contentsOf: fixtures.appending(path: "operation-diagnostics.json"))
    )
    try expect(diagnostics.excerpts.map(\.stream) == ["stdout", "stderr"], "diagnostics streams changed")
    try expect(diagnostics.excerpts[1].truncated && diagnostics.excerpts[1].redacted, "diagnostics flags changed")
    try expect(!diagnostics.logReference.hasPrefix("/"), "log reference must stay opaque")
}

runner.check("occupancy and upgrade fixtures decode across the Swift boundary") {
    let fixtures = fixtureURL.deletingLastPathComponent()
    let decoder = ContractDecoder()
    let lease = try decoder.decode(
        OccupancyLease.self,
        from: Data(contentsOf: fixtures.appending(path: "occupancy-lease.json"))
    )
    try expect(lease.state == .held && lease.releasedAt == nil, "occupancy lease fixture must be held")
    let acquire = try decoder.decode(
        AcquireOccupancyRequest.self,
        from: Data(contentsOf: fixtures.appending(path: "acquire-occupancy-request.json"))
    )
    let release = try decoder.decode(
        ReleaseOccupancyRequest.self,
        from: Data(contentsOf: fixtures.appending(path: "release-occupancy-request.json"))
    )
    try expect(acquire.worktreeId == lease.worktreeId && release.leaseId == lease.id, "occupancy fixtures disagree")
    try expect(acquire.holderKind == lease.holderKind && acquire.holderLabel == lease.holderLabel, "occupancy holder drifted")
    let reAcquire = try decoder.decode(AcquireOccupancyRequest.self, from: JSONEncoder().encode(AcquireOccupancyRequest(
        requestId: acquire.requestId, worktreeId: acquire.worktreeId, holderKind: acquire.holderKind, holderLabel: acquire.holderLabel
    )))
    try expect(reAcquire == acquire, "acquire request does not round-trip")

    // The status fixture's worktree carries the lease once attached, and a
    // worktree without the field reports no occupancy rather than failing.
    var root = try JSONSerialization.jsonObject(with: fixtureData) as! [String: Any]
    var repositories = root["repositories"] as! [[String: Any]]
    var worktrees = repositories[0]["worktrees"] as! [[String: Any]]
    worktrees[0]["occupancy"] = [try JSONSerialization.jsonObject(with: Data(contentsOf: fixtures.appending(path: "occupancy-lease.json")))]
    repositories[0]["worktrees"] = worktrees
    root["repositories"] = repositories
    let occupied = try decoder.decode(StatusSnapshot.self, from: JSONSerialization.data(withJSONObject: root))
    try expect(occupied.repositories[0].worktrees[0].heldOccupancy.map(\.id) == [lease.id], "occupancy did not attach to the worktree")
    let plain = try decoder.decode(StatusSnapshot.self, from: fixtureData)
    try expect(plain.repositories[0].worktrees[0].heldOccupancy.isEmpty, "absent occupancy must decode as empty")

    let upgrade = try decoder.decode(
        ContractErrorEnvelope.self,
        from: Data(contentsOf: fixtures.appending(path: "upgrade-required-error.json"))
    )
    try expect(upgrade.error.code == DaemonClient.upgradeRequiredCode, "upgrade fixture code changed")
    try expect(upgrade.error.currentState == "2" && upgrade.error.requestedState == "1", "upgrade fixture context changed")
    try expect(upgrade.error.nextAction == "upgrade_client" && !upgrade.error.retryable, "upgrade fixture action changed")
}

runner.check("Swift mutation requests encode canonical non-null service arrays") {
    let request = StartEnvironmentRequest(
        requestId: "request_test",
        idempotencyKey: "start:test",
        worktreeId: "worktree_test",
        targetId: "demo",
        confirmedTargetId: "demo",
        serviceIds: ["storefront"]
    )
    let encoded = try JSONEncoder().encode(request)
    let value = try JSONSerialization.jsonObject(with: encoded) as? [String: Any]
    try expect(value?["schemaVersion"] as? Int == contractSchemaVersion, "encoded schema changed")
    try expect(value?["serviceIds"] as? [String] == ["storefront"], "services did not encode as an array")
    try expect(value?["targetId"] as? String == "demo", "target did not encode")
    try expect(value?["confirmedTargetId"] as? String == "demo", "target confirmation did not encode")
    try expect(value?["expectedEnvironmentRevision"] == nil, "nil revision did not stay omitted")
}

runner.check("canonical fixture decodes with expected fields") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    try expect(snapshot.schemaVersion == contractSchemaVersion, "unexpected schema version \(snapshot.schemaVersion)")
    try expect(snapshot.snapshotRevision == 42, "unexpected snapshot revision")
    try expect(snapshot.daemon.state == .ready, "daemon state did not decode")
    try expect(snapshot.repositories.count == 1, "expected one repository")
    let repository = snapshot.repositories[0]
    try expect(repository.profileKey == "sample", "repository profile key did not decode")
    try expect(repository.observation?.stale == false, "repository observation freshness did not decode")
    try expect(repository.runtime?.defaultTargetId == "testing", "repository runtime default did not decode")
    try expect(repository.runtime?.targets.count == 4, "repository targets did not decode")
    try expect(
        repository.runtime?.targets.filter(\.warnOnStart).map(\.id) == ["demo", "production"],
        "warn-on-start target policy did not decode"
    )
    try expect(repository.runtime?.services.count == 8, "repository service catalog did not decode")
    try expect(repository.worktrees.count == 1, "expected one worktree")
    try expect(!repository.worktrees[0].git.isClean, "canonical worktree should report fixture changes")
    try expect(repository.worktrees[0].git.hasTrackedChanges, "canonical tracked-change state did not decode")
    guard let pullRequestObservation = repository.worktrees[0].pullRequest,
          let pullRequest = pullRequestObservation.pullRequest else {
        throw CheckError("canonical pull request observation is missing")
    }
    try expect(pullRequestObservation.status == .found, "pull request availability did not decode")
    try expect(pullRequest.number == 42, "pull request number did not decode")
    try expect(pullRequest.checks.state == .pending, "pull request check state did not decode")
    try expect(pullRequest.checks.items.count == 4, "pull request checks did not decode")
    try expect(pullRequest.reviewDecision == .reviewRequired, "pull request review state did not decode")

    guard let environment = snapshot.environments.first else {
        throw CheckError("canonical environment is missing")
    }
    try expect(environment.displayName == "Demo environment", "canonical environment is missing")
    try expect(environment.targetId == "testing", "environment target did not decode")
    try expect(environment.revision == 17, "environment revision did not decode")
    try expect(environment.health == .degraded, "canonical environment health did not decode")
    try expect(environment.desiredState == .running, "desired state did not decode")
    try expect(environment.observedState == .running, "observed state did not decode")
    try expect(environment.services.count == 2, "expected two services")

    guard let storefront = environment.services.first(where: { $0.id == "storefront" }) else {
        throw CheckError("storefront service is missing")
    }
    try expect(storefront.run?.cpuPercent == 8.2, "storefront run cpu did not decode")
    try expect(storefront.run?.processCount == 7, "storefront run process count did not decode")
    try expect(
        storefront.run?.sourceRevision == "0123456789abcdef0123456789abcdef01234567",
        "storefront source revision did not decode"
    )
    try expect(storefront.run?.sourceHasTrackedChanges == true, "storefront source dirty state did not decode")

    guard let billing = environment.services.first(where: { $0.id == "billing-service" }) else {
        throw CheckError("billing service is missing")
    }
    try expect(billing.observedState == .exited, "billing observed state did not decode")
    try expect(billing.health == .unhealthy, "billing health did not decode")
    try expect(billing.run?.restartCount == 2, "billing restart count did not decode")

    try expect(environment.portLeases.count == 3, "expected three port leases")
    try expect(environment.portLease(withId: "port_storefront_http")?.port == 7005, "storefront port did not decode")
    try expect(environment.portLeases(for: billing).count == 2, "billing port leases did not resolve")
    try expect(environment.infrastructureLeases.first?.ownership == "owned", "infrastructure ownership did not decode")
    try expect(environment.urls.count == 2, "expected two URLs")
    try expect(environment.sortedURLs.first?.service == "billing-service", "URL ordering is not deterministic")
    try expect(environment.resources.memoryBytes == 1_400_000_000, "aggregate memory did not decode")

    guard let worktreeChanges = snapshot.repositories.first?.worktrees.first?.changes else {
        throw CheckError("worktree line changes are missing")
    }
    try expect(worktreeChanges.committed.additions == 2_031, "committed additions did not decode")
    try expect(worktreeChanges.uncommitted.deletions == 3, "uncommitted deletions did not decode")
    try expect(worktreeChanges.service("storefront")?.committed.files == 11, "service attribution did not decode")
    try expect(worktreeChanges.sharedCommitted.additions == 30, "shared changes did not decode")

    try expect(snapshot.operations.first?.state == .succeeded, "operation state did not decode")
    try expect(snapshot.operations.first?.runId == storefront.run?.id, "operation run identity did not decode")
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
    try expect(snapshot.repository(for: environment)?.displayName == "sample", "repository lookup")
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

runner.check("failed ownership recovery states remain explicit and actionable") {
    guard var value = try JSONSerialization.jsonObject(with: fixtureData) as? [String: Any] else {
        throw CheckError("canonical fixture is not a JSON object")
    }
    var environments = value["environments"] as? [[String: Any]] ?? []
    var environment = environments[0]
    environment["desiredState"] = "stopped"
    environment["observedState"] = "failed"
    environment["health"] = "degraded"
    var services = environment["services"] as? [[String: Any]] ?? []
    services[0]["desiredState"] = "stopped"
    services[0]["observedState"] = "unverifiable"
    services[0]["health"] = "degraded"
    services[0]["observationCode"] = "PROCESS_OWNERSHIP_UNVERIFIED"
    environment["services"] = services
    environments[0] = environment
    value["environments"] = environments
    let data = try JSONSerialization.data(withJSONObject: value)
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: data)
    guard let recovered = snapshot.environments.first else {
        throw CheckError("recovery environment is missing")
    }
    try expect(recovered.desiredState == .stopped, "recovery desired state collapsed")
    try expect(recovered.observedState == .failed, "recovery observed state collapsed")
    try expect(recovered.health == .degraded, "recovery health collapsed")
    try expect(recovered.services[0].observedState == .unverifiable, "unverifiable service collapsed")
    try expect(recovered.services[0].observationCode == "PROCESS_OWNERSHIP_UNVERIFIED", "observation code missing")
    try expect(recovered.hasUnverifiableServices, "unverifiable service was not derived")
    try expect(recovered.allowsStopRequest && recovered.allowsRebuildRequest, "recovery actions were unavailable")

    let decoder = ContractDecoder()
    try expect(try decoder.decode(DesiredState.self, from: Data("\"failed\"".utf8)) == .failed, "legacy desired failure collapsed")
    try expect(try decoder.decode(ObservedState.self, from: Data("\"degraded\"".utf8)) == .degraded, "legacy observed degradation collapsed")
    try expect(try decoder.decode(ObservedState.self, from: Data("\"orphaned\"".utf8)) == .orphaned, "orphaned state collapsed")
}

runner.check("additive fields are ignored") {
    let json = """
    {
      "schemaVersion": 2,
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
      "schemaVersion": 2,
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
      "urls": {"storefront": "http://127.0.0.1:7005"},
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
    schemaVersion: contractSchemaVersion,
    transport: "http",
    host: "127.0.0.1",
    port: 49402,
    daemonVersion: "0.1.0-dev",
    instanceId: "daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
    pid: 4242,
    createdAt: Date(timeIntervalSince1970: 1_784_000_000)
)
let sampleTokenRaw = "MDEyMzQ1Njc4OWFiY2RlZjAx" + "MjM0NTY3ODlhYmNkZWY" // gitleaks:allow -- deterministic test-only value

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
        EndpointDescriptor(schemaVersion: contractSchemaVersion + 1, transport: "http", host: "127.0.0.1", port: 1, daemonVersion: "x", instanceId: "y"),
        .unsupportedSchemaVersion(contractSchemaVersion + 1)
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: contractSchemaVersion, transport: "unix", host: "127.0.0.1", port: 1, daemonVersion: "x", instanceId: "y"),
        .unsupportedTransport("unix")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: contractSchemaVersion, transport: "http", host: "192.168.1.5", port: 1, daemonVersion: "x", instanceId: "y"),
        .nonLoopbackHost("192.168.1.5")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: contractSchemaVersion, transport: "http", host: "localhost", port: 1, daemonVersion: "x", instanceId: "y"),
        .nonLoopbackHost("localhost")
    )
    try expectValidationError(
        EndpointDescriptor(schemaVersion: contractSchemaVersion, transport: "http", host: "127.0.0.1", port: 0, daemonVersion: "x", instanceId: "y"),
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
      "schemaVersion": 2,
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
        try expect(error == .missing(missingURL.path), "expected missing, got \(error)")
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

runner.check("Darwin process identity resolves the current process") {
    let pid = Int(ProcessInfo.processInfo.processIdentifier)
    let identity = try DarwinProcessIdentityProvider().processIdentity(forPID: pid)
    try expect(identity.pid == pid, "process identity returned the wrong PID")
    try expect(identity.startedAt < Date(), "process start time must be in the past")
}

runner.check("process identity is verified before token access") {
    let events = LockedRecorder<String>()
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let factory = RuntimeConnectionFactory(
        descriptorProvider: StubDescriptorProvider(descriptor: sampleDescriptor, events: events),
        processIdentityProvider: StubProcessIdentityProvider(
            identity: ProcessIdentity(
                pid: sampleDescriptor.pid,
                startedAt: sampleDescriptor.processStartedAt.addingTimeInterval(1)
            ),
            events: events
        ),
        tokenProvider: StubTokenProvider(token: token, events: events),
        transport: MockTransport { request in
            events.append("transport")
            return (Data(), httpResponse(for: request, status: 200))
        }
    )
    do {
        _ = try factory.connect()
        throw CheckError("mismatched process identity was accepted")
    } catch let error as RuntimeConnectionError {
        guard case .processIdentityMismatch = error else {
            throw CheckError("expected process identity mismatch, got \(error)")
        }
    }
    try expect(events.values == ["descriptor", "identity"], "token or transport was reached: \(events.values)")
}

runner.check("verified runtime connection loads descriptor identity then token") {
    let events = LockedRecorder<String>()
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let factory = RuntimeConnectionFactory(
        descriptorProvider: StubDescriptorProvider(descriptor: sampleDescriptor, events: events),
        processIdentityProvider: StubProcessIdentityProvider(
            identity: ProcessIdentity(pid: sampleDescriptor.pid, startedAt: sampleDescriptor.processStartedAt),
            events: events
        ),
        tokenProvider: StubTokenProvider(token: token, events: events)
    )
    _ = try factory.connect()
    try expect(events.values == ["descriptor", "identity", "token"], "runtime trust order changed: \(events.values)")
}

runner.check("URL session transport rejects redirect responses") {
    let request = URLRequest(url: URL(string: "http://127.0.0.1:49402/handshake")!)
    let redirect = HTTPURLResponse(
        url: request.url!,
        statusCode: 302,
        httpVersion: "HTTP/1.1",
        headerFields: ["Location": "http://127.0.0.1:49403/handshake"]
    )!
    do {
        try URLSessionDaemonTransport.validateNoRedirect(request: request, response: redirect)
        throw CheckError("redirect response was accepted")
    } catch let error as DaemonClientError {
        guard case .redirectRejected = error else {
            throw CheckError("expected redirect rejection, got \(error)")
        }
    }

    let changedURL = HTTPURLResponse(
        url: URL(string: "http://127.0.0.1:49403/handshake")!,
        statusCode: 200,
        httpVersion: "HTTP/1.1",
        headerFields: nil
    )!
    do {
        try URLSessionDaemonTransport.validateNoRedirect(request: request, response: changedURL)
        throw CheckError("changed response URL was accepted")
    } catch let error as DaemonClientError {
        guard case .redirectRejected = error else {
            throw CheckError("expected changed-URL rejection, got \(error)")
        }
    }
}

await runner.checkAsync("LaunchAgent install is exact atomic and secret-free") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-install-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let sourceURL = directory.appending(path: "source/switchyard")
    try fileManager.createDirectory(at: sourceURL.deletingLastPathComponent(), withIntermediateDirectories: true)
    try Data("daemon-binary".utf8).write(to: sourceURL)
    try fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: sourceURL.path)
    let paths = LaunchAgentPaths(
        installedBinaryURL: directory.appending(path: "Application Support/Switchyard/bin/switchyard"),
        commandLinkURL: directory.appending(path: ".local/bin/sy"),
        launchAgentURL: directory.appending(path: "Library/LaunchAgents/com.theronburger.switchyard.daemon.plist"),
        standardOutputURL: directory.appending(path: "Application Support/Switchyard/logs/stdout.log"),
        standardErrorURL: directory.appending(path: "Application Support/Switchyard/logs/stderr.log")
    )
    let launchctlURL = URL(fileURLWithPath: "/bin/launchctl")
    let runner = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        if command.executableURL == launchctlURL, command.arguments.first == "print" {
            return ExactCommandResult(exitCode: 1)
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: runner,
        paths: paths,
        launchctlURL: launchctlURL,
        userID: 501
    )
    let plan = try await manager.makeInstallPlan()
    let plist = try PropertyListSerialization.propertyList(from: plan.propertyList, options: [], format: nil)
    guard let dictionary = plist as? [String: Any] else {
        throw CheckError("LaunchAgent plist is not a dictionary")
    }
    try expect(
        dictionary["ProgramArguments"] as? [String] == [paths.installedBinaryURL.path, "daemon"],
        "LaunchAgent arguments are not exact"
    )
    try expect(
        dictionary["AssociatedBundleIdentifiers"] as? [String] == ["com.theronburger.switchyard"],
        "LaunchAgent is not attributed to the main app bundle"
    )
    try expect(
        dictionary["EnvironmentVariables"] as? [String: String] == ["SWITCHYARD_CHANNEL": "release"],
        "LaunchAgent build channel is missing or unexpected"
    )
    let plistText = String(decoding: plan.propertyList, as: UTF8.self).lowercased()
    try expect(!plistText.contains("token") && !plistText.contains("authorization"), "LaunchAgent plist contains credentials")

    try await manager.install()
    try expect(fileManager.isExecutableFile(atPath: paths.installedBinaryURL.path), "installed daemon is not executable")
    try expect(
        try fileManager.destinationOfSymbolicLink(atPath: paths.commandLinkURL!.path) == paths.installedBinaryURL.path,
        "sy does not target the installed helper"
    )
    try expect(fileManager.fileExists(atPath: paths.launchAgentURL.path), "LaunchAgent plist was not installed")
    try await manager.repair()
    let launchctlCommands = runner.commands.filter { $0.executableURL == launchctlURL }
    try expect(
        launchctlCommands.map(\.arguments) == [
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["bootstrap", "gui/501", paths.launchAgentURL.path],
            ["bootout", "gui/501/com.theronburger.switchyard.daemon"],
            ["bootstrap", "gui/501", paths.launchAgentURL.path],
            ["kickstart", "-k", "gui/501/com.theronburger.switchyard.daemon"],
        ],
        "launchctl argv changed: \(launchctlCommands)"
    )
}

await runner.checkAsync("changed LaunchAgent plist reloads only the owned service") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-plist-update-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let binaryData = Data("same-packaged-daemon".utf8)
    let sourceURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    try writeTestFile(binaryData, to: sourceURL, permissions: 0o700)
    try writeTestFile(binaryData, to: paths.installedBinaryURL, permissions: 0o700)
    try writeTestFile(Data("<?xml version=\"1.0\"?><plist version=\"1.0\"><dict/></plist>".utf8), to: paths.launchAgentURL, permissions: 0o600)
    let launchctlURL = URL(fileURLWithPath: "/bin/launchctl")
    let commands = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: commands,
        paths: paths,
        launchctlURL: launchctlURL,
        userID: 501
    )

    try await manager.install()

    let installedPlist = try Data(contentsOf: paths.launchAgentURL)
    let decoded = try PropertyListSerialization.propertyList(from: installedPlist, options: [], format: nil)
    guard let dictionary = decoded as? [String: Any] else {
        throw CheckError("installed LaunchAgent plist is not a dictionary")
    }
    try expect(
        dictionary["AssociatedBundleIdentifiers"] as? [String] == ["com.theronburger.switchyard"],
        "installed LaunchAgent lacks app attribution"
    )
    let launchctlCommands = commands.commands.filter { $0.executableURL == launchctlURL }
    try expect(
        launchctlCommands.map(\.arguments) == [
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["bootout", "gui/501/com.theronburger.switchyard.daemon"],
            ["bootstrap", "gui/501", paths.launchAgentURL.path],
        ],
        "plist update did not reload only the owned service: \(launchctlCommands)"
    )
}

await runner.checkAsync("unchanged LaunchAgent install does not rewrite owned files") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-unchanged-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let binaryData = Data("same-packaged-daemon".utf8)
    let sourceURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    try writeTestFile(binaryData, to: sourceURL, permissions: 0o700)
    try writeTestFile(binaryData, to: paths.installedBinaryURL, permissions: 0o700)
    try fileManager.createDirectory(at: paths.commandLinkURL!.deletingLastPathComponent(), withIntermediateDirectories: true)
    try fileManager.createSymbolicLink(atPath: paths.commandLinkURL!.path, withDestinationPath: paths.installedBinaryURL.path)
    let launchctlURL = URL(fileURLWithPath: "/bin/launchctl")
    let commands = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: commands,
        paths: paths,
        launchctlURL: launchctlURL,
        userID: 501
    )
    let plan = try await manager.makeInstallPlan()
    try writeTestFile(plan.propertyList, to: paths.launchAgentURL, permissions: 0o600)
    let binaryAttributesBefore = try fileManager.attributesOfItem(atPath: paths.installedBinaryURL.path)
    let plistAttributesBefore = try fileManager.attributesOfItem(atPath: paths.launchAgentURL.path)

    try expect(try await manager.inspect() == .enabled, "matching helper was not enabled")
    try expect(try await manager.inspect() == .enabled, "cached matching helper changed status")
    try await manager.install()

    let binaryAttributesAfter = try fileManager.attributesOfItem(atPath: paths.installedBinaryURL.path)
    let plistAttributesAfter = try fileManager.attributesOfItem(atPath: paths.launchAgentURL.path)
    try expect(
        binaryAttributesBefore[.systemFileNumber] as? NSNumber == binaryAttributesAfter[.systemFileNumber] as? NSNumber,
        "unchanged helper inode was replaced"
    )
    try expect(
        binaryAttributesBefore[.modificationDate] as? Date == binaryAttributesAfter[.modificationDate] as? Date,
        "unchanged helper modification date changed"
    )
    try expect(
        plistAttributesBefore[.systemFileNumber] as? NSNumber == plistAttributesAfter[.systemFileNumber] as? NSNumber,
        "unchanged plist inode was replaced"
    )
    let launchctlCommands = commands.commands.filter { $0.executableURL == launchctlURL }
    try expect(
        launchctlCommands.map(\.arguments) == [
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["kickstart", "-k", "gui/501/com.theronburger.switchyard.daemon"],
        ],
        "unchanged install launchctl argv changed"
    )
}

await runner.checkAsync("changed packaged helper is detected atomically replaced and kickstarted") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-update-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let sourceURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    let packagedData = Data("new-daemon-with-same-semantic-version".utf8)
    try writeTestFile(packagedData, to: sourceURL, permissions: 0o700)
    try writeTestFile(Data("old-daemon-with-same-version".utf8), to: paths.installedBinaryURL, permissions: 0o700)
    let installedAttributesBefore = try fileManager.attributesOfItem(atPath: paths.installedBinaryURL.path)
    let launchctlURL = URL(fileURLWithPath: "/bin/launchctl")
    let commands = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: commands,
        paths: paths,
        launchctlURL: launchctlURL,
        userID: 501
    )
    let plan = try await manager.makeInstallPlan()
    try writeTestFile(plan.propertyList, to: paths.launchAgentURL, permissions: 0o600)

    try expect(try await manager.inspect() == .outdated, "changed helper was not marked outdated")
    try expect(commands.commands.isEmpty, "outdated detection invoked launchctl before reinstall")
    try await manager.install()

    let installedAttributesAfter = try fileManager.attributesOfItem(atPath: paths.installedBinaryURL.path)
    try expect(try Data(contentsOf: paths.installedBinaryURL) == packagedData, "packaged helper was not installed")
    try expect(
        installedAttributesBefore[.systemFileNumber] as? NSNumber != installedAttributesAfter[.systemFileNumber] as? NSNumber,
        "helper replacement was not atomic"
    )
    let launchctlCommands = commands.commands.filter { $0.executableURL == launchctlURL }
    try expect(
        launchctlCommands.map(\.arguments) == [
            ["print", "gui/501/com.theronburger.switchyard.daemon"],
            ["kickstart", "-k", "gui/501/com.theronburger.switchyard.daemon"],
        ],
        "updated helper launchctl argv changed: \(launchctlCommands)"
    )
}

await runner.checkAsync("unsafe helper symlink is refused without mutation") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-symlink-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let realSourceURL = directory.appending(path: "bundle/real-daemon")
    let symlinkURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    let installedData = Data("installed-daemon".utf8)
    try writeTestFile(Data("packaged-daemon".utf8), to: realSourceURL, permissions: 0o700)
    try fileManager.createSymbolicLink(at: symlinkURL, withDestinationURL: realSourceURL)
    try writeTestFile(installedData, to: paths.installedBinaryURL, permissions: 0o700)
    try fileManager.createDirectory(at: paths.commandLinkURL!.deletingLastPathComponent(), withIntermediateDirectories: true)
    try fileManager.createSymbolicLink(atPath: paths.commandLinkURL!.path, withDestinationPath: paths.installedBinaryURL.path)
    let commands = RecordingExactRunner { _ in ExactCommandResult(exitCode: 0) }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: symlinkURL)),
        commandRunner: commands,
        paths: paths,
        userID: 501
    )
    let plan = try await manager.makeInstallPlan()
    try writeTestFile(plan.propertyList, to: paths.launchAgentURL, permissions: 0o600)

    do {
        _ = try await manager.inspect()
        throw CheckError("symlinked packaged helper was accepted")
    } catch let error as DaemonServiceError {
        try expect(error == .binaryInvalid, "unexpected symlink error: \(error)")
    }
    try expect(try Data(contentsOf: paths.installedBinaryURL) == installedData, "symlink refusal mutated installed helper")
    try expect(commands.commands.isEmpty, "symlink refusal invoked a command")
}

await runner.checkAsync("foreign sy command is refused without mutation") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-command-conflict-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let sourceURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    try writeTestFile(Data("packaged-daemon".utf8), to: sourceURL, permissions: 0o700)
    let foreign = Data("#!/bin/sh\necho foreign\n".utf8)
    try writeTestFile(foreign, to: paths.commandLinkURL!, permissions: 0o700)
    let commands = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: commands,
        paths: paths,
        userID: getuid()
    )
    do {
        try await manager.install()
        throw CheckError("foreign sy command was replaced")
    } catch let error as DaemonServiceError {
        try expect(error == .commandLinkConflict, "unexpected sy conflict error: \(error)")
    }
    try expect(try Data(contentsOf: paths.commandLinkURL!) == foreign, "foreign sy command bytes changed")
}

await runner.checkAsync("outdated plist is replaced without carrying credentials") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-plist-check-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let sourceURL = directory.appending(path: "bundle/SwitchyardDaemon")
    let paths = testLaunchAgentPaths(in: directory)
    let binaryData = Data("matching-daemon".utf8)
    try writeTestFile(binaryData, to: sourceURL, permissions: 0o700)
    try writeTestFile(binaryData, to: paths.installedBinaryURL, permissions: 0o700)
    let launchctlURL = URL(fileURLWithPath: "/bin/launchctl")
    let commands = RecordingExactRunner { command in
        if command.arguments == ["version"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: Data("{\"schemaVersion\":\(contractSchemaVersion),\"version\":\"0.1.0-dev\"}".utf8)
            )
        }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = LaunchAgentServiceManager(
        binaryProvider: StubBinaryProvider(binary: DaemonBinary(sourceURL: sourceURL)),
        commandRunner: commands,
        paths: paths,
        launchctlURL: launchctlURL,
        userID: 501
    )
    let plan = try await manager.makeInstallPlan()
    var stalePlist = try PropertyListSerialization.propertyList(
        from: plan.propertyList,
        options: [],
        format: nil
    ) as! [String: Any]
    stalePlist["EnvironmentVariables"] = ["SWITCHYARD_TOKEN": sampleTokenRaw]
    let staleData = try PropertyListSerialization.data(
        fromPropertyList: stalePlist,
        format: .xml,
        options: 0
    )
    try writeTestFile(staleData, to: paths.launchAgentURL, permissions: 0o600)

    try expect(try await manager.inspect() == .outdated, "noncanonical plist was not marked outdated")
    try await manager.install()
    let repairedPlist = try Data(contentsOf: paths.launchAgentURL)
    let repairedText = String(decoding: repairedPlist, as: UTF8.self)
    try expect(!repairedText.contains(sampleTokenRaw), "credential survived plist replacement")
    try expect(!repairedText.contains("SWITCHYARD_TOKEN"), "credential key survived plist replacement")
}

// MARK: - Agent connections

await runner.checkAsync("agent connection inspection is read-only and does not launch Switchyard") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-agent-inspect-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let codexExecutable = directory.appending(path: "bin/codex")
    try writeTestFile(Data("codex".utf8), to: codexExecutable, permissions: 0o700)
    let paths = try agentTestPaths(in: directory, codexExecutableURL: codexExecutable)
    try writeTestFile(Data("model = \"local\"\n".utf8), to: paths.codexConfigURL, permissions: 0o600)
    let claude: [String: Any] = [
        "foreignSetting": true,
        "mcpServers": [
            "switchyard": [
                "type": "stdio",
                "command": paths.switchyardExecutableURL.path,
                "args": ["mcp"],
                "env": [String: String](),
            ],
        ],
    ]
    try writeTestFile(
        try JSONSerialization.data(withJSONObject: claude),
        to: paths.claudeConfigURL,
        permissions: 0o600
    )
    let commands = RecordingExactRunner { command in
        try expect(command.executableURL == codexExecutable, "inspection launched an unexpected executable")
        try expect(command.arguments == ["mcp", "list", "--json"], "inspection argv changed")
        return ExactCommandResult(
            exitCode: 0,
            standardOutput: try codexServerList(command: paths.switchyardExecutableURL.path)
        )
    }
    let report = await AgentConnectionManager(paths: paths, commandRunner: commands).inspect()
    try expect(report.status(for: .codex)?.state == .connected, "Codex connection was not recognized")
    try expect(report.status(for: .claude)?.state == .connected, "Claude connection was not recognized")
    try expect(commands.commands.allSatisfy { $0.executableURL != paths.switchyardExecutableURL }, "inspection launched Switchyard MCP")
}

await runner.checkAsync("Codex repair uses exact argv once and is idempotent") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-codex-repair-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let codexExecutable = directory.appending(path: "bin/codex")
    try writeTestFile(Data("codex".utf8), to: codexExecutable, permissions: 0o700)
    let paths = try agentTestPaths(in: directory, codexExecutableURL: codexExecutable)
    let foreignConfig = Data("model = \"foreign\"\n".utf8)
    try writeTestFile(foreignConfig, to: paths.codexConfigURL, permissions: 0o600)
    let installed = LockedBox(false)
    let commands = RecordingExactRunner { command in
        if command.arguments == ["mcp", "list", "--json"] {
            return ExactCommandResult(
                exitCode: 0,
                standardOutput: installed.read()
                    ? try codexServerList(command: paths.switchyardExecutableURL.path)
                    : Data("[]".utf8)
            )
        }
        let expected = [
            "mcp", "add", "switchyard", "--",
            paths.switchyardExecutableURL.path, "mcp",
        ]
        try expect(command.arguments == expected, "Codex repair argv changed: \(command.arguments)")
        installed.update { $0 = true }
        return ExactCommandResult(exitCode: 0)
    }
    let manager = AgentConnectionManager(paths: paths, commandRunner: commands)
    let first = await manager.repair(.codex)
    try expect(first.status(for: .codex)?.state == .connected, "Codex repair did not connect")
    _ = await manager.repair(.codex)
    let addCommands = commands.commands.filter { $0.arguments.prefix(2) == ["mcp", "add"] }
    try expect(addCommands.count == 1, "idempotent Codex repair added the server \(addCommands.count) times")
    try expect(addCommands[0].arguments.allSatisfy { !$0.localizedCaseInsensitiveContains("token") }, "repair argv contains token material")
    try expect(commands.commands.allSatisfy { $0.executableURL != paths.switchyardExecutableURL }, "repair launched Switchyard MCP")
    try expect(try Data(contentsOf: paths.codexConfigURL) == foreignConfig, "stubbed Codex repair rewrote foreign config")
}

await runner.checkAsync("Claude repair is atomic, owner-only, foreign-preserving, and idempotent") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-claude-repair-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let claudeExecutable = directory.appending(path: "bin/claude")
    try writeTestFile(Data("claude".utf8), to: claudeExecutable, permissions: 0o700)
    let paths = try agentTestPaths(
        in: directory,
        codexExecutableURL: nil,
        claudeExecutableURL: claudeExecutable
    )
    let original: [String: Any] = [
        "foreignSetting": ["nested": true, "value": "preserve-me"],
        "mcpServers": [
            "foreign": ["type": "http", "url": "https://example.invalid/mcp"],
            "switchyard": [
                "type": "stdio",
                "command": "/old/switchyard",
                "args": ["mcp"],
                "env": ["SWITCHYARD_DAEMON_TOKEN": "remove-me"],
            ],
        ],
    ]
    try writeTestFile(
        try JSONSerialization.data(withJSONObject: original),
        to: paths.claudeConfigURL,
        permissions: 0o600
    )
    let commands = RecordingExactRunner { command in
        try expect(command.executableURL == claudeExecutable, "Claude repair launched an unexpected executable")
        try expect(
            command.environmentOverrides == ["CLAUDE_CONFIG_DIR": paths.claudeConfigDirectoryURL.path],
            "Claude personal profile override changed"
        )
        var root = try JSONSerialization.jsonObject(with: Data(contentsOf: paths.claudeConfigURL)) as! [String: Any]
        var servers = root["mcpServers"] as! [String: Any]
        if command.arguments == ["mcp", "remove", "switchyard", "--scope", "user"] {
            servers.removeValue(forKey: "switchyard")
        } else {
            let expected = [
                "mcp", "add", "--scope", "user", "switchyard", "--",
                paths.switchyardExecutableURL.path, "mcp",
            ]
            try expect(command.arguments == expected, "Claude repair argv changed: \(command.arguments)")
            servers["switchyard"] = [
                "type": "stdio",
                "command": paths.switchyardExecutableURL.path,
                "args": ["mcp"],
                "env": [String: String](),
            ]
        }
        root["mcpServers"] = servers
        try writeTestFile(
            try JSONSerialization.data(withJSONObject: root),
            to: paths.claudeConfigURL,
            permissions: 0o600
        )
        return ExactCommandResult(exitCode: 0)
    }
    let manager = AgentConnectionManager(paths: paths, commandRunner: commands)
    let repaired = await manager.repair(.claude)
    try expect(repaired.status(for: .claude)?.state == .connected, "Claude repair did not connect")
    let firstRepair = try Data(contentsOf: paths.claudeConfigURL)
    let root = try JSONSerialization.jsonObject(with: firstRepair) as? [String: Any]
    let foreign = (root?["mcpServers"] as? [String: Any])?["foreign"] as? [String: Any]
    let switchyard = (root?["mcpServers"] as? [String: Any])?["switchyard"] as? [String: Any]
    try expect((root?["foreignSetting"] as? [String: Any])?["value"] as? String == "preserve-me", "foreign root config changed")
    try expect(foreign?["url"] as? String == "https://example.invalid/mcp", "foreign MCP config changed")
    try expect(switchyard?["command"] as? String == paths.switchyardExecutableURL.path, "Claude command is not exact")
    try expect(switchyard?["args"] as? [String] == ["mcp"], "Claude args are not exact")
    try expect((switchyard?["env"] as? [String: Any])?.isEmpty == true, "Claude entry retained token material")
    let attributes = try fileManager.attributesOfItem(atPath: paths.claudeConfigURL.path)
    try expect(attributes[.posixPermissions] as? Int == 0o600, "Claude config is not owner-only")
    _ = await manager.repair(.claude)
    try expect(try Data(contentsOf: paths.claudeConfigURL) == firstRepair, "idempotent Claude repair rewrote the file")
    try expect(commands.commands.count == 2, "idempotent Claude repair reran host mutation commands")
    try expect(commands.commands.allSatisfy { $0.executableURL != paths.switchyardExecutableURL }, "Claude repair launched Switchyard MCP")
    try expect(
        commands.commands.flatMap(\.arguments).allSatisfy { !$0.localizedCaseInsensitiveContains("token") },
        "Claude repair argv contains token material"
    )
}

await runner.checkAsync("missing Claude connection installs through the personal CLI without health launch") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-claude-install-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let claudeExecutable = directory.appending(path: "bin/claude")
    try writeTestFile(Data("claude".utf8), to: claudeExecutable, permissions: 0o700)
    let paths = try agentTestPaths(
        in: directory,
        codexExecutableURL: nil,
        claudeExecutableURL: claudeExecutable
    )
    let commands = RecordingExactRunner { command in
        let expected = [
            "mcp", "add", "--scope", "user", "switchyard", "--",
            paths.switchyardExecutableURL.path, "mcp",
        ]
        try expect(command.arguments == expected, "Claude install argv changed")
        let root: [String: Any] = [
            "mcpServers": [
                "switchyard": [
                    "type": "stdio",
                    "command": paths.switchyardExecutableURL.path,
                    "args": ["mcp"],
                    "env": [String: String](),
                ],
            ],
        ]
        try writeTestFile(
            try JSONSerialization.data(withJSONObject: root),
            to: paths.claudeConfigURL,
            permissions: 0o600
        )
        return ExactCommandResult(exitCode: 0)
    }
    let report = await AgentConnectionManager(paths: paths, commandRunner: commands).repair(.claude)
    try expect(report.status(for: .claude)?.state == .connected, "missing Claude connection did not install")
    try expect(commands.commands.count == 1, "Claude install ran more than one mutation command")
    try expect(commands.commands[0].executableURL == claudeExecutable, "Claude install used the wrong executable")
    try expect(!commands.commands[0].arguments.contains(where: { $0.localizedCaseInsensitiveContains("token") }), "Claude install stored token material")
}

await runner.checkAsync("unsupported or symlinked Claude config is refused byte-for-byte") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-claude-refusal-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    var paths = try agentTestPaths(in: directory, codexExecutableURL: nil)
    let unsupported = Data("{\"mcpServers\":[\"unknown-shape\"],\"foreign\":true}\n".utf8)
    try writeTestFile(unsupported, to: paths.claudeConfigURL, permissions: 0o600)
    var report = await AgentConnectionManager(paths: paths).repair(.claude)
    try expect(report.status(for: .claude)?.state == .refused, "unsupported Claude config was not refused")
    try expect(try Data(contentsOf: paths.claudeConfigURL) == unsupported, "refusal changed unknown config bytes")

    let target = directory.appending(path: "claude/target.json")
    let targetData = Data("{\"foreign\":true}\n".utf8)
    try writeTestFile(targetData, to: target, permissions: 0o600)
    try fileManager.removeItem(at: paths.claudeConfigURL)
    try fileManager.createSymbolicLink(at: paths.claudeConfigURL, withDestinationURL: target)
    paths = AgentConnectionPaths(
        switchyardExecutableURL: paths.switchyardExecutableURL,
        codexExecutableURL: nil,
        codexConfigURL: paths.codexConfigURL,
        claudeConfigURL: paths.claudeConfigURL
    )
    report = await AgentConnectionManager(paths: paths).repair(.claude)
    try expect(report.status(for: .claude)?.state == .refused, "symlinked Claude config was not refused")
    try expect(try Data(contentsOf: target) == targetData, "symlink refusal changed its target")
}

await runner.checkAsync("failed Claude replacement restores the exact original bytes") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-claude-rollback-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let claudeExecutable = directory.appending(path: "bin/claude")
    try writeTestFile(Data("claude".utf8), to: claudeExecutable, permissions: 0o700)
    let paths = try agentTestPaths(
        in: directory,
        codexExecutableURL: nil,
        claudeExecutableURL: claudeExecutable
    )
    let original = Data("{\"mcpServers\":{\"foreign\":{\"type\":\"http\",\"url\":\"https://example.invalid\"},\"switchyard\":{\"type\":\"stdio\",\"command\":\"/old\",\"args\":[\"mcp\"]}},\"foreign\":true}\n".utf8)
    try writeTestFile(original, to: paths.claudeConfigURL, permissions: 0o600)
    let commands = RecordingExactRunner { command in
        if command.arguments.contains("remove") {
            let removed = Data("{\"mcpServers\":{\"foreign\":{\"type\":\"http\",\"url\":\"https://example.invalid\"}},\"foreign\":true}\n".utf8)
            try writeTestFile(removed, to: paths.claudeConfigURL, permissions: 0o600)
            return ExactCommandResult(exitCode: 0)
        }
        return ExactCommandResult(exitCode: 1)
    }
    let report = await AgentConnectionManager(paths: paths, commandRunner: commands).repair(.claude)
    try expect(report.status(for: .claude)?.state == .refused, "failed Claude add was not surfaced as a refusal")
    try expect(try Data(contentsOf: paths.claudeConfigURL) == original, "failed Claude repair did not restore exact bytes")
    try expect(commands.commands.allSatisfy { $0.executableURL != paths.switchyardExecutableURL }, "rollback path launched Switchyard MCP")
}

await runner.checkAsync("invalid Codex inspection refuses without touching foreign config") {
    let fileManager = FileManager.default
    let directory = fileManager.temporaryDirectory.appending(path: "switchyard-codex-refusal-\(UUID().uuidString)")
    try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
    defer { try? fileManager.removeItem(at: directory) }

    let codexExecutable = directory.appending(path: "bin/codex")
    try writeTestFile(Data("codex".utf8), to: codexExecutable, permissions: 0o700)
    let paths = try agentTestPaths(in: directory, codexExecutableURL: codexExecutable)
    let original = Data("foreign = \"byte-for-byte\"\n".utf8)
    try writeTestFile(original, to: paths.codexConfigURL, permissions: 0o600)
    let commands = RecordingExactRunner { command in
        try expect(command.arguments == ["mcp", "list", "--json"], "refusal attempted mutation")
        return ExactCommandResult(exitCode: 0, standardOutput: Data("not-json".utf8))
    }
    let report = await AgentConnectionManager(paths: paths, commandRunner: commands).repair(.codex)
    try expect(report.status(for: .codex)?.state == .refused, "invalid Codex config was not refused")
    try expect(try Data(contentsOf: paths.codexConfigURL) == original, "Codex refusal changed foreign config bytes")
    try expect(commands.commands.allSatisfy { !$0.arguments.contains("add") }, "Codex refusal attempted repair")
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

await runner.checkAsync("daemon client submits authenticated environment mutations") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let requestId = "request_app_start"
    let receipt = """
    {"schemaVersion":2,"requestId":"\(requestId)","operationId":"operation_app_start",
     "acceptedAt":"2026-08-14T10:00:00Z","environmentId":"environment_app_start"}
    """
    let transport = MockTransport { request in
        try expect(request.httpMethod == "POST", "mutation did not use POST")
        try expect(request.url?.path() == "/v1/environments", "unexpected mutation path")
        try expect(request.url?.host() == "127.0.0.1", "mutation left loopback")
        try expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer \(sampleTokenRaw)", "mutation is unauthenticated")
        try expect(request.value(forHTTPHeaderField: "Content-Type") == "application/json", "mutation content type changed")
        try expect(request.value(forHTTPHeaderField: "Origin") == nil, "mutation unexpectedly sent an Origin")
        try expect(request.value(forHTTPHeaderField: "X-Switchyard-Request-Id") == requestId, "request identity header changed")
        try expect(request.url?.absoluteString.contains(sampleTokenRaw) == false, "token leaked into mutation URL")
        let body = try JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any]
        try expect(body?["worktreeId"] as? String == "worktree_app", "worktree was not encoded")
        try expect(body?["targetId"] as? String == "production", "target was not encoded")
        try expect(body?["confirmedTargetId"] as? String == "production", "target confirmation was not encoded")
        try expect(body?["serviceIds"] as? [String] == ["storefront", "billing-service"], "services were not encoded")
        return (Data(receipt.utf8), httpResponse(for: request, status: 202))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    let result = try await client.startEnvironment(StartEnvironmentRequest(
        requestId: requestId,
        idempotencyKey: "start_app_start",
        worktreeId: "worktree_app",
        targetId: "production",
        confirmedTargetId: "production",
        serviceIds: ["storefront", "billing-service"]
    ))
    try expect(result.operationId == "operation_app_start", "mutation receipt did not decode")
}

await runner.checkAsync("daemon client encodes revisioned stops and rejects hostile paths before transport") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let sentPaths = LockedRecorder<String>()
    let requestId = "request_app_stop"
    let transport = MockTransport { request in
        sentPaths.append(request.url?.path() ?? "")
        let body = try JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any]
        try expect(body?["expectedEnvironmentRevision"] as? Int == 17, "stop revision was not encoded")
        let receipt = """
        {"schemaVersion":2,"requestId":"\(requestId)","operationId":"operation_app_stop",
         "acceptedAt":"2026-08-14T10:00:00Z","environmentId":"environment_app"}
        """
        return (Data(receipt.utf8), httpResponse(for: request, status: 202))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    let request = StopEnvironmentRequest(
        requestId: requestId,
        idempotencyKey: "stop_app",
        expectedEnvironmentRevision: 17
    )
    _ = try await client.stopEnvironment(id: "environment_app", request: request)
    try expect(sentPaths.values == ["/v1/environments/environment_app/stop"], "stop path changed")
    do {
        _ = try await client.stopEnvironment(id: "../../foreign", request: request)
        throw CheckError("hostile environment path was accepted")
    } catch let error as DaemonClientError {
        guard case .invalidRequest = error else {
            throw CheckError("unexpected hostile path error: \(error)")
        }
    }
    try expect(sentPaths.values.count == 1, "hostile path reached the transport")
}

await runner.checkAsync("daemon client submits managed worktree mutations and rejects hostile paths") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let paths = LockedRecorder<String>()
    let transport = MockTransport { request in
        paths.append(request.url?.path() ?? "")
        let body = try JSONSerialization.jsonObject(with: request.httpBody ?? Data()) as? [String: Any]
        guard let requestId = body?["requestId"] as? String else {
            throw CheckError("workspace request identity missing")
        }
        if request.url?.path() == "/v1/worktrees" {
            try expect(body?["repositoryId"] as? String == "repository_app", "repository was not encoded")
            try expect(body?["branch"] as? String == "feature/go-service", "branch was not encoded")
            try expect(body?["startPoint"] as? String == "origin/main", "base was not encoded")
        } else {
            try expect(body?["worktreeId"] as? String == "worktree_app", "worktree was not encoded")
        }
        let receipt = """
        {"schemaVersion":2,"requestId":"\(requestId)","operationId":"operation_workspace",
         "acceptedAt":"2026-08-17T16:00:00Z"}
        """
        return (Data(receipt.utf8), httpResponse(for: request, status: 202))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    _ = try await client.createWorktree(CreateWorktreeRequest(
        requestId: "request_create",
        idempotencyKey: "create:key",
        repositoryId: "repository_app",
        branch: "feature/go-service",
        startPoint: "origin/main"
    ))
    _ = try await client.adoptWorktree(AdoptWorktreeRequest(
        requestId: "request_adopt",
        idempotencyKey: "adopt:key",
        worktreeId: "worktree_app"
    ))
    _ = try await client.archiveWorktree(ArchiveWorktreeRequest(
        requestId: "request_archive",
        idempotencyKey: "archive:key",
        worktreeId: "worktree_app"
    ))
    do {
        _ = try await client.archiveWorktree(ArchiveWorktreeRequest(
            requestId: "request_hostile",
            idempotencyKey: "archive:hostile",
            worktreeId: "../foreign"
        ))
        throw CheckError("hostile worktree path was accepted")
    } catch let error as DaemonClientError {
        guard case .invalidRequest = error else {
            throw CheckError("unexpected hostile workspace path error: \(error)")
        }
    }
    try expect(
        paths.values == ["/v1/worktrees", "/v1/worktrees/worktree_app/adopt", "/v1/worktrees/worktree_app/archive"],
        "workspace paths changed"
    )
}

await runner.checkAsync("live workspace actions handshake before mutation") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let paths = LockedRecorder<String>()
    let requestId = "request_verified_workspace"
    let handshake = """
    {"schemaVersion":2,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
     "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[2]}
    """
    let receipt = """
    {"schemaVersion":2,"requestId":"\(requestId)","operationId":"operation_verified_workspace",
     "acceptedAt":"2026-08-17T16:00:00Z"}
    """
    let client = try DaemonClient(
        descriptor: sampleDescriptor,
        token: token,
        transport: MockTransport { request in
            paths.append(request.url?.path() ?? "")
            let body = request.url?.path() == "/handshake" ? Data(handshake.utf8) : Data(receipt.utf8)
            return (body, httpResponse(for: request, status: request.url?.path() == "/handshake" ? 200 : 202))
        }
    )
    let actions = LiveWorkspaceActionClient(
        connectionFactory: StubConnectionFactory([.success(DaemonConnection(descriptor: sampleDescriptor, client: client))])
    )
    _ = try await actions.adoptWorktree(AdoptWorktreeRequest(
        requestId: requestId,
        idempotencyKey: "adopt:verified",
        worktreeId: "worktree_app"
    ))
    try expect(paths.values == ["/handshake", "/v1/worktrees/worktree_app/adopt"], "workspace mutation did not verify the daemon first")
}

await runner.checkAsync("live environment actions handshake before mutation") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let paths = LockedRecorder<String>()
    let requestId = "request_verified_start"
    let handshake = """
    {"schemaVersion":2,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
     "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[2]}
    """
    let receipt = """
    {"schemaVersion":2,"requestId":"\(requestId)","operationId":"operation_verified_start",
     "acceptedAt":"2026-08-14T10:00:00Z","environmentId":"environment_verified"}
    """
    let client = try DaemonClient(
        descriptor: sampleDescriptor,
        token: token,
        transport: MockTransport { request in
            paths.append(request.url?.path() ?? "")
            let body = request.url?.path() == "/handshake" ? Data(handshake.utf8) : Data(receipt.utf8)
            return (body, httpResponse(for: request, status: request.url?.path() == "/handshake" ? 200 : 202))
        }
    )
    let actions = LiveEnvironmentActionClient(
        connectionFactory: StubConnectionFactory([.success(DaemonConnection(descriptor: sampleDescriptor, client: client))])
    )
    _ = try await actions.startEnvironment(StartEnvironmentRequest(
        requestId: requestId,
        idempotencyKey: "start_verified",
        worktreeId: "worktree_verified",
        serviceIds: ["storefront"]
    ))
    try expect(paths.values == ["/handshake", "/v1/environments"], "mutation did not verify the live daemon first")
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

await runner.checkAsync("daemon client declares its exact schema version on every request") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let declared = LockedRecorder<String>()
    let handshake = """
    {"schemaVersion":2,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
     "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[2]}
    """
    let receipt = """
    {"schemaVersion":2,"requestId":"request_declared","operationId":"operation_declared",
     "acceptedAt":"2026-08-14T10:00:00Z"}
    """
    let transport = MockTransport { request in
        declared.append(request.value(forHTTPHeaderField: DaemonClient.schemaVersionHeader) ?? "")
        let isHandshake = request.url?.path() == "/handshake"
        return (Data((isHandshake ? handshake : receipt).utf8), httpResponse(for: request, status: isHandshake ? 200 : 202))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    _ = try await client.handshake()
    _ = try await client.prepareWorktree(PrepareWorktreeRequest(
        requestId: "request_declared", idempotencyKey: "prepare:declared", worktreeId: "worktree_app"
    ))
    try expect(declared.values == ["2", "2"], "GET and POST must both declare schema version 2, got \(declared.values)")
}

await runner.checkAsync("daemon client maps HTTP 426 to upgradeRequired even without a readable envelope") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let envelope = """
    {"schemaVersion":2,"error":{"code":"UPGRADE_REQUIRED","message":"This client's contract schema version is not supported by the daemon.","retryable":false,"currentState":"3","requestedState":"2","nextAction":"upgrade_client"}}
    """
    for body in [envelope, "not json", "{\"error\":{\"code\":\"OTHER\",\"message\":\"x\",\"retryable\":false}}"] {
        let transport = MockTransport { request in
            (Data(body.utf8), httpResponse(for: request, status: 426))
        }
        let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
        do {
            _ = try await client.status()
            throw CheckError("expected upgradeRequired for 426")
        } catch let error as DaemonClientError {
            guard case .upgradeRequired(let message) = error else {
                throw CheckError("expected upgradeRequired for 426, got \(error)")
            }
            if body == envelope {
                try expect(message == "This client's contract schema version is not supported by the daemon.", "daemon message did not survive")
            }
        }
    }
}

await runner.checkAsync("handshake schema mismatches are upgrade problems, not malformed responses") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let cases: [(String, Bool)] = [
        ("""
        {"schemaVersion":3,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
         "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[3]}
        """, true),
        ("""
        {"schemaVersion":2,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
         "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[1]}
        """, true),
        ("""
        {"schemaVersion":0,"daemonInstanceId":"daemon_01J5EYX37NFK6E7K5M0RMWN9G8",
         "daemonVersion":"0.1.0-dev","supportedSchemaVersions":[2]}
        """, false),
    ]
    for (body, upgrade) in cases {
        let transport = MockTransport { request in
            (Data(body.utf8), httpResponse(for: request, status: 200))
        }
        let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
        do {
            _ = try await client.handshake()
            throw CheckError("expected handshake failure")
        } catch let error as DaemonClientError {
            switch (error, upgrade) {
            case (.upgradeRequired, true), (.malformedResponse, false):
                break
            default:
                throw CheckError("unexpected handshake mapping \(error) for upgrade=\(upgrade)")
            }
        }
    }
}

runner.check("a descriptor from another contract generation routes to upgradeRequired") {
    let error = RuntimeConnectionError.descriptor(.unsupportedSchemaVersion(1))
    try expect(error.requiresUpgrade, "unsupported schema descriptor must require an upgrade")
    try expect(!RuntimeConnectionError.descriptor(.malformed("x")).requiresUpgrade, "malformed descriptors are not upgrade problems")
    try expect(!error.retryableWhileDaemonStarts, "an upgrade requirement must not be retried as a startup race")

    var machine = DaemonLifecycleMachine(state: .locatingEndpoint)
    try machine.handle(.endpointUpgradeRequired(message: "update"))
    try expect(machine.state == .upgradeRequired(message: "update"), "descriptor mismatch should reach upgradeRequired")
    try expect(machine.state.needsUserAction && machine.state.canRepair, "upgradeRequired needs the user and offers repair")
}

await runner.checkAsync("daemon client acquires and releases occupancy through exact routes and refuses unsafe holders") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let paths = LockedRecorder<String>()
    let transport = MockTransport { request in
        paths.append("\(request.httpMethod ?? "") \(request.url?.path() ?? "")")
        let body: String
        if request.url?.path().hasSuffix("/release") == true {
            body = """
            {"id":"occupancy_01","worktreeId":"worktree_app","holderKind":"agent-task","holderLabel":"Codex task",
             "state":"released","acquiredAt":"2026-08-21T09:05:00Z","releasedAt":"2026-08-21T09:45:00Z"}
            """
        } else {
            body = """
            {"id":"occupancy_01","worktreeId":"worktree_app","holderKind":"agent-task","holderLabel":"Codex task",
             "state":"held","acquiredAt":"2026-08-21T09:05:00Z"}
            """
        }
        return (Data(body.utf8), httpResponse(for: request, status: 200))
    }
    let client = try DaemonClient(descriptor: sampleDescriptor, token: token, transport: transport)
    let lease = try await client.acquireOccupancy(AcquireOccupancyRequest(
        requestId: "request_occupancy", worktreeId: "worktree_app", holderKind: "agent-task", holderLabel: "Codex task"
    ))
    try expect(lease.state == .held, "acquired lease must be held")
    let released = try await client.releaseOccupancy(ReleaseOccupancyRequest(
        requestId: "request_release", worktreeId: "worktree_app", leaseId: lease.id
    ))
    try expect(released.state == .released, "released lease must be released")
    try expect(
        paths.values == ["POST /v1/worktrees/worktree_app/occupancy", "POST /v1/worktrees/worktree_app/occupancy/occupancy_01/release"],
        "occupancy routes changed: \(paths.values)"
    )

    for (kind, label) in [("Codex Desktop", "Codex task"), ("agent-", "Codex task"), ("agent-task", "/Users/someone"), ("agent-task", "")] {
        do {
            _ = try await client.acquireOccupancy(AcquireOccupancyRequest(
                requestId: "request_bad", worktreeId: "worktree_app", holderKind: kind, holderLabel: label
            ))
            throw CheckError("unsafe holder \(kind)/\(label) reached the daemon")
        } catch let error as DaemonClientError {
            guard case .invalidRequest = error else { throw CheckError("expected invalidRequest, got \(error)") }
        }
    }
    try expect(paths.values.count == 2, "unsafe occupancy requests must not be sent")
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

    var outdated = DaemonLifecycleMachine()
    try outdated.handle(.begin)
    try outdated.handle(.registrationChecked(.outdated))
    try expect(outdated.state == .registrationRequired, "outdated helper should enter the install path")
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

await runner.checkAsync("lifecycle controller waits boundedly then performs live handshake and status") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let handshakeBody = """
    {"schemaVersion": 2, "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "daemonVersion": "0.1.0-dev",
     "supportedSchemaVersions":[2]}
    """
    let client = try DaemonClient(
        descriptor: sampleDescriptor,
        token: token,
        transport: MockTransport { request in
            let body = request.url?.path == "/v1/status" ? fixtureData : Data(handshakeBody.utf8)
            return (body, httpResponse(for: request, status: 200))
        }
    )
    let connection = DaemonConnection(descriptor: sampleDescriptor, client: client)
    let missing = RuntimeConnectionError.descriptor(.missing("/private/runtime.json"))
    let factory = StubConnectionFactory([.failure(missing), .failure(missing), .success(connection)])
    let service = StubServiceManager(status: .enabled)
    let sleepCalls = LockedRecorder<Duration>()
    let healthyReport = DoctorReport(checks: [
        DoctorCheck(id: "live", title: "Live", outcome: .passed("healthy")),
    ])
    let controller = DaemonLifecycleController(
        serviceManager: service,
        connectionFactory: factory,
        doctor: StubDoctor(report: healthyReport),
        sleeper: StubSleeper(calls: sleepCalls),
        waitPolicy: EndpointWaitPolicy(delays: [.milliseconds(1), .milliseconds(2)])
    )
    let result = await controller.refresh()
    try expect(result.state.isOperational, "controller did not reach ready: \(result.state)")
    try expect(result.snapshot?.snapshotRevision == 42, "controller did not fetch live status")
    try expect(sleepCalls.values == [.milliseconds(1), .milliseconds(2)], "endpoint backoff changed")
    let serviceCalls = await service.calls
    try expect(serviceCalls == ["inspect", "kickstart"], "unexpected lifecycle service calls: \(serviceCalls)")
}

await runner.checkAsync("lifecycle controller refuses an invalid runtime without starting it") {
    let factory = StubConnectionFactory([.failure(.processIdentityMismatch)])
    let service = StubServiceManager(status: .enabled)
    let controller = DaemonLifecycleController(
        serviceManager: service,
        connectionFactory: factory,
        doctor: StubDoctor(report: DoctorReport(checks: [])),
        sleeper: StubSleeper(calls: LockedRecorder<Duration>()),
        waitPolicy: EndpointWaitPolicy(delays: [.milliseconds(1)])
    )
    let result = await controller.refresh()
    guard case .unreachable = result.state else {
        throw CheckError("invalid runtime should be unreachable, got \(result.state)")
    }
    let serviceCalls = await service.calls
    try expect(serviceCalls == ["inspect"], "invalid runtime must not be kickstarted: \(serviceCalls)")
}

await runner.checkAsync("lifecycle refresh automatically reinstalls an outdated helper") {
    let token = try BearerToken(rawValue: sampleTokenRaw)
    let handshakeBody = """
    {"schemaVersion": 2, "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "daemonVersion": "0.1.0-dev",
     "supportedSchemaVersions":[2]}
    """
    let connection = DaemonConnection(
        descriptor: sampleDescriptor,
        client: try DaemonClient(
            descriptor: sampleDescriptor,
            token: token,
            transport: MockTransport { request in
                let body = request.url?.path == "/v1/status" ? fixtureData : Data(handshakeBody.utf8)
                return (body, httpResponse(for: request, status: 200))
            }
        )
    )
    let service = StubServiceManager(status: .outdated)
    let sleeps = LockedRecorder<Duration>()
    let controller = DaemonLifecycleController(
        serviceManager: service,
        connectionFactory: StubConnectionFactory([.failure(.processIdentityMismatch), .success(connection)]),
        doctor: StubDoctor(report: DoctorReport(checks: [])),
        sleeper: StubSleeper(calls: sleeps),
        waitPolicy: EndpointWaitPolicy(delays: [.milliseconds(1), .milliseconds(2)])
    )
    let result = await controller.refresh()
    try expect(result.state.isOperational, "updated lifecycle did not reconnect")
    let calls = await service.calls
    try expect(calls == ["inspect", "install", "inspect"], "outdated refresh did not reinstall exactly once: \(calls)")
    try expect(sleeps.values == [.milliseconds(1), .milliseconds(2)], "restart did not retry stale process identity safely")
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
    let serviceManager = StubServiceManager(status: .enabled)
    let doctor = LiveConnectionDoctor(
        serviceManager: serviceManager,
        connectionFactory: RuntimeConnectionFactory(
            location: location,
            transport: MockTransport { request in (Data(), httpResponse(for: request, status: 200)) }
        )
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
    {"schemaVersion": 2, "endpoint": "http://127.0.0.1:49402",
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
    {"schemaVersion": 2, "daemonInstanceId": "daemon_01J5EYX37NFK6E7K5M0RMWN9G8", "daemonVersion": "0.1.0-dev",
     "supportedSchemaVersions":[2]}
    """
    let serviceManager = StubServiceManager(status: .enabled)
    let doctor = LiveConnectionDoctor(
        serviceManager: serviceManager,
        connectionFactory: RuntimeConnectionFactory(
            location: location,
            processIdentityProvider: StubProcessIdentityProvider(
                identity: ProcessIdentity(
                    pid: 4242,
                    startedAt: ISO8601DateFormatter().date(from: "2026-08-14T09:00:00Z")!
                ),
                events: nil
            ),
            transport: MockTransport { request in
                let body = request.url?.path == "/v1/status" ? fixtureData : Data(handshakeBody.utf8)
                return (body, httpResponse(for: request, status: 200))
            }
        )
    )
    let report = await doctor.run()
    try expect(report.isHealthy, "doctor should be healthy: \(report.checks.map { "\($0.id)=\($0.outcome)" })")
    let handshakeCheck = report.checks.first { $0.id == "handshake" }
    guard case .passed = handshakeCheck?.outcome else {
        throw CheckError("handshake should pass against the mock daemon")
    }
}

// MARK: - AppModel scenarios

runner.check("app launch defaults live and fixtures require an explicit switch") {
    try expect(AppLaunchConfiguration.resolve(arguments: ["Switchyard"], environment: [:]) == .live, "default launch is not live")
    try expect(
        AppLaunchConfiguration.resolve(arguments: ["Switchyard", "--fixture", "empty"], environment: [:]) == .fixture(.empty),
        "fixture flag was not honored"
    )
    try expect(
        AppLaunchConfiguration.resolve(arguments: ["Switchyard"], environment: ["SWITCHYARD_FIXTURE": "failure"]) == .fixture(.failure),
        "fixture environment switch was not honored"
    )
}

runner.check("development channel is isolated from release paths and identity") {
    try expect(
        SwitchyardChannel.resolve(
            infoDictionary: ["SwitchyardChannel": "development"],
            environment: ["SWITCHYARD_CHANNEL": "release"]
        ) == .development,
        "a packaged development app could be redirected to the release channel"
    )
    try expect(
        SwitchyardChannel.resolve(infoDictionary: nil, environment: ["SWITCHYARD_CHANNEL": "release"]) == .release,
        "an unpackaged release-channel override was ignored"
    )
    let developmentPaths = LaunchAgentPaths.standard(channel: .development)
    let releasePaths = LaunchAgentPaths.standard(channel: .release)
    try expect(developmentPaths.installedBinaryURL != releasePaths.installedBinaryURL, "helper paths overlap")
    try expect(developmentPaths.launchAgentURL != releasePaths.launchAgentURL, "LaunchAgent paths overlap")

    let developmentLocation = DaemonEndpointLocation.standard(channel: .development)
    let releaseLocation = DaemonEndpointLocation.standard(channel: .release)
    try expect(developmentLocation != releaseLocation, "runtime connection paths overlap")

    let plan = try LaunchAgentPlanBuilder.make(
        binary: DaemonBinary(sourceURL: URL(fileURLWithPath: "/tmp/SwitchyardDevelopmentDaemon")),
        paths: developmentPaths,
        userID: 501,
        channel: .development
    )
    let plist = try PropertyListSerialization.propertyList(from: plan.propertyList, options: [], format: nil)
    guard let dictionary = plist as? [String: Any] else {
        throw CheckError("development LaunchAgent plist is not a dictionary")
    }
    try expect(
        dictionary["Label"] as? String == SwitchyardChannel.development.launchAgentLabel,
        "development LaunchAgent uses the release label"
    )
    try expect(
        dictionary["AssociatedBundleIdentifiers"] as? [String] == [SwitchyardChannel.development.appBundleIdentifier],
        "development LaunchAgent is attributed to the release app"
    )
}

await runner.checkAsync("live app model maps controller status and repair state") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    let session = DaemonSession(
        instanceId: snapshot.daemon.instanceId,
        daemonVersion: snapshot.daemon.version,
        endpoint: sampleDescriptor
    )
    let report = DoctorReport(checks: [DoctorCheck(id: "live", title: "Live", outcome: .passed("healthy"))])
    let controller = StubLifecycleController(
        refreshResult: DaemonLifecycleResult(state: .ready(session), snapshot: snapshot, doctorReport: report),
        repairResult: DaemonLifecycleResult(
            state: .unreachable(reason: "repair failed"),
            snapshot: nil,
            doctorReport: DoctorReport(checks: [DoctorCheck(id: "live", title: "Live", outcome: .failed("repair failed"))])
        )
    )
    let agentConnections = StubAgentConnections(report: AgentConnectionReport(statuses: AgentHost.allCases.map {
        AgentConnectionStatus(host: $0, state: .missing, detail: "missing")
    }))
    let model = AppModel(
        liveController: controller,
        agentConnections: agentConnections,
        pollingInterval: .seconds(60)
    )
    try expect(!model.isFixtureMode, "injected live model became fixture mode")
    await model.refresh()
    try expect(model.phase == .loaded, "live snapshot did not map to loaded")
    try expect(model.lifecycleState.isOperational, "live state did not map to ready")
    try expect(model.snapshot?.snapshotRevision == 42, "live snapshot was not retained")
    try expect(model.agentConnectionReport?.statuses.count == 2, "agent connection status was not exposed")
    await model.repairAll()
    guard case .failed(let message) = model.phase else {
        throw CheckError("failed repair did not map to disconnected state")
    }
    try expect(message == "repair failed", "failed repair message changed: \(message)")
    try expect(await agentConnections.repairedAll == 1, "Repair All did not repair agent hosts")
}

await runner.checkAsync("live app model keeps start and stop transitions scoped until completion") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    let session = DaemonSession(
        instanceId: snapshot.daemon.instanceId,
        daemonVersion: snapshot.daemon.version,
        endpoint: sampleDescriptor
    )
    let result = DaemonLifecycleResult(
        state: .ready(session),
        snapshot: snapshot,
        doctorReport: DoctorReport(checks: [])
    )
    let receipt = try ContractDecoder().decode(
        MutationReceipt.self,
        from: Data(contentsOf: fixtureURL.deletingLastPathComponent().appending(path: "mutation-receipt.json"))
    )
    let actions = StubEnvironmentActions(receipt: receipt)
    let agentConnections = StubAgentConnections(report: AgentConnectionReport(statuses: AgentHost.allCases.map {
        AgentConnectionStatus(host: $0, state: .connected, detail: "connected")
    }))
    let model = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        environmentActions: actions,
        agentConnections: agentConnections,
        pollingInterval: .seconds(60)
    )
    await model.refresh()
    await model.startEnvironment(
        worktreeId: snapshot.repositories[0].worktrees[0].id,
        targetId: "demo",
        confirmedTargetId: "demo",
        serviceIds: ["storefront", "billing-service"]
    )
    guard case .accepted(let start) = model.environmentActionState else {
        throw CheckError("accepted start receipt was not exposed")
    }
    try expect(start.kind == .start && start.receipt.operationId == receipt.operationId, "start receipt changed")
    try expect(start.stage == .starting, "accepted start did not remain transitional")
    try expect(start.worktreeId == snapshot.repositories[0].worktrees[0].id, "start transition leaked worktree identity")
    try expect(!model.canSubmitEnvironmentAction, "accepted operation incorrectly enabled another mutation")
    let starts = await actions.starts
    try expect(starts.count == 1, "start action was not submitted once")
    try expect(starts[0].targetId == "demo", "selected target changed")
    try expect(starts[0].confirmedTargetId == "demo", "selected target confirmation changed")
    try expect(starts[0].serviceIds == ["storefront", "billing-service"], "selected services changed")

    guard let environment = snapshot.environments.first else { throw CheckError("fixture environment missing") }
    let stopModel = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        environmentActions: actions,
        agentConnections: agentConnections,
        pollingInterval: .seconds(60)
    )
    await stopModel.refresh()
    await stopModel.stopEnvironment(environment)
    guard case .accepted(let stop) = stopModel.environmentActionState else {
        throw CheckError("accepted stop receipt was not exposed")
    }
    try expect(stop.kind == .stop, "stop receipt kind changed")
    try expect(stop.stage == .stopping, "accepted stop did not remain transitional")
    try expect(stopModel.environmentTransition(forWorktreeId: environment.worktreeId) == .stopping, "stop transition was not scoped to its worktree")
    try expect(model.environmentTransition(forWorktreeId: "foreign_worktree") == nil, "transition leaked to another worktree")
    let stops = await actions.stops
    try expect(stops.count == 1, "stop action was not submitted once")
    try expect(stops[0].0 == environment.id, "stop environment identity changed")
    try expect(stops[0].1.expectedEnvironmentRevision == environment.revision, "stop revision changed")
}

await runner.checkAsync("live app model rejects an old healthy run as start completion") {
    let receipt = try ContractDecoder().decode(
        MutationReceipt.self,
        from: Data(contentsOf: fixtureURL.deletingLastPathComponent().appending(path: "mutation-receipt.json"))
    )
    guard var value = try JSONSerialization.jsonObject(with: fixtureData) as? [String: Any],
          let expectedRunId = receipt.runId else {
        throw CheckError("canonical run receipt is unavailable")
    }
    let oldRunId = "run_old_healthy"
    var operations = value["operations"] as? [[String: Any]] ?? []
    operations[0]["id"] = receipt.operationId
    operations[0]["runId"] = oldRunId
    value["operations"] = operations
    var environments = value["environments"] as? [[String: Any]] ?? []
    var environment = environments[0]
    var services = environment["services"] as? [[String: Any]] ?? []
    for index in services.indices {
        var run = services[index]["run"] as? [String: Any] ?? [:]
        run["id"] = oldRunId
        services[index]["run"] = run
    }
    environment["services"] = services
    environments[0] = environment
    value["environments"] = environments
    let snapshot = try ContractDecoder().decode(
        StatusSnapshot.self,
        from: JSONSerialization.data(withJSONObject: value)
    )
    let session = DaemonSession(
        instanceId: snapshot.daemon.instanceId,
        daemonVersion: snapshot.daemon.version,
        endpoint: sampleDescriptor
    )
    let result = DaemonLifecycleResult(
        state: .ready(session), snapshot: snapshot, doctorReport: DoctorReport(checks: [])
    )
    let actions = StubEnvironmentActions(receipt: receipt)
    let model = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        environmentActions: actions,
        pollingInterval: .seconds(60)
    )
    await model.refresh()
    await model.startEnvironment(
        worktreeId: snapshot.repositories[0].worktrees[0].id,
        serviceIds: ["storefront", "billing-service"]
    )
    for _ in 0..<50 {
        if case .failed = model.environmentActionState { break }
        try await Task.sleep(for: .milliseconds(10))
    }
    guard case .failed(.start, let message) = model.environmentActionState else {
        throw CheckError("old run \(oldRunId) was accepted as new run \(expectedRunId)")
    }
    try expect(message.contains("did not publish"), "run identity mismatch was not explained")
}

await runner.checkAsync("live app model submits managed worktree actions with transitional state") {
    let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: fixtureData)
    let session = DaemonSession(
        instanceId: snapshot.daemon.instanceId,
        daemonVersion: snapshot.daemon.version,
        endpoint: sampleDescriptor
    )
    let result = DaemonLifecycleResult(
        state: .ready(session),
        snapshot: snapshot,
        doctorReport: DoctorReport(checks: [])
    )
    let receipt = try ContractDecoder().decode(
        MutationReceipt.self,
        from: Data(contentsOf: fixtureURL.deletingLastPathComponent().appending(path: "mutation-receipt.json"))
    )
    let actions = StubWorkspaceActions(receipt: receipt)
    let model = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        workspaceActions: actions,
        pollingInterval: .seconds(60)
    )
    await model.refresh()
    await model.createWorktree(repositoryId: "repository_app", branch: "feature/go-service", startPoint: "origin/main")
    guard case .accepted(.create, let createReceipt) = model.workspaceActionState else {
        throw CheckError("accepted workspace create was not exposed")
    }
    try expect(createReceipt.operationId == receipt.operationId, "workspace receipt changed")
    try expect(!model.canSubmitWorkspaceAction, "active workspace operation allowed another mutation")
    let creates = await actions.creates
    try expect(creates.count == 1 && creates[0].branch == "feature/go-service", "workspace create request changed")

    let adoptedWorktree = snapshot.repositories[0].worktrees[0]
    let adoptActions = StubWorkspaceActions(receipt: receipt)
    let adoptModel = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        workspaceActions: adoptActions,
        pollingInterval: .seconds(60)
    )
    await adoptModel.refresh()
    await adoptModel.adoptWorktree(adoptedWorktree)
    guard case .accepted(.adopt, _) = adoptModel.workspaceActionState else {
        throw CheckError("accepted workspace adoption was not exposed")
    }
    let adopts = await adoptActions.adopts
    try expect(adopts.count == 1 && adopts[0].worktreeId == adoptedWorktree.id, "workspace adoption request changed")

    let archiveActions = StubWorkspaceActions(receipt: receipt)
    let archiveModel = AppModel(
        liveController: StubLifecycleController(refreshResult: result, repairResult: result),
        workspaceActions: archiveActions,
        pollingInterval: .seconds(60)
    )
    await archiveModel.refresh()
    let archivedWorktree = snapshot.repositories[0].worktrees[0]
    await archiveModel.archiveWorktree(archivedWorktree)
    guard case .accepted(.archive, _) = archiveModel.workspaceActionState else {
        throw CheckError("accepted workspace archive was not exposed")
    }
    let archives = await archiveActions.archives
    try expect(archives.count == 1 && archives[0].worktreeId == archivedWorktree.id, "workspace archive request changed")
}

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
