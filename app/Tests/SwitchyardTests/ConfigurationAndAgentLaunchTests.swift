import Foundation
import Testing
@testable import SwitchyardApp
@testable import SwitchyardKit

// MARK: - Repository draft and YAML entry

struct RepositoryConfigurationDraftTests {
    @Test
    func `draft suggests a stable key and managed root outside the checkout`() {
        let draft = RepositoryConfigurationDraft.suggested(forRootPath: "/Users/example/Developer/My Repo_2/")
        #expect(draft.key == "my-repo-2")
        #expect(draft.displayName == "My Repo_2")
        #expect(draft.rootPath == "/Users/example/Developer/My Repo_2")
        #expect(draft.managedWorktreesRoot == "/Users/example/Developer/My Repo_2-worktrees")
        #expect(draft.remote == "origin")
        #expect(draft.defaultBase == "origin/main")
    }

    @Test
    func `draft validation rejects every unsafe input and duplicate keys`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }

        var draft = RepositoryConfigurationDraft.suggested(forRootPath: root.path)
        #expect(draft.problems().isEmpty)
        #expect(draft.problems(existingKeys: [draft.key]) == [.keyAlreadyConfigured])

        draft.key = "Bad Key"
        draft.displayName = "  "
        draft.remote = "ori gin"
        draft.defaultBase = "-bad"
        draft.managedWorktreesRoot = root.path + "/inside"
        let problems = draft.problems()
        #expect(problems.contains(.keyInvalid))
        #expect(problems.contains(.displayNameMissing))
        #expect(problems.contains(.remoteInvalid))
        #expect(problems.contains(.defaultBaseInvalid))
        #expect(problems.contains(.managedRootInsideRoot))

        draft.rootPath = "relative/path"
        #expect(draft.problems().contains(.rootNotAbsolute))
        draft.rootPath = root.path + "/missing"
        #expect(draft.problems().contains(.rootIsNotDirectory))
        draft.managedWorktreesRoot = ""
        #expect(draft.problems().contains(.managedRootNotAbsolute))
    }

    @Test
    func `yaml entry quotes every scalar and carries only generic profile sections`() {
        let draft = RepositoryConfigurationDraft(
            key: "aurora-console",
            displayName: "Aurora \"Console\"",
            rootPath: "/Users/example/Developer/aurora console",
            remote: "origin",
            defaultBase: "origin/main",
            managedWorktreesRoot: "/Users/example/Developer/aurora-console-worktrees",
            enabled: false
        )
        let snippet = draft.yamlSnippet
        #expect(snippet.hasPrefix("  aurora-console:\n    enabled: false\n"))
        #expect(snippet.contains("displayName: \"Aurora \\\"Console\\\"\""))
        #expect(snippet.contains("root: \"/Users/example/Developer/aurora console\""))
        #expect(snippet.contains("managedWorktreesRoot: \"/Users/example/Developer/aurora-console-worktrees\""))
        for section in ["values", "toolchains", "caches", "environmentSources", "preparation", "targets", "services", "infrastructure", "artifacts", "actions", "cleanup"] {
            #expect(snippet.contains("\(section): {}"), "missing \(section)")
        }
        #expect(snippet.contains("defaultTarget: \"\""))
        #expect(!snippet.contains("\t"))
    }
}

// MARK: - Acceptance presentation

struct ConfigurationAcceptancePresentationTests {
    private func status(_ json: String) throws -> ConfigurationStatus {
        try ContractDecoder().decode(ConfigurationStatus.self, from: Data(json.utf8))
    }

    @Test
    func `pending candidate distinguishes changed repositories from new ones`() throws {
        let presentation = ConfigurationAcceptancePresentation(status: try status(FixtureConfigurationActionClient.canonicalPendingJSON))
        #expect(presentation.stateLabel == "Pending acceptance")
        #expect(presentation.expectedRevision == 4)
        #expect(presentation.canAccept)
        #expect(presentation.repositoryState(profileKey: "sample", isPublished: true) == .pendingChange)
        #expect(presentation.repositoryState(profileKey: "second-sample", isPublished: false) == .pendingAddition)
        #expect(presentation.repositoryState(profileKey: "untouched", isPublished: true) == .accepted)
        #expect(presentation.summary.contains("2 repository entries"))
        #expect(presentation.summary.contains("1 executable fingerprints"))
        let candidate = try #require(presentation.status.candidate)
        #expect(presentation.changedRepositoryKeys(candidate) == ["sample", "second-sample"])
    }

