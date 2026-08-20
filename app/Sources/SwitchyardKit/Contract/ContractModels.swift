import Foundation

public let contractSchemaVersion = 2

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
    public let profileKey: String
    public let remote: String
    public let worktrees: [Worktree]
    public let runtime: RepositoryRuntime?
    public let observation: RepositoryObservation?
}

public struct RepositoryObservation: Decodable, Sendable {
    public let observedAt: Date?
    public let lastAttemptAt: Date
    public let stale: Bool
    public let errorCode: String?
}

public struct RepositoryRuntime: Decodable, Sendable {
    public let defaultTargetId: String
    public let targets: [RuntimeTarget]
    public let services: [RuntimeService]
}

public struct RuntimeTarget: Decodable, Identifiable, Sendable {
    public let id: String
    public let displayName: String
    public let risk: String
    public let warnOnStart: Bool
}

public struct RuntimeService: Decodable, Identifiable, Sendable {
    public let id: String
    public let displayName: String
    public let kind: String
    public let available: Bool
    public let unavailableReason: String?
}

public struct Worktree: Decodable, Identifiable, Sendable {
    public let id: String
    public let path: String
    public let branch: String?
    public let headRevision: String
    public let isPrimary: Bool
    public let git: WorktreeState
    public let changes: WorktreeChanges?
    public let pullRequest: PullRequestObservation?
    public let workspace: WorkspaceStatus?
}

public struct WorkspaceStatus: Decodable, Sendable {
    public let ownership: WorkspaceOwnership
    public let state: WorkspacePreparationState
    public let fingerprint: String
    public let preparedAt: Date
    public let toolchains: [WorkspaceToolchain]
}

public enum WorkspaceOwnership: String, ForwardCompatibleDecodable {
    case unknown
    case adopted
    case managed
}

public enum WorkspacePreparationState: String, ForwardCompatibleDecodable {
    case unknown
    case unprepared
    case ready
}

public struct WorkspaceToolchain: Decodable, Sendable, Identifiable {
    public let id: String
    public let requestedVersion: String
    public let resolvedVersion: String
}

public struct PullRequestObservation: Decodable, Sendable {
    public let status: PullRequestAvailability
    public let account: String?
    public let observedAt: Date?
    public let lastAttemptAt: Date
    public let stale: Bool
    public let errorCode: String?
    public let pullRequest: PullRequest?
}

public enum PullRequestAvailability: String, ForwardCompatibleDecodable {
    case unknown
    case found
    case none
    case unavailable
}

public struct PullRequest: Decodable, Sendable {
    public let number: Int
    public let title: String
    public let url: String
    public let state: PullRequestState
    public let draft: Bool
    public let mergeable: PullRequestMergeable
    public let mergeState: PullRequestMergeState
    public let reviewDecision: PullRequestReviewDecision
    public let baseBranch: String
    public let headBranch: String
    public let headRevision: String
    public let createdAt: Date
    public let updatedAt: Date
    public let closedAt: Date?
    public let mergedAt: Date?
    public let checks: PullRequestChecks
}

public enum PullRequestState: String, ForwardCompatibleDecodable {
    case unknown
    case open
    case closed
    case merged
}

public enum PullRequestMergeable: String, ForwardCompatibleDecodable {
    case unknown
    case mergeable
    case conflicting
    case notApplicable = "not_applicable"
}

public enum PullRequestMergeState: String, ForwardCompatibleDecodable {
    case unknown
    case clean
    case blocked
    case behind
    case dirty
    case hasHooks = "has_hooks"
    case unstable
    case notApplicable = "not_applicable"
}

public enum PullRequestReviewDecision: String, ForwardCompatibleDecodable {
    case unknown
    case approved
    case changesRequested = "changes_requested"
    case reviewRequired = "review_required"
    case notApplicable = "not_applicable"
}

public struct PullRequestChecks: Decodable, Sendable {
    public let state: PullRequestChecksState
    public let total: Int
    public let passing: Int
    public let failing: Int
    public let pending: Int
    public let skipping: Int
    public let cancelled: Int
    public let items: [PullRequestCheck]
}

