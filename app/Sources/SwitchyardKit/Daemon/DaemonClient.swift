import Foundation

/// Result of the exact-version handshake with the daemon.
public struct HandshakeResponse: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let daemonInstanceId: String
    public let daemonVersion: String
    public let supportedSchemaVersions: [Int]
}

/// Transport seam so the client is exercisable without a live daemon.
public protocol DaemonTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse)
}

/// Production transport: an ephemeral, cache-free `URLSession`.
public struct URLSessionDaemonTransport: DaemonTransport {
    private let session: URLSession

    public init(timeout: TimeInterval = 5) {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = timeout
        configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        configuration.httpCookieStorage = nil
        configuration.urlCache = nil
        session = URLSession(
            configuration: configuration,
            delegate: RedirectRejectingSessionDelegate.shared,
            delegateQueue: nil
        )
    }

    public func send(_ request: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else {
            throw DaemonClientError.malformedResponse("response was not HTTP")
        }
        try Self.validateNoRedirect(request: request, response: http)
        return (data, http)
    }

    public static func validateNoRedirect(request: URLRequest, response: HTTPURLResponse) throws {
        guard !(300...399).contains(response.statusCode), response.url == request.url else {
            throw DaemonClientError.redirectRejected
        }
    }
}

private final class RedirectRejectingSessionDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    static let shared = RedirectRejectingSessionDelegate()

    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil)
    }
}

public enum DaemonClientError: Error, Sendable, CustomStringConvertible {
    case invalidRequest(String)
    case unauthorized
    case upgradeRequired(message: String)
    case contract(ContractError)
    case transportFailure(String)
    case malformedResponse(String)
    case httpStatus(Int)
    case redirectRejected

    public var description: String {
        switch self {
        case .invalidRequest(let detail):
            return "the daemon request is invalid: \(detail)"
        case .unauthorized:
            return "the daemon rejected this client's credentials"
        case .upgradeRequired(let message):
            return "daemon and app versions are incompatible: \(message)"
        case .contract(let error):
            return "daemon error \(error.code): \(error.message)"
        case .transportFailure(let detail):
            return "could not reach the daemon: \(detail)"
        case .malformedResponse(let detail):
            return "daemon response was malformed: \(detail)"
        case .httpStatus(let code):
            return "daemon returned unexpected HTTP status \(code)"
        case .redirectRejected:
            return "daemon HTTP redirects are not accepted"
        }
    }
}

/// Typed client for the authenticated loopback HTTP contract (D-015).
///
/// The bearer token travels only in the `Authorization` header — never in
/// URLs, logs, or error payloads.
public struct DaemonClient: Sendable {
    /// Stable machine-readable code the daemon uses for version mismatches.
    public static let upgradeRequiredCode = "UPGRADE_REQUIRED"
    /// Every request declares this client's exact contract schema version so
    /// the daemon can answer a mismatch with HTTP 426 and the stable code.
    public static let schemaVersionHeader = "X-Switchyard-Schema-Version"
    /// HTTP status the daemon uses for an exact-version mismatch.
    public static let upgradeRequiredStatus = 426

    private let baseURL: URL
    private let descriptor: EndpointDescriptor
    private let token: BearerToken
    private let transport: any DaemonTransport
    private let decoder = ContractDecoder()

    public init(
        descriptor: EndpointDescriptor,
        token: BearerToken,
        transport: any DaemonTransport = URLSessionDaemonTransport()
    ) throws {
        self.baseURL = try descriptor.loopbackBaseURL()
        self.descriptor = descriptor
        self.token = token
        self.transport = transport
    }