    @Test
    func `accepted and missing states resolve without a candidate`() throws {
        let accepted = ConfigurationAcceptancePresentation(status: try status(FixtureConfigurationActionClient.canonicalAcceptedJSON))
        #expect(accepted.status.acceptedRevision == 5)
        #expect(!accepted.canAccept)
        #expect(accepted.repositoryState(profileKey: "sample", isPublished: true) == .accepted)
        #expect(accepted.summary == "Revision 5 is accepted and authorizes every compiled command.")

        let missing = ConfigurationAcceptancePresentation(status: try status(FixtureConfigurationActionClient.missingJSON))
        #expect(missing.expectedRevision == 0)
        #expect(!missing.canAccept)
        #expect(missing.repositoryState(profileKey: "sample", isPublished: false) == .pendingAddition)
        #expect(missing.repositoryState(profileKey: "sample", isPublished: true) == .unknown)
    }
}

// MARK: - Daemon client contract

private final class RecordingTransport: DaemonTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var _requests: [URLRequest] = []
    let responder: @Sendable (URLRequest) -> (Int, String)

    init(responder: @escaping @Sendable (URLRequest) -> (Int, String)) {
        self.responder = responder
    }

    var requests: [URLRequest] {
        lock.withLock { _requests }
    }

    private func record(_ request: URLRequest) {
        lock.withLock { _requests.append(request) }
    }

    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        record(request)
        let (status, body) = responder(request)
        let response = HTTPURLResponse(
            url: request.url!, statusCode: status, httpVersion: "HTTP/1.1",
            headerFields: ["Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"]
        )!
        return (Data(body.utf8), response)
    }
}

struct ConfigurationDaemonClientTests {
    private static let descriptor = EndpointDescriptor(
        schemaVersion: contractSchemaVersion, transport: "http", host: "127.0.0.1", port: 49402,
        daemonVersion: "0.1.0-dev", instanceId: "daemon_test", pid: 4242,
        createdAt: Date(timeIntervalSince1970: 1_784_000_000)
    )
    private static let tokenRaw = "MDEyMzQ1Njc4OWFiY2RlZjAx" + "MjM0NTY3ODlhYmNkZWY" // gitleaks:allow -- deterministic test-only value
    private static let digest = "sha256:" + String(repeating: "ab", count: 32)

    private func client(_ transport: RecordingTransport) throws -> DaemonClient {
        try DaemonClient(descriptor: Self.descriptor, token: try BearerToken(rawValue: Self.tokenRaw), transport: transport)
    }

    @Test
    func `configuration read validate and accept use exact paths bodies and status codes`() async throws {
        let transport = RecordingTransport { request in
            switch request.url?.path {
            case "/v1/configuration": return (200, FixtureConfigurationActionClient.canonicalPendingJSON)
            case "/v1/configuration/validate": return (200, FixtureConfigurationActionClient.canonicalPendingJSON)
            case "/v1/configuration/accept": return (200, FixtureConfigurationActionClient.canonicalAcceptedJSON)
            default: return (404, "{}")
            }
        }
        let client = try client(transport)

        let status = try await client.configuration()
        #expect(status.state == .pending)
        #expect(status.candidate?.repositoryDigests.count == 2)

        let validated = try await client.validateConfiguration(ConfigurationValidationRequest(expectedRevision: 4))
        #expect(validated.acceptedRevision == 4)

        let pendingDigest = try #require(status.candidate?.digest)
        let accepted = try await client.acceptConfiguration(ConfigurationAcceptanceRequest(expectedRevision: 4, digest: pendingDigest))
        #expect(accepted.state == .accepted)
        #expect(accepted.acceptedRevision == 5)
        #expect(accepted.acceptedDigest == pendingDigest)

        let requests = transport.requests
        #expect(requests.map(\.httpMethod) == ["GET", "POST", "POST"])
        #expect(requests.map { $0.url?.path } == ["/v1/configuration", "/v1/configuration/validate", "/v1/configuration/accept"])
        let validateBody = try #require(requests[1].httpBody.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] })
        #expect(validateBody["schemaVersion"] as? Int == contractSchemaVersion)
        #expect(validateBody["expectedRevision"] as? Int == 4)
        let acceptBody = try #require(requests[2].httpBody.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] })
        #expect(acceptBody["expectedRevision"] as? Int == 4)
        #expect(acceptBody["digest"] as? String == pendingDigest)
        for request in requests {
            #expect(request.value(forHTTPHeaderField: "Authorization")?.hasPrefix("Bearer ") == true)
            #expect(request.url?.query == nil)
        }
    }

