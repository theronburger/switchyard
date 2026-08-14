import Foundation

public let contractSchemaVersion = 1

public struct StatusSnapshot: Decodable, Sendable {
    public let schemaVersion: Int
    public let snapshotRevision: Int64
    public let generatedAt: Date
    public let daemon: DaemonStatus
    public let repositories: [Repository]
    public let environments: [Environment]
    public let operations: [Operation]
    public let alerts: [Alert]
}

public struct DaemonStatus: Decodable, Sendable {
    public let instanceId: String
    public let version: String
    public let state: DaemonState
    public let startedAt: Date
}

public enum DaemonState: String, ForwardCompatibleDecodable {
    case unknown
    case starting
    case ready
    case degraded
    case stopping
}

public struct Repository: Decodable, Identifiable, Sendable {
    public let id: String
    public let displayName: String
    public let rootPath: String
    public let adapter: String
    public let remote: String
    public let worktrees: [Worktree]
}

public struct Worktree: Decodable, Identifiable, Sendable {
    public let id: String
    public let path: String
    public let branch: String?
    public let headRevision: String
    public let isPrimary: Bool
    public let git: WorktreeState
}

public struct WorktreeState: Decodable, Sendable {
    public let hasTrackedChanges: Bool
    public let hasUntrackedFiles: Bool
    public let hasUnpushedCommits: Bool
    public let locked: Bool
    public let prunable: Bool
}

public struct Environment: Decodable, Identifiable, Sendable {
    public let id: String
    public let revision: Int64
    public let repositoryId: String
    public let worktreeId: String
    public let displayName: String
    public let desiredState: DesiredState
    public let observedState: ObservedState
    public let health: Health
    public let services: [Service]
    public let portLeases: [PortLease]
    public let infrastructureLeases: [InfrastructureLease]
    public let urls: [String: String]
    public let resources: ResourceUsage
    public let attentionAlertIds: [String]
}

public enum DesiredState: String, ForwardCompatibleDecodable {
    case unknown
    case stopped
    case running
}

public enum ObservedState: String, ForwardCompatibleDecodable {
    case unknown
    case stopped
    case starting
    case running
    case stopping
    case exited
    case failed
}

public enum Health: String, ForwardCompatibleDecodable {
    case unknown
    case notApplicable = "not_applicable"
    case starting
    case healthy
    case degraded
    case unhealthy
}

public struct Service: Decodable, Identifiable, Sendable {
    public let id: String
    public let displayName: String
    public let kind: String
    public let desiredState: DesiredState
    public let observedState: ObservedState
    public let health: Health
    public let portLeaseIds: [String]
    public let run: ServiceRun?
}

public struct ServiceRun: Decodable, Identifiable, Sendable {
    public let id: String
    public let startedAt: Date
    public let restartCount: Int
    public let processCount: Int
    public let cpuPercent: Double
    public let memoryBytes: Int64
}

public struct PortLease: Decodable, Identifiable, Sendable {
    public let id: String
    public let serviceId: String
    public let purpose: String
    public let host: String
    public let port: Int
    public let state: String
    public let acquiredAt: Date
}

public struct InfrastructureLease: Decodable, Identifiable, Sendable {
    public let id: String
    public let serviceId: String
    public let displayName: String
    public let kind: String
    public let scope: String
    public let state: String
    public let ownership: String
}

public struct ResourceUsage: Decodable, Sendable {
    public let cpuPercent: Double
    public let memoryBytes: Int64
}

public struct Operation: Decodable, Identifiable, Sendable {
    public let id: String
    public let kind: String
    public let state: OperationState
    public let environmentId: String?
    public let environmentRevision: Int64?
    public let createdAt: Date
    public let updatedAt: Date
    public let error: ContractError?
}

public enum OperationState: String, ForwardCompatibleDecodable {
    case unknown
    case pending
    case running
    case succeeded
    case failed
    case cancelled
}

public struct Alert: Decodable, Identifiable, Sendable {
    public let id: String
    public let environmentId: String?
    public let serviceId: String?
    public let severity: AlertSeverity
    public let code: String
    public let summary: String
    public let status: AlertStatus
    public let firstSeenAt: Date
    public let lastSeenAt: Date
    public let occurrences: Int
}

public enum AlertSeverity: String, ForwardCompatibleDecodable {
    case unknown
    case info
    case warning
    case error
}

public enum AlertStatus: String, ForwardCompatibleDecodable {
    case unknown
    case active
    case acknowledged
    case resolved
}

public struct ContractError: Decodable, Error, Sendable {
    public let code: String
    public let message: String
    public let retryable: Bool
    public let resourceKind: String?
    public let resourceId: String?
    public let currentState: String?
    public let requestedState: String?
}

public struct StartEnvironmentRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let worktreeId: String
    public let serviceIds: [String]

    public init(
        requestId: String,
        idempotencyKey: String,
        expectedEnvironmentRevision: Int64? = nil,
        worktreeId: String,
        serviceIds: [String]
    ) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = expectedEnvironmentRevision
        self.worktreeId = worktreeId
        self.serviceIds = serviceIds
    }
}

public struct StopEnvironmentRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?

    public init(
        requestId: String,
        idempotencyKey: String,
        expectedEnvironmentRevision: Int64? = nil
    ) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = expectedEnvironmentRevision
    }
}

public struct MutationReceipt: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let operationId: String
    public let acceptedAt: Date
    public let environmentId: String?
}

public struct EnvironmentContext: Decodable, Sendable {
    public let revision: Int64
    public let environmentId: String
    public let desiredState: DesiredState
    public let observedState: ObservedState
    public let health: Health
    public let urls: [String: String]
    public let attentionCount: Int
    public let attention: [AttentionItem]
    public let truncated: Bool
}

public struct AttentionItem: Decodable, Sendable {
    public let severity: AlertSeverity
    public let code: String
    public let summary: String
}

public protocol ForwardCompatibleDecodable: RawRepresentable, Decodable, Sendable where RawValue == String {
    static var unknown: Self { get }
}

public extension ForwardCompatibleDecodable {
    init(from decoder: Decoder) throws {
        let rawValue = try decoder.singleValueContainer().decode(String.self)
        self = Self(rawValue: rawValue) ?? Self.unknown
    }
}