public enum PullRequestChecksState: String, ForwardCompatibleDecodable {
    case unknown
    case passing
    case failing
    case pending
    case cancelled
    case neutral
    case none
    case unavailable
}

public struct PullRequestCheck: Decodable, Identifiable, Sendable {
    public var id: String { "\(workflow)-\(name)-\(url)" }
    public let name: String
    public let workflow: String
    public let state: String
    public let bucket: PullRequestCheckBucket
    public let url: String
    public let startedAt: Date?
    public let completedAt: Date?
}

public enum PullRequestCheckBucket: String, ForwardCompatibleDecodable {
    case unknown
    case pass
    case fail
    case pending
    case skipping
    case cancel
}

public struct WorktreeState: Decodable, Sendable {
    public let hasTrackedChanges: Bool
    public let hasUntrackedFiles: Bool
    public let hasUnpushedCommits: Bool
    public let locked: Bool
    public let prunable: Bool
}

public struct LineChanges: Decodable, Sendable, Equatable {
    public let additions: Int64
    public let deletions: Int64
    public let files: Int

    public init(additions: Int64, deletions: Int64, files: Int) {
        self.additions = additions
        self.deletions = deletions
        self.files = files
    }
}

public struct ServiceLineChanges: Decodable, Identifiable, Sendable, Equatable {
    public var id: String { serviceId }
    public let serviceId: String
    public let committed: LineChanges
    public let uncommitted: LineChanges
}

public struct WorktreeChanges: Decodable, Sendable, Equatable {
    public let baseRevision: String
    public let committed: LineChanges
    public let uncommitted: LineChanges
    public let sharedCommitted: LineChanges
    public let sharedUncommitted: LineChanges
    public let services: [ServiceLineChanges]

    public func service(_ id: String) -> ServiceLineChanges? {
        services.first { $0.serviceId == id }
    }
}

public struct Environment: Decodable, Identifiable, Sendable {
    public let id: String
    public let revision: Int64
    public let repositoryId: String
    public let worktreeId: String
    public let displayName: String
    public let targetId: String?
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
    case failed
    case orphaned
}

public enum ObservedState: String, ForwardCompatibleDecodable {
    case unknown
    case stopped
    case starting
    case running
    case stopping
    case exited
    case failed
    case orphaned
    case degraded
    case unverifiable
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
    public let observationCode: String?
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
    public let sourceRevision: String?
    public let sourceHasTrackedChanges: Bool?
    public let sourceHasUntrackedFiles: Bool?
    public let sourceObservedAt: Date?
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
    public let runId: String?
    public let kind: String
    public let state: OperationState
    public let phase: String?
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
    public let phase: String?
    public let step: String?
    public let diagnostic: String?
    public let logReference: String?
    public let nextAction: String?
    public let exitCode: Int?
}

public struct CleanupScope: Codable, Sendable, Equatable {
    public let kind: String
    public let id: String?

    public static let global = CleanupScope(kind: "global", id: nil)

    public init(kind: String, id: String? = nil) {
        self.kind = kind
        self.id = id
    }
}

public struct CleanupPlanRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let scope: CleanupScope

    public init(scope: CleanupScope = .global) {
        self.schemaVersion = contractSchemaVersion
        self.scope = scope
    }
}

public struct CleanupApplyRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let planId: String
    public let expectedRevision: Int64
    public let candidateIds: [String]

    public init(planId: String, expectedRevision: Int64, candidateIds: [String]) {
        self.schemaVersion = contractSchemaVersion
        self.planId = planId
        self.expectedRevision = expectedRevision
        self.candidateIds = candidateIds
    }
}

public struct CleanupCandidate: Decodable, Sendable, Identifiable, Equatable {
    public let id: String
    public let kind: String
    public let profileKey: String
    public let worktreeId: String
    public let fingerprint: String
    public let bytes: Int64
    public let path: String
}

public struct CleanupProtection: Decodable, Sendable, Identifiable, Equatable {
    public var id: String { "\(kind):\(path)" }
    public let kind: String
    public let path: String
    public let reason: String
    public let profileKey: String?
    public let worktreeId: String?
}