    @Test
    func `repository mutation posts the exact CAS body and refuses malformed requests locally`() async throws {
        let transport = RecordingTransport { request in
            switch request.url?.path {
            case "/v1/configuration/repositories": return (200, FixtureConfigurationActionClient.canonicalPendingJSON)
            default: return (404, "{}")
            }
        }
        let client = try client(transport)
        let entry = ConfigurationRepositoryEntry(
            key: "aurora-console", enabled: true, displayName: "Aurora", root: "/Users/example/Developer/aurora",
            remote: "origin", defaultBase: "origin/main", managedWorktreesRoot: "/Users/example/Developer/aurora-worktrees"
        )
        let status = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
            expectedRevision: 4, expectedSourceDigest: Self.digest, operation: .upsert, key: "aurora-console", entry: entry
        ))
        #expect(status.desired?.repositories.map(\.key) == ["sample", "second-sample"])
        #expect(status.desired?.sourceDigest == "sha256:" + String(repeating: "3", count: 64))
        let request = try #require(transport.requests.first)
        #expect(request.httpMethod == "POST")
        let requestBody = try #require(request.httpBody)
        let body = try #require(JSONSerialization.jsonObject(with: requestBody) as? [String: Any])
        #expect(body["expectedRevision"] as? Int == 4)
        #expect(body["expectedSourceDigest"] as? String == Self.digest)
        #expect(body["operation"] as? String == "upsert")
        #expect(body["key"] as? String == "aurora-console")
        #expect((body["entry"] as? [String: Any])?["managedWorktreesRoot"] as? String == "/Users/example/Developer/aurora-worktrees")

        // An absent desired file is signalled by omitting the digest entirely.
        _ = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
            expectedRevision: 0, expectedSourceDigest: "", operation: .upsert, key: "aurora-console", entry: entry
        ))
        let freshBody = try #require(transport.requests.last?.httpBody)
        let fresh = try #require(JSONSerialization.jsonObject(with: freshBody) as? [String: Any])
        #expect(fresh["expectedSourceDigest"] == nil)

        for request in [
            ConfigurationRepositoryMutationRequest(expectedRevision: 4, expectedSourceDigest: "abc", operation: .remove, key: "sample", entry: nil),
            ConfigurationRepositoryMutationRequest(expectedRevision: 4, expectedSourceDigest: nil, operation: .remove, key: "Sample", entry: nil),
            ConfigurationRepositoryMutationRequest(expectedRevision: 4, expectedSourceDigest: nil, operation: .remove, key: "sample", entry: entry),
            ConfigurationRepositoryMutationRequest(expectedRevision: 4, expectedSourceDigest: nil, operation: .upsert, key: "sample", entry: nil),
            ConfigurationRepositoryMutationRequest(expectedRevision: 4, expectedSourceDigest: nil, operation: .upsert, key: "other", entry: entry),
            ConfigurationRepositoryMutationRequest(expectedRevision: -1, expectedSourceDigest: nil, operation: .remove, key: "sample", entry: nil),
        ] {
            await #expect(throws: DaemonClientError.self) {
                _ = try await client.mutateRepositoryConfiguration(request)
            }
        }
        #expect(transport.requests.count == 2)
    }

    @Test
    func `client refuses a desired view that contradicts itself`() async throws {
        let transport = RecordingTransport { _ in
            (200, """
            {"schemaVersion":2,"state":"missing","acceptedRevision":0,
             "desired":{"present":false,"sourceDigest":"sha256:\(String(repeating: "1", count: 64))","repositories":[]}}
            """)
        }
        await #expect(throws: DaemonClientError.self) {
            _ = try await client(transport).configuration()
        }
    }

    @Test
    func `client refuses malformed digests and malformed configuration status`() async throws {
        let transport = RecordingTransport { _ in
            (200, """
            {"schemaVersion":2,"state":"pending","acceptedRevision":1,"acceptedDigest":"\(Self.digest)"}
            """)
        }
        let client = try client(transport)
        await #expect(throws: DaemonClientError.self) {
            _ = try await client.acceptConfiguration(ConfigurationAcceptanceRequest(expectedRevision: 1, digest: "sha256:short"))
        }
        await #expect(throws: DaemonClientError.self) {
            _ = try await client.acceptConfiguration(ConfigurationAcceptanceRequest(expectedRevision: -1, digest: Self.digest))
        }
        #expect(transport.requests.isEmpty)
        await #expect(throws: DaemonClientError.self) {
            _ = try await client.configuration()
        }
        #expect(DaemonClient.validDigest(Self.digest))
        #expect(!DaemonClient.validDigest("sha256:" + String(repeating: "G", count: 64)))
        #expect(!DaemonClient.validDigest("md5:" + String(repeating: "a", count: 64)))
    }

    @Test
    func `configuration conflicts surface the daemon's stable error code`() async throws {
        let transport = RecordingTransport { _ in
            (409, """
            {"schemaVersion":2,"error":{"code":"CONFIGURATION_REVISION_CONFLICT","message":"Configuration changed before this request completed","retryable":true}}
            """)
        }
        let client = try client(transport)
        do {
            _ = try await client.validateConfiguration(ConfigurationValidationRequest(expectedRevision: 3))
            Issue.record("expected a conflict")
        } catch DaemonClientError.contract(let error) {
            #expect(error.code == "CONFIGURATION_REVISION_CONFLICT")
            #expect(error.retryable)
        }
    }

    @Test
    func `prepare worktree posts to the exact worktree path`() async throws {
        let transport = RecordingTransport { request in
            (202, """
            {"schemaVersion":2,"requestId":"\(request.value(forHTTPHeaderField: "X-Switchyard-Request-Id") ?? "")","operationId":"operation_prepare","acceptedAt":"2026-08-14T09:00:00Z"}
            """)
        }
        let client = try client(transport)
        let receipt = try await client.prepareWorktree(PrepareWorktreeRequest(
            requestId: "app_prepare", idempotencyKey: "workspace_prepare_1", worktreeId: "worktree_01"
        ))
        #expect(receipt.operationId == "operation_prepare")
        #expect(transport.requests.first?.url?.path == "/v1/worktrees/worktree_01/prepare")
        await #expect(throws: DaemonClientError.self) {
            _ = try await client.prepareWorktree(PrepareWorktreeRequest(
                requestId: "app_prepare", idempotencyKey: "workspace_prepare_2", worktreeId: "bad/id"
            ))
        }
    }
}

