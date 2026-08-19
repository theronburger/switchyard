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
        guard handshake.schemaVersion == EndpointDescriptor.supportedSchemaVersion,
              handshake.supportedSchemaVersions.contains(EndpointDescriptor.supportedSchemaVersion),
              handshake.daemonInstanceId == descriptor.daemonInstanceId else {
            throw DaemonClientError.malformedResponse("handshake identity or schema does not match the runtime descriptor")
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

    private func get<Value: Decodable>(_ type: Value.Type, path: String) async throws -> Value {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.httpMethod = "GET"
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.setValue(token.authorizationHeaderValue, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(UUID().uuidString, forHTTPHeaderField: "X-Switchyard-Request-Id")

        return try await send(request, decoding: type, successStatus: 200)
    }

    private func post<Body: Encodable, Value: Decodable>(
        _ type: Value.Type,
        pathComponents: [String],
        body: Body,
        requestId: String
    ) async throws -> Value {
        let url = pathComponents.reduce(baseURL) { partial, component in
            partial.appending(path: component)
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        request.setValue(token.authorizationHeaderValue, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue(requestId, forHTTPHeaderField: "X-Switchyard-Request-Id")
        do {
            request.httpBody = try JSONEncoder().encode(body)
        } catch {
            throw DaemonClientError.invalidRequest("request body could not be encoded")
        }
        return try await send(request, decoding: type, successStatus: 202)
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

private struct ContractErrorEnvelope: Decodable {
    let error: ContractError
}