    public func handshake() async throws -> HandshakeResponse {
        let handshake = try await get(HandshakeResponse.self, path: "handshake")
        guard handshake.daemonInstanceId == descriptor.daemonInstanceId else {
            throw DaemonClientError.malformedResponse("handshake identity does not match the runtime descriptor")
        }
        guard handshake.schemaVersion > 0, !handshake.supportedSchemaVersions.isEmpty,
              handshake.supportedSchemaVersions.allSatisfy({ $0 > 0 }) else {
            throw DaemonClientError.malformedResponse("handshake schema versions are invalid")
        }
        guard handshake.schemaVersion == EndpointDescriptor.supportedSchemaVersion,
              handshake.supportedSchemaVersions.contains(EndpointDescriptor.supportedSchemaVersion) else {
            throw DaemonClientError.upgradeRequired(
                message: "daemon speaks contract schema version \(handshake.schemaVersion); this app requires \(EndpointDescriptor.supportedSchemaVersion)"
            )
        }
        guard handshake.daemonVersion == descriptor.daemonVersion else {
            throw DaemonClientError.upgradeRequired(message: "daemon version does not match the runtime descriptor")
        }
        return handshake
    }

    public func status() async throws -> StatusSnapshot {
        let snapshot = try await get(StatusSnapshot.self, path: "v1/status")
        guard snapshot.daemon.instanceId == descriptor.daemonInstanceId,
              snapshot.daemon.version == descriptor.daemonVersion else {
            throw DaemonClientError.malformedResponse("status identity does not match the runtime descriptor")
        }
        return snapshot
    }