// MARK: - App model flows

private actor RecordingConfigurationActions: ConfigurationActionSubmitting {
    var current: ConfigurationStatus
    private(set) var validations: [ConfigurationValidationRequest] = []
    private(set) var acceptances: [ConfigurationAcceptanceRequest] = []
    var failNextAccept = false

    init(current: ConfigurationStatus) {
        self.current = current
    }

    func configuration() async throws -> ConfigurationStatus { current }

    func validateConfiguration(_ request: ConfigurationValidationRequest) async throws -> ConfigurationStatus {
        validations.append(request)
        current = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(FixtureConfigurationActionClient.canonicalPendingJSON.utf8))
        return current
    }

    func acceptConfiguration(_ request: ConfigurationAcceptanceRequest) async throws -> ConfigurationStatus {
        acceptances.append(request)
        if failNextAccept { throw DaemonClientError.contract(ContractError(
            code: "CONFIGURATION_REVISION_CONFLICT", message: "changed", retryable: true,
            resourceKind: nil, resourceId: nil, currentState: nil, requestedState: nil,
            phase: nil, step: nil, diagnostic: nil, logReference: nil, nextAction: nil, exitCode: nil
        )) }
        current = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(FixtureConfigurationActionClient.canonicalAcceptedJSON.utf8))
        return current
    }

    func setFailNextAccept(_ value: Bool) { failNextAccept = value }

    private(set) var mutations: [ConfigurationRepositoryMutationRequest] = []
    var failNextMutation: ContractError?

    func setFailNextMutation(_ error: ContractError?) { failNextMutation = error }

    func mutateRepositoryConfiguration(_ request: ConfigurationRepositoryMutationRequest) async throws -> ConfigurationStatus {
        mutations.append(request)
        if let failure = failNextMutation {
            failNextMutation = nil
            throw DaemonClientError.contract(failure)
        }
        current = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(FixtureConfigurationActionClient.canonicalPendingJSON.utf8))
        return current
    }
}

private func contractError(_ code: String, _ message: String) -> ContractError {
    ContractError(
        code: code, message: message, retryable: false,
        resourceKind: nil, resourceId: nil, currentState: nil, requestedState: nil,
        phase: nil, step: nil, diagnostic: nil, logReference: nil, nextAction: nil, exitCode: nil
    )
}

