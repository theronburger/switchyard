import Foundation

public protocol CleanupActionSubmitting: Sendable {
    func planCleanup(_ request: CleanupPlanRequest) async throws -> CleanupPlan
    func applyCleanup(_ request: CleanupApplyRequest) async throws -> CleanupResult
}

public struct LiveCleanupActionClient: CleanupActionSubmitting {
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()) {
        self.connectionFactory = connectionFactory
    }

    public func planCleanup(_ request: CleanupPlanRequest) async throws -> CleanupPlan {
        let client = try await verifiedClient()
        return try await client.planCleanup(request)
    }

    public func applyCleanup(_ request: CleanupApplyRequest) async throws -> CleanupResult {
        let client = try await verifiedClient()
        return try await client.applyCleanup(request)
    }

    private func verifiedClient() async throws -> DaemonClient {
        let connection = try connectionFactory.connect()
        _ = try await connection.client.handshake()
        return connection.client
    }
}