    public func startEnvironment(_ request: StartEnvironmentRequest) async throws -> MutationReceipt {
        try Self.validate(request)
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "environments"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    public func stopEnvironment(
        id environmentId: String,
        request: StopEnvironmentRequest
    ) async throws -> MutationReceipt {
        try Self.validate(request)
        guard Self.validPathIdentifier(environmentId) else {
            throw DaemonClientError.invalidRequest("environment ID is not safe for the local API path")
        }
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "environments", environmentId, "stop"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    public func createWorktree(_ request: CreateWorktreeRequest) async throws -> MutationReceipt {
        try Self.validate(request)
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "worktrees"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    public func archiveWorktree(_ request: ArchiveWorktreeRequest) async throws -> MutationReceipt {
        try Self.validate(request)
        guard Self.validPathIdentifier(request.worktreeId) else {
            throw DaemonClientError.invalidRequest("worktree ID is not safe for the local API path")
        }
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "worktrees", request.worktreeId, "archive"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    public func adoptWorktree(_ request: AdoptWorktreeRequest) async throws -> MutationReceipt {
        try Self.validate(request)
        guard Self.validPathIdentifier(request.worktreeId) else {
            throw DaemonClientError.invalidRequest("worktree ID is not safe for the local API path")
        }
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "worktrees", request.worktreeId, "adopt"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    public func prepareWorktree(_ request: PrepareWorktreeRequest) async throws -> MutationReceipt {
        try Self.validate(request)
        guard Self.validPathIdentifier(request.worktreeId) else {
            throw DaemonClientError.invalidRequest("worktree ID is not safe for the local API path")
        }
        let receipt = try await post(
            MutationReceipt.self,
            pathComponents: ["v1", "worktrees", request.worktreeId, "prepare"],
            body: request,
            requestId: request.requestId
        )
        try Self.validate(receipt, requestId: request.requestId)
        return receipt
    }

    /// Records an explicit handoff lease after the app launched a task into a
    /// worktree. The daemon is the only lease writer; the app never infers
    /// occupancy from the launcher.
    public func acquireOccupancy(_ request: AcquireOccupancyRequest) async throws -> OccupancyLease {
        try Self.validate(request)
        let lease = try await post(
            OccupancyLease.self,
            pathComponents: ["v1", "worktrees", request.worktreeId, "occupancy"],
            body: request,
            requestId: request.requestId,
            successStatus: 200
        )
        guard lease.worktreeId == request.worktreeId, lease.state == .held, Self.validOpaqueValue(lease.id, maximumBytes: 256) else {
            throw DaemonClientError.malformedResponse("occupancy lease does not match the request")
        }
        return lease
    }

    public func releaseOccupancy(_ request: ReleaseOccupancyRequest) async throws -> OccupancyLease {
        try Self.validate(request)
        let lease = try await post(
            OccupancyLease.self,
            pathComponents: ["v1", "worktrees", request.worktreeId, "occupancy", request.leaseId, "release"],
            body: request,
            requestId: request.requestId,
            successStatus: 200
        )
        guard lease.worktreeId == request.worktreeId, lease.id == request.leaseId, lease.state == .released else {
            throw DaemonClientError.malformedResponse("released occupancy lease does not match the request")
        }
        return lease
    }

    public func configuration() async throws -> ConfigurationStatus {
        let status = try await get(ConfigurationStatus.self, path: "v1/configuration")
        try Self.validate(status)
        return status
    }

    public func validateConfiguration(_ request: ConfigurationValidationRequest) async throws -> ConfigurationStatus {
        guard request.schemaVersion == contractSchemaVersion, request.expectedRevision >= 0 else {
            throw DaemonClientError.invalidRequest("configuration validation request is invalid")
        }
        let status = try await post(
            ConfigurationStatus.self,
            pathComponents: ["v1", "configuration", "validate"],
            body: request,
            requestId: "configuration_\(UUID().uuidString.lowercased())",
            successStatus: 200
        )
        try Self.validate(status)
        return status
    }

    public func acceptConfiguration(_ request: ConfigurationAcceptanceRequest) async throws -> ConfigurationStatus {
        guard request.schemaVersion == contractSchemaVersion, request.expectedRevision >= 0,
              Self.validDigest(request.digest) else {
            throw DaemonClientError.invalidRequest("configuration acceptance request is invalid")
        }
        let status = try await post(
            ConfigurationStatus.self,
            pathComponents: ["v1", "configuration", "accept"],
            body: request,
            requestId: "configuration_\(UUID().uuidString.lowercased())",
            successStatus: 200
        )
        try Self.validate(status)
        return status
    }

    public func mutateRepositoryConfiguration(_ request: ConfigurationRepositoryMutationRequest) async throws -> ConfigurationStatus {
        guard request.schemaVersion == contractSchemaVersion, request.expectedRevision >= 0,
              request.expectedSourceDigest.map(Self.validDigest) ?? true,
              Self.validRepositoryKey(request.key),
              request.operation == .remove ? request.entry == nil : request.entry?.key == request.key else {
            throw DaemonClientError.invalidRequest("configuration repository mutation is invalid")
        }
        let status = try await post(
            ConfigurationStatus.self,
            pathComponents: ["v1", "configuration", "repositories"],
            body: request,
            requestId: "configuration_\(UUID().uuidString.lowercased())",
            successStatus: 200
        )
        try Self.validate(status)
        return status
    }

    public func planCleanup(_ request: CleanupPlanRequest) async throws -> CleanupPlan {
        guard request.schemaVersion == contractSchemaVersion,
              request.scope.kind == "global" && request.scope.id == nil ||
                ((request.scope.kind == "repository" || request.scope.kind == "worktree") &&
                    Self.validOpaqueValue(request.scope.id ?? "", maximumBytes: 256)) else {
            throw DaemonClientError.invalidRequest("cleanup scope is invalid")
        }
        return try await post(
            CleanupPlan.self,
            pathComponents: ["v1", "cleanup", "plans"],
            body: request,
            requestId: "cleanup_\(UUID().uuidString.lowercased())",
            successStatus: 201
        )
    }

    public func applyCleanup(_ request: CleanupApplyRequest) async throws -> CleanupResult {
        guard request.schemaVersion == contractSchemaVersion,
              Self.validPathIdentifier(request.planId), request.expectedRevision > 0,
              request.candidateIds.count <= 1_024,
              Set(request.candidateIds).count == request.candidateIds.count,
              request.candidateIds.allSatisfy({ Self.validOpaqueValue($0, maximumBytes: 256) }) else {
            throw DaemonClientError.invalidRequest("cleanup selection is invalid")
        }
        return try await post(
            CleanupResult.self,
            pathComponents: ["v1", "cleanup", "plans", request.planId, "apply"],
            body: request,
            requestId: "cleanup_\(UUID().uuidString.lowercased())",
            successStatus: 200
        )
    }

    private func get<Value: Decodable>(_ type: Value.Type, path: String) async throws -> Value {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.httpMethod = "GET"
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.setValue(token.authorizationHeaderValue, forHTTPHeaderField: "Authorization")
        request.setValue(String(contractSchemaVersion), forHTTPHeaderField: Self.schemaVersionHeader)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(UUID().uuidString, forHTTPHeaderField: "X-Switchyard-Request-Id")

        return try await send(request, decoding: type, successStatus: 200)
    }

    private func post<Body: Encodable, Value: Decodable>(
        _ type: Value.Type,
        pathComponents: [String],
        body: Body,
        requestId: String,
        successStatus: Int = 202
    ) async throws -> Value {
        let url = pathComponents.reduce(baseURL) { partial, component in
            partial.appending(path: component)
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.setValue(token.authorizationHeaderValue, forHTTPHeaderField: "Authorization")
        request.setValue(String(contractSchemaVersion), forHTTPHeaderField: Self.schemaVersionHeader)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(requestId, forHTTPHeaderField: "X-Switchyard-Request-Id")
        do {
            request.httpBody = try JSONEncoder().encode(body)
        } catch {
            throw DaemonClientError.invalidRequest("request body could not be encoded")
        }
        return try await send(request, decoding: type, successStatus: successStatus)
    }

    private func send<Value: Decodable>(
        _ request: URLRequest,
        decoding type: Value.Type,
        successStatus: Int
    ) async throws -> Value {
        let data: Data
        let response: HTTPURLResponse
        do {
            (data, response) = try await transport.send(request)
        } catch let error as DaemonClientError {
            throw error
        } catch {
            throw DaemonClientError.transportFailure(error.localizedDescription)
        }

        switch response.statusCode {
        case successStatus:
            try Self.validateSecurityHeaders(response)
            do {
                return try decoder.decode(type, from: data)
            } catch {
                throw DaemonClientError.malformedResponse(String(describing: error))
            }
        case 401, 403:
            throw DaemonClientError.unauthorized
        case Self.upgradeRequiredStatus:
            // The status line alone is authoritative: an unreadable envelope
            // still means the daemon and app disagree on the exact contract.
            if let envelope = try? decoder.decode(ContractErrorEnvelope.self, from: data),
               envelope.error.code == Self.upgradeRequiredCode {
                throw DaemonClientError.upgradeRequired(message: envelope.error.message)
            }
            throw DaemonClientError.upgradeRequired(message: "the daemon requires a different contract schema version")
        default:
            if let envelope = try? decoder.decode(ContractErrorEnvelope.self, from: data) {
                if envelope.error.code == Self.upgradeRequiredCode {
                    throw DaemonClientError.upgradeRequired(message: envelope.error.message)
                }
                throw DaemonClientError.contract(envelope.error)
            }
            throw DaemonClientError.httpStatus(response.statusCode)
        }
    }

    private static func validateSecurityHeaders(_ response: HTTPURLResponse) throws {
        guard response.value(forHTTPHeaderField: "Cache-Control")?
            .lowercased().contains("no-store") == true,
              response.value(forHTTPHeaderField: "X-Content-Type-Options")?
            .lowercased() == "nosniff" else {
            throw DaemonClientError.malformedResponse("response is missing required security headers")
        }
    }

    private static func validate(_ request: StartEnvironmentRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
        guard validOpaqueValue(request.worktreeId, maximumBytes: 256),
              validOpaqueValue(request.targetId, maximumBytes: 256),
              (request.confirmedTargetId == nil || (
                  request.confirmedTargetId == request.targetId &&
                      validOpaqueValue(request.confirmedTargetId ?? "", maximumBytes: 256)
              )),
              !request.serviceIds.isEmpty,
              request.serviceIds.count <= 32,
              Set(request.serviceIds).count == request.serviceIds.count,
              request.serviceIds.allSatisfy({ validOpaqueValue($0, maximumBytes: 256) }) else {
            throw DaemonClientError.invalidRequest("worktree or service selection is invalid")
        }
    }

    private static func validate(_ request: StopEnvironmentRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
    }

    private static func validate(_ request: CreateWorktreeRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
        guard request.expectedEnvironmentRevision == nil,
              validOpaqueValue(request.repositoryId, maximumBytes: 256),
              validOpaqueValue(request.branch, maximumBytes: 256),
              request.startPoint.map({ validOpaqueValue($0, maximumBytes: 256) }) ?? true else {
            throw DaemonClientError.invalidRequest("repository, branch, or base is invalid")
        }
    }

    private static func validate(_ request: ArchiveWorktreeRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
        guard request.expectedEnvironmentRevision == nil,
              validOpaqueValue(request.worktreeId, maximumBytes: 256) else {
            throw DaemonClientError.invalidRequest("worktree selection is invalid")
        }
    }

    private static func validate(_ request: AdoptWorktreeRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
        guard request.expectedEnvironmentRevision == nil,
              validOpaqueValue(request.worktreeId, maximumBytes: 256) else {
            throw DaemonClientError.invalidRequest("worktree selection is invalid")
        }
    }

    private static func validate(_ request: PrepareWorktreeRequest) throws {
        try validateMutation(
            schemaVersion: request.schemaVersion,
            requestId: request.requestId,
            idempotencyKey: request.idempotencyKey,
            expectedEnvironmentRevision: request.expectedEnvironmentRevision
        )
        guard request.expectedEnvironmentRevision == nil,
              validOpaqueValue(request.worktreeId, maximumBytes: 256) else {
            throw DaemonClientError.invalidRequest("worktree selection is invalid")
        }
    }

    /// Holder kinds are generic lowercase tokens such as `agent-task`; labels
    /// are bounded single-line display text without path separators.
    static func validOccupancyHolder(kind: String, label: String) -> Bool {
        guard !kind.isEmpty, kind.utf8.count <= 64,
              kind.first.map({ $0.isLetter && $0.isLowercase }) == true,
              !kind.hasSuffix("-"), !kind.contains("--"),
              kind.allSatisfy({ ($0.isLetter && $0.isLowercase && $0.isASCII) || ($0.isNumber && $0.isASCII) || $0 == "-" }) else {
            return false
        }
        return validOpaqueValue(label, maximumBytes: 256) && !label.contains("/") && !label.contains("\\")
    }

    private static func validate(_ request: AcquireOccupancyRequest) throws {
        guard request.schemaVersion == contractSchemaVersion,
              validOpaqueValue(request.requestId, maximumBytes: 256),
              validOpaqueValue(request.worktreeId, maximumBytes: 256),
              validPathIdentifier(request.worktreeId),
              validOccupancyHolder(kind: request.holderKind, label: request.holderLabel) else {
            throw DaemonClientError.invalidRequest("occupancy request is invalid")
        }
    }

    private static func validate(_ request: ReleaseOccupancyRequest) throws {
        guard request.schemaVersion == contractSchemaVersion,
              validOpaqueValue(request.requestId, maximumBytes: 256),
              validOpaqueValue(request.worktreeId, maximumBytes: 256),
              validPathIdentifier(request.worktreeId),
              validOpaqueValue(request.leaseId, maximumBytes: 256),
              validPathIdentifier(request.leaseId) else {
            throw DaemonClientError.invalidRequest("occupancy release request is invalid")
        }
    }

    private static func validate(_ status: ConfigurationStatus) throws {
        guard status.schemaVersion == contractSchemaVersion,
              status.acceptedRevision >= 0,
              status.state != .unknown else {
            throw DaemonClientError.malformedResponse("configuration status is invalid")
        }
        if status.acceptedRevision == 0 {
            guard status.acceptedDigest == nil || status.acceptedDigest == "" else {
                throw DaemonClientError.malformedResponse("accepted configuration identity is invalid")
            }
        } else {
            guard validDigest(status.acceptedDigest ?? "") else {
                throw DaemonClientError.malformedResponse("accepted configuration identity is invalid")
            }
        }
        if status.state == .pending, status.candidate == nil {
            throw DaemonClientError.malformedResponse("pending configuration candidate is required")
        }
        if let desired = status.desired {
            guard desired.sourceDigest.map(validDigest) ?? true,
                  desired.present || (desired.sourceDigest == nil && desired.repositories.isEmpty),
                  Set(desired.repositories.map(\.key)).count == desired.repositories.count,
                  desired.repositories.allSatisfy({ validRepositoryKey($0.key) && $0.root.hasPrefix("/") && $0.managedWorktreesRoot.hasPrefix("/") }) else {
                throw DaemonClientError.malformedResponse("configuration desired file is invalid")
            }
        }
        if let candidate = status.candidate {
            guard candidate.schemaVersion == contractSchemaVersion,
                  validDigest(candidate.digest), validDigest(candidate.sourceDigest),
                  !candidate.compilerVersion.isEmpty,
                  candidate.repositoryDigests.allSatisfy({ validOpaqueValue($0.key, maximumBytes: 256) && validDigest($0.value) }),
                  candidate.executableDigests.allSatisfy({ !$0.key.isEmpty && validDigest($0.value) }) else {
                throw DaemonClientError.malformedResponse("configuration candidate is invalid")
            }
        }
    }

    /// Repository keys are stable opaque identifiers: lowercase, digits, and
    /// single hyphens, starting with a letter.
    public static func validRepositoryKey(_ value: String) -> Bool {
        guard let first = value.unicodeScalars.first, value.utf8.count <= 64,
              ("a"..."z").contains(first) else { return false }
        var previousHyphen = false
        for scalar in value.unicodeScalars {
            let lower = ("a"..."z").contains(scalar), digit = ("0"..."9").contains(scalar)
            if scalar == "-" {
                if previousHyphen { return false }
                previousHyphen = true
            } else if lower || digit {
                previousHyphen = false
            } else {
                return false
            }
        }
        return !previousHyphen
    }

    /// `sha256:` followed by exactly 64 lowercase hexadecimal digits.
    public static func validDigest(_ value: String) -> Bool {
        let prefix = "sha256:"
        guard value.hasPrefix(prefix) else { return false }
        let hex = value.dropFirst(prefix.count)
        return hex.count == 64 && hex.allSatisfy { "0123456789abcdef".contains($0) }
    }

    private static func validateMutation(
        schemaVersion: Int,
        requestId: String,
        idempotencyKey: String,
        expectedEnvironmentRevision: Int64?
    ) throws {
        guard schemaVersion == contractSchemaVersion,
              validOpaqueValue(requestId, maximumBytes: 256),
              validOpaqueValue(idempotencyKey, maximumBytes: 512),
              expectedEnvironmentRevision.map({ $0 >= 0 }) ?? true else {
            throw DaemonClientError.invalidRequest("mutation identity or revision is invalid")
        }
    }

    private static func validate(_ receipt: MutationReceipt, requestId: String) throws {
        guard receipt.schemaVersion == contractSchemaVersion,
              receipt.requestId == requestId,
              validOpaqueValue(receipt.operationId, maximumBytes: 256),
              receipt.runId.map({ validOpaqueValue($0, maximumBytes: 256) }) ?? true,
              receipt.environmentId.map({ validOpaqueValue($0, maximumBytes: 256) }) ?? true else {
            throw DaemonClientError.malformedResponse("mutation receipt identity or schema is invalid")
        }
    }

    private static func validOpaqueValue(_ value: String, maximumBytes: Int) -> Bool {
        !value.isEmpty &&
            value.utf8.count <= maximumBytes &&
            value.trimmingCharacters(in: .whitespacesAndNewlines) == value &&
            value.unicodeScalars.allSatisfy { !CharacterSet.controlCharacters.contains($0) }
    }

    private static func validPathIdentifier(_ value: String) -> Bool {
        validOpaqueValue(value, maximumBytes: 256) && value.unicodeScalars.allSatisfy {
            $0 != "/" && !CharacterSet.whitespacesAndNewlines.contains($0)
        }
    }
}