private struct StubLifecycleController: DaemonLifecycleControlling {
    let result: DaemonLifecycleResult
    func refresh() async -> DaemonLifecycleResult { result }
    func repair() async -> DaemonLifecycleResult { result }
}

private actor RecordingWorkspaceActions: WorkspaceActionSubmitting {
    let receipt: MutationReceipt
    private(set) var prepares: [PrepareWorktreeRequest] = []
    init(receipt: MutationReceipt) { self.receipt = receipt }
    func createWorktree(_ request: CreateWorktreeRequest) async throws -> MutationReceipt { receipt }
    func adoptWorktree(_ request: AdoptWorktreeRequest) async throws -> MutationReceipt { receipt }
    func archiveWorktree(_ request: ArchiveWorktreeRequest) async throws -> MutationReceipt { receipt }
    func prepareWorktree(_ request: PrepareWorktreeRequest) async throws -> MutationReceipt {
        prepares.append(request)
        return receipt
    }
}

@MainActor
struct ConfigurationAppModelTests {
    private static var fixtureURL: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "contracts/v2/fixtures/status.json")
    }

    @Test
    func `fixture model exposes pending acceptance per repository and accepts the exact digest`() async throws {
        let model = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL)
        await model.refresh()
        #expect(model.phase == .loaded)
        let repository = try #require(model.snapshot?.repositories.first)
        let presentation = try #require(model.configurationPresentation)
        #expect(presentation.status.state == .pending)
        #expect(model.acceptanceState(for: repository) == .pendingChange)
        #expect(model.configuredRepositoryKeys == ["sample", "second-sample"])
        #expect(model.canMutateConfiguration)

        // A stale digest never reaches the daemon.
        await model.acceptConfiguration(candidateDigest: "sha256:" + String(repeating: "0", count: 64))
        #expect(model.configurationPresentation?.status.state == .pending)

        let digest = try #require(presentation.status.candidate?.digest)
        await model.acceptConfiguration(candidateDigest: digest)
        #expect(model.configurationPresentation?.status.state == .accepted)
        #expect(model.configurationPresentation?.status.acceptedRevision == 5)
        #expect(model.acceptanceState(for: repository) == .accepted)
    }

    @Test
    func `validation and acceptance compare and swap against the observed revision`() async throws {
        let accepted = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(FixtureConfigurationActionClient.canonicalAcceptedJSON.utf8))
        let actions = RecordingConfigurationActions(current: accepted)
        let model = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL, configurationActions: actions)
        await model.refresh()
        #expect(model.configurationPresentation?.status.acceptedRevision == 5)

        await model.validateConfiguration()
        #expect(await actions.validations.map(\.expectedRevision) == [5])
        let candidate = try #require(model.configurationPresentation?.status.candidate)

        await actions.setFailNextAccept(true)
        await model.acceptConfiguration(candidateDigest: candidate.digest)
        #expect(model.configurationState.failureMessage?.contains("CONFIGURATION_REVISION_CONFLICT") == true)
        #expect(await actions.acceptances.map { ($0.expectedRevision, $0.digest) == (4, candidate.digest) } == [true])
        model.dismissConfigurationFailure()
        #expect(model.configurationState.status?.state == .pending)
        #expect(model.canMutateConfiguration)
    }

    @Test
    func `repository edits carry the observed revision and desired digest and surface daemon refusals`() async throws {
        let accepted = try ContractDecoder().decode(ConfigurationStatus.self, from: Data("""
        {"schemaVersion":2,"state":"accepted","acceptedRevision":5,
         "acceptedDigest":"sha256:\(String(repeating: "2", count: 64))",
         "desired":{"present":true,"sourceDigest":"sha256:\(String(repeating: "9", count: 64))","repositories":[
           {"key":"sample","enabled":true,"displayName":"Sample","root":"/Users/example/Developer/sample",
            "remote":"origin","defaultBase":"origin/main","managedWorktreesRoot":"/Users/example/Developer/sample-worktrees"}]}}
        """.utf8))
        let actions = RecordingConfigurationActions(current: accepted)
        let model = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL, configurationActions: actions)
        await model.refresh()
        #expect(model.canEditRepositoryConfiguration)
        #expect(model.configuredRepositoryKeys == ["sample"])
        let repository = try #require(model.snapshot?.repositories.first)
        #expect(model.desiredEntry(for: repository)?.key == "sample")

        // Disable rewrites the known entry with only the flag changed.
        #expect(await model.setRepositoryEnabled(key: "sample", enabled: false))
        var mutation = try #require(await actions.mutations.last)
        #expect(mutation.operation == .upsert)
        #expect(mutation.expectedRevision == 5)
        #expect(mutation.expectedSourceDigest == "sha256:" + String(repeating: "9", count: 64))
        #expect(mutation.entry?.enabled == false)
        #expect(mutation.entry?.root == "/Users/example/Developer/sample")
        #expect(model.configurationPresentation?.status.state == .pending)

        // After the daemon answered, the next edit uses the new digest.
        let draft = RepositoryConfigurationDraft(
            key: "second-sample", displayName: "Second", rootPath: "/Users/example/Developer/second-sample",
            managedWorktreesRoot: "/Users/example/Developer/second-sample-worktrees"
        )
        #expect(await model.saveRepositoryConfiguration(draft))
        mutation = try #require(await actions.mutations.last)
        #expect(mutation.expectedRevision == 4)
        #expect(mutation.expectedSourceDigest == "sha256:" + String(repeating: "3", count: 64))
        #expect(mutation.entry?.key == "second-sample")

        // A daemon refusal keeps the last good state and shows the code.
        await actions.setFailNextMutation(contractError("CONFIGURATION_DESIRED_CHANGED", "configuration.yaml changed since it was last read; reload and retry"))
        #expect(!(await model.removeRepositoryConfiguration(key: "second-sample")))
        mutation = try #require(await actions.mutations.last)
        #expect(mutation.operation == .remove)
        #expect(mutation.entry == nil)
        #expect(model.configurationState.failureMessage?.contains("CONFIGURATION_DESIRED_CHANGED") == true)
        model.dismissConfigurationFailure()
        #expect(model.configurationState.status?.state == .pending)
        #expect(model.canEditRepositoryConfiguration)

        // Flipping a key the desired file no longer lists never reaches the daemon.
        let before = await actions.mutations.count
        #expect(!(await model.setRepositoryEnabled(key: "vanished", enabled: true)))
        #expect(await actions.mutations.count == before)
        #expect(model.configurationState.failureMessage?.contains("vanished") == true)
    }

    @Test
    func `edits are unavailable while the desired file is unreadable or unknown`() async throws {
        let broken = try ContractDecoder().decode(ConfigurationStatus.self, from: Data("""
        {"schemaVersion":2,"state":"accepted","acceptedRevision":5,
         "acceptedDigest":"sha256:\(String(repeating: "2", count: 64))",
         "desired":{"present":true,"sourceDigest":"sha256:\(String(repeating: "9", count: 64))",
                    "problem":"decode YAML: line 3: did not find expected key","repositories":[]}}
        """.utf8))
        let actions = RecordingConfigurationActions(current: broken)
        let model = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL, configurationActions: actions)
        await model.refresh()
        #expect(model.canMutateConfiguration)
        #expect(!model.canEditRepositoryConfiguration)
        #expect(model.configurationPresentation?.desiredFileSummary.contains("did not find expected key") == true)
        #expect(!(await model.saveRepositoryConfiguration(RepositoryConfigurationDraft.suggested(forRootPath: "/tmp"))))
        #expect(await actions.mutations.isEmpty)

        let legacy = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(FixtureConfigurationActionClient.canonicalAcceptedJSON.utf8))
        let legacyModel = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL, configurationActions: RecordingConfigurationActions(current: legacy))
        await legacyModel.refresh()
        #expect(!legacyModel.canEditRepositoryConfiguration)
    }

    @Test
    func `fixture client enforces the daemon's add edit disable remove rules`() async throws {
        let client = FixtureConfigurationActionClient(scenario: .empty)
        let initial = try await client.configuration()
        #expect(initial.desired?.present == false)
        let entry = ConfigurationRepositoryEntry(
            key: "alpha", enabled: true, displayName: "Alpha", root: "/tmp/alpha",
            remote: "origin", defaultBase: "origin/main", managedWorktreesRoot: "/tmp/alpha-worktrees"
        )
        let added = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
            expectedRevision: 0, expectedSourceDigest: nil, operation: .upsert, key: "alpha", entry: entry
        ))
        #expect(added.state == .pending)
        #expect(added.desired?.repositories.map(\.key) == ["alpha"])
        let digest = try #require(added.desired?.sourceDigest)

        // Stale digest, repointed root, and removing an enabled entry are refused.
        await #expect(throws: FixtureError.self) {
            _ = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
                expectedRevision: 0, expectedSourceDigest: nil, operation: .upsert, key: "alpha", entry: entry
            ))
        }
        let repointed = ConfigurationRepositoryEntry(
            key: "alpha", enabled: true, displayName: "Alpha", root: "/tmp/elsewhere",
            remote: "origin", defaultBase: "origin/main", managedWorktreesRoot: "/tmp/alpha-worktrees"
        )
        await #expect(throws: FixtureError.self) {
            _ = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
                expectedRevision: 0, expectedSourceDigest: digest, operation: .upsert, key: "alpha", entry: repointed
            ))
        }
        await #expect(throws: FixtureError.self) {
            _ = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
                expectedRevision: 0, expectedSourceDigest: digest, operation: .remove, key: "alpha", entry: nil
            ))
        }
        let disabled = ConfigurationRepositoryEntry(
            key: "alpha", enabled: false, displayName: "Alpha", root: "/tmp/alpha",
            remote: "origin", defaultBase: "origin/main", managedWorktreesRoot: "/tmp/alpha-worktrees"
        )
        let paused = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
            expectedRevision: 0, expectedSourceDigest: digest, operation: .upsert, key: "alpha", entry: disabled
        ))
        let pausedDigest = try #require(paused.candidate?.digest)
        let accepted = try await client.acceptConfiguration(ConfigurationAcceptanceRequest(expectedRevision: 0, digest: pausedDigest))
        #expect(accepted.acceptedRevision == 1)
        let removed = try await client.mutateRepositoryConfiguration(ConfigurationRepositoryMutationRequest(
            expectedRevision: 1, expectedSourceDigest: accepted.desired?.sourceDigest, operation: .remove, key: "alpha", entry: nil
        ))
        #expect(removed.desired?.repositories.isEmpty == true)
    }

    @Test
    func `agent handoff prepares an unprepared worktree through the daemon and waits for the operation`() async throws {
        var root = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: Self.fixtureURL)) as? [String: Any])
        var repositories = try #require(root["repositories"] as? [[String: Any]])
        var worktrees = try #require(repositories[0]["worktrees"] as? [[String: Any]])
        worktrees[0]["workspace"] = nil
        repositories[0]["worktrees"] = worktrees
        root["repositories"] = repositories
        var operations = try #require(root["operations"] as? [[String: Any]])
        operations.append([
            "id": "operation_prepare_1", "kind": "prepareWorktree", "state": "succeeded",
            "createdAt": "2026-08-14T09:56:00Z", "updatedAt": "2026-08-14T09:56:30Z",
        ])
        root["operations"] = operations
        let snapshot = try ContractDecoder().decode(StatusSnapshot.self, from: JSONSerialization.data(withJSONObject: root))
        let session = DaemonSession(
            instanceId: snapshot.daemon.instanceId, daemonVersion: snapshot.daemon.version,
            endpoint: EndpointDescriptor(
                schemaVersion: contractSchemaVersion, transport: "http", host: "127.0.0.1", port: 49402,
                daemonVersion: snapshot.daemon.version, instanceId: snapshot.daemon.instanceId,
                pid: 4242, createdAt: snapshot.daemon.startedAt
            )
        )
        let report = DoctorReport(checks: [DoctorCheck(id: "live", title: "Live", outcome: .passed("healthy"))])
        let receipt = try ContractDecoder().decode(MutationReceipt.self, from: Data("""
        {"schemaVersion":2,"requestId":"app_x","operationId":"operation_prepare_1","acceptedAt":"2026-08-14T09:56:00Z"}
        """.utf8))
        let workspaceActions = RecordingWorkspaceActions(receipt: receipt)
        let model = AppModel(
            liveController: StubLifecycleController(result: DaemonLifecycleResult(state: .ready(session), snapshot: snapshot, doctorReport: report)),
            workspaceActions: workspaceActions,
            configurationActions: nil,
            agentConnections: nil,
            pollingInterval: .seconds(60)
        )
        await model.refresh()
        let worktree = try #require(model.snapshot?.repositories.first?.worktrees.first)
        #expect(!model.worktreeIsPrepared(worktree))
        #expect(model.canPrepareWorktree)

        let ready = await model.prepareWorktreeForHandoff(worktree)
        #expect(ready)
        #expect(model.agentHandoffState == .idle)
        let prepares = await workspaceActions.prepares
        #expect(prepares.map(\.worktreeId) == [worktree.id])
        #expect(prepares.first?.expectedEnvironmentRevision == nil)
    }
}

