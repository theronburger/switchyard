import Foundation

public protocol WorkspaceActionSubmitting: Sendable {
    func createWorktree(_ request: CreateWorktreeRequest) async throws -> MutationReceipt
    func adoptWorktree(_ request: AdoptWorktreeRequest) async throws -> MutationReceipt
    func archiveWorktree(_ request: ArchiveWorktreeRequest) async throws -> MutationReceipt
}

public struct LiveWorkspaceActionClient: WorkspaceActionSubmitting {
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()) {
        self.connectionFactory = connectionFactory
    }

    public func createWorktree(_ request: CreateWorktreeRequest) async throws -> MutationReceipt {
        let client = try await verifiedClient()
        return try await client.createWorktree(request)
    }

    public func archiveWorktree(_ request: ArchiveWorktreeRequest) async throws -> MutationReceipt {
        let client = try await verifiedClient()
        return try await client.archiveWorktree(request)
    }

    public func adoptWorktree(_ request: AdoptWorktreeRequest) async throws -> MutationReceipt {
        let client = try await verifiedClient()
        return try await client.adoptWorktree(request)
    }

    private func verifiedClient() async throws -> DaemonClient {
        let connection = try connectionFactory.connect()
        _ = try await connection.client.handshake()
        return connection.client
    }
}
