import Foundation

public protocol EnvironmentActionSubmitting: Sendable {
    func startEnvironment(_ request: StartEnvironmentRequest) async throws -> MutationReceipt
    func stopEnvironment(id: String, request: StopEnvironmentRequest) async throws -> MutationReceipt
}

public struct LiveEnvironmentActionClient: EnvironmentActionSubmitting {
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()) {
        self.connectionFactory = connectionFactory
    }

    public func startEnvironment(_ request: StartEnvironmentRequest) async throws -> MutationReceipt {
        let client = try await verifiedClient()
        return try await client.startEnvironment(request)
    }

    public func stopEnvironment(id: String, request: StopEnvironmentRequest) async throws -> MutationReceipt {
        let client = try await verifiedClient()
        return try await client.stopEnvironment(id: id, request: request)
    }

    private func verifiedClient() async throws -> DaemonClient {
        let connection = try connectionFactory.connect()
        _ = try await connection.client.handshake()
        return connection.client
    }
}