// MARK: - Codex launch

struct CodexTaskLauncherTests {
    @Test
    func `launch plan mirrors the Codex CLI deep link for the exact worktree`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        let worktree = root.appending(path: "work tree+a&b=c", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: worktree, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let application = URL(fileURLWithPath: "/Applications/Example.app", isDirectory: true)

        let plan = try CodexLaunchPlan.make(applicationURL: application, worktreePath: worktree.path)
        #expect(plan.applicationURL == application)
        #expect(plan.url.scheme == "codex")
        #expect(plan.url.host == "threads")
        #expect(plan.url.path == "/new")
        let expectedQuery = "path=" + worktree.path
            .replacingOccurrences(of: "/", with: "%2F")
            .replacingOccurrences(of: " ", with: "+")
            .replacingOccurrences(of: "+a&b=c", with: "%2Ba%26b%3Dc")
        #expect(plan.url.query == expectedQuery)
        // form-urlencoded round trip: `+` is a space, everything else is percent-decoded.
        let decoded = expectedQuery.dropFirst("path=".count)
            .replacingOccurrences(of: "+", with: " ")
            .removingPercentEncoding
        #expect(decoded == worktree.path)
        #expect(CodexLaunchPlan.bundleIdentifier == "com.openai.codex")
        #expect(CodexLaunchPlan.signingRequirement.contains("2DC432GLL2"))

        #expect(throws: CodexTaskError.invalidWorktree) {
            _ = try CodexLaunchPlan.make(applicationURL: application, worktreePath: "relative/path")
        }
        #expect(throws: CodexTaskError.invalidWorktree) {
            _ = try CodexLaunchPlan.make(applicationURL: application, worktreePath: root.appending(path: "missing").path)
        }
    }

    @Test
    func `launcher refuses missing or untrusted apps and hands the verified plan to open`() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let application = URL(fileURLWithPath: "/Applications/Example.app", isDirectory: true)

        let missing = CodexTaskLauncher(applicationURL: { nil }, verify: { _ in }, launch: { _ in })
        #expect(!missing.isInstalled)
        await #expect(throws: CodexTaskError.notInstalled) { try await missing.open(worktreePath: root.path) }

        let untrusted = CodexTaskLauncher(
            applicationURL: { application },
            verify: { _ in throw CodexTaskError.bundleNotTrusted },
            launch: { _ in Issue.record("launched an untrusted bundle") }
        )
        await #expect(throws: CodexTaskError.bundleNotTrusted) { try await untrusted.open(worktreePath: root.path) }

        let launched = LaunchRecorder()
        let trusted = CodexTaskLauncher(
            applicationURL: { application },
            verify: { url in #expect(url == application) },
            launch: { plan in await launched.record(plan) }
        )
        try await trusted.open(worktreePath: root.path)
        let plans = await launched.plans
        #expect(plans.count == 1)
        #expect(plans.first?.applicationURL == application)
        #expect(plans.first?.url.absoluteString.hasPrefix("codex://threads/new?path=") == true)
    }

    @Test
    func `signature verification rejects an unsigned bundle that merely claims the Codex identifier`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString, directoryHint: .isDirectory)
        let application = root.appending(path: "Codex.app", directoryHint: .isDirectory)
        let contents = application.appending(path: "Contents", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: contents.appending(path: "MacOS"), withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try """
        <?xml version="1.0" encoding="UTF-8"?>
        <plist version="1.0"><dict>
        <key>CFBundleIdentifier</key><string>com.openai.codex</string>
        <key>CFBundleExecutable</key><string>Codex</string>
        </dict></plist>
        """.write(to: contents.appending(path: "Info.plist"), atomically: true, encoding: .utf8)
        try FileManager.default.copyItem(atPath: "/usr/bin/true", toPath: contents.appending(path: "MacOS/Codex").path)
        #expect(throws: CodexTaskError.bundleNotTrusted) {
            try CodexLaunchPlan.verifyBundle(at: application)
        }
    }
}

private actor LaunchRecorder {
    private(set) var plans: [CodexLaunchPlan] = []
    func record(_ plan: CodexLaunchPlan) { plans.append(plan) }
}