public struct CleanupPlan: Decodable, Sendable, Identifiable, Equatable {
    public let schemaVersion: Int
    public let id: String
    public let revision: Int64
    public let scope: CleanupScope
    public let candidates: [CleanupCandidate]
    public let protected: [CleanupProtection]
    public let createdAt: Date
    public let expiresAt: Date
}

public struct CleanupRemoval: Decodable, Sendable, Equatable {
    public let candidateId: String
    public let removed: Bool
    public let reason: String?
}

public struct CleanupResult: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let planId: String
    public let planRevision: Int64
    public let removals: [CleanupRemoval]
    public let completedAt: Date
}

public struct StartEnvironmentRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let worktreeId: String
    public let targetId: String
    public let confirmedTargetId: String?
    public let serviceIds: [String]

    public init(
        requestId: String,
        idempotencyKey: String,
        expectedEnvironmentRevision: Int64? = nil,
        worktreeId: String,
        targetId: String = "testing",
        confirmedTargetId: String? = nil,
        serviceIds: [String]
    ) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = expectedEnvironmentRevision
        self.worktreeId = worktreeId
        self.targetId = targetId
        self.confirmedTargetId = confirmedTargetId
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

public struct CreateWorktreeRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let repositoryId: String
    public let branch: String
    public let startPoint: String?

    public init(requestId: String, idempotencyKey: String, repositoryId: String, branch: String, startPoint: String? = nil) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = nil
        self.repositoryId = repositoryId
        self.branch = branch
        self.startPoint = startPoint
    }
}

public struct ArchiveWorktreeRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let worktreeId: String

    public init(requestId: String, idempotencyKey: String, worktreeId: String) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = nil
        self.worktreeId = worktreeId
    }
}

public struct AdoptWorktreeRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let worktreeId: String

    public init(requestId: String, idempotencyKey: String, worktreeId: String) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = nil
        self.worktreeId = worktreeId
    }
}

public struct MutationReceipt: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let operationId: String
    public let runId: String?
    public let acceptedAt: Date
    public let environmentId: String?
}

public struct EnvironmentContext: Decodable, Sendable {
    public let revision: Int64
    public let environmentId: String
    public let runId: String?
    public let sourceRevision: String?
    public let sourceDirty: Bool?
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

public struct PrepareWorktreeRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let requestId: String
    public let idempotencyKey: String
    public let expectedEnvironmentRevision: Int64?
    public let worktreeId: String

    public init(requestId: String, idempotencyKey: String, worktreeId: String) {
        self.schemaVersion = contractSchemaVersion
        self.requestId = requestId
        self.idempotencyKey = idempotencyKey
        self.expectedEnvironmentRevision = nil
        self.worktreeId = worktreeId
    }
}

public enum ConfigurationState: String, ForwardCompatibleDecodable {
    case unknown
    case missing
    case accepted
    case pending
}

/// One validated-but-not-yet-accepted private configuration revision.
public struct ConfigurationCandidate: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let digest: String
    public let sourceDigest: String
    public let compilerVersion: String
    public let repositoryDigests: [String: String]
    public let executableDigests: [String: String]
    public let stagedAt: Date
}

/// Daemon-published configuration acceptance state (D-025).
public struct ConfigurationStatus: Decodable, Sendable, Equatable {
    public let schemaVersion: Int
    public let state: ConfigurationState
    public let acceptedRevision: Int64
    public let acceptedDigest: String?
    public let candidate: ConfigurationCandidate?
}

public struct ConfigurationValidationRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let expectedRevision: Int64

    public init(expectedRevision: Int64) {
        self.schemaVersion = contractSchemaVersion
        self.expectedRevision = expectedRevision
    }
}

public struct ConfigurationAcceptanceRequest: Codable, Sendable, Equatable {
    public let schemaVersion: Int
    public let expectedRevision: Int64
    public let digest: String

    public init(expectedRevision: Int64, digest: String) {
        self.schemaVersion = contractSchemaVersion
        self.expectedRevision = expectedRevision
        self.digest = digest
    }
}
