import Foundation

/// Records and releases explicit worktree handoff leases through the daemon.
///
/// A lease is the only occupancy evidence Switchyard keeps for tasks the app
/// launches. It is acquired after a launch the app performed, never inferred
/// from a deep link or a running process, and ends only when the owner
/// releases it.
public protocol OccupancyActionSubmitting: Sendable {
    func acquireOccupancy(_ request: AcquireOccupancyRequest) async throws -> OccupancyLease
    func releaseOccupancy(_ request: ReleaseOccupancyRequest) async throws -> OccupancyLease
}

public struct LiveOccupancyActionClient: OccupancyActionSubmitting {
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()) {
        self.connectionFactory = connectionFactory
    }

    public func acquireOccupancy(_ request: AcquireOccupancyRequest) async throws -> OccupancyLease {
        let client = try await verifiedClient()
        return try await client.acquireOccupancy(request)
    }

    public func releaseOccupancy(_ request: ReleaseOccupancyRequest) async throws -> OccupancyLease {
        let client = try await verifiedClient()
        return try await client.releaseOccupancy(request)
    }

    private func verifiedClient() async throws -> DaemonClient {
        let connection = try connectionFactory.connect()
        _ = try await connection.client.handshake()
        return connection.client
    }
}
