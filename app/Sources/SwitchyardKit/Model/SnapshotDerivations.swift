import Foundation

/// Aggregates the menu bar and overview surfaces render.
public struct StatusSummary: Sendable, Equatable {
    public let environmentCount: Int
    public let runningCount: Int
    public let unhealthyCount: Int
    public let attentionCount: Int
    public let activeAlertCount: Int
    public let totalCPUPercent: Double
    public let totalMemoryBytes: Int64
}

extension StatusSnapshot {
    public var summary: StatusSummary {
        StatusSummary(
            environmentCount: environments.count,
            runningCount: environments.count(where: { $0.observedState == .running }),
            unhealthyCount: environments.count(where: { $0.health == .unhealthy }),
            attentionCount: environments.count(where: { $0.needsAttention }),
            activeAlertCount: activeAlerts.count,
            totalCPUPercent: environments.reduce(0) { $0 + $1.resources.cpuPercent },
            totalMemoryBytes: environments.reduce(0) { $0 + $1.resources.memoryBytes }
        )
    }

    public var activeAlerts: [Alert] {
        alerts.filter { $0.status == .active }
    }

    public func environment(withId id: String) -> Environment? {
        environments.first { $0.id == id }
    }

    public func repository(withId id: String) -> Repository? {
        repositories.first { $0.id == id }
    }

    public func repository(for environment: Environment) -> Repository? {
        repository(withId: environment.repositoryId)
    }

    public func worktree(for environment: Environment) -> Worktree? {
        repository(withId: environment.repositoryId)?
            .worktrees.first { $0.id == environment.worktreeId }
    }

    public func worktree(withId id: String) -> Worktree? {
        repositories.lazy.flatMap(\.worktrees).first { $0.id == id }
    }

    public func environment(for worktree: Worktree) -> Environment? {
        environments.first { $0.worktreeId == worktree.id }
    }

    public func alerts(forEnvironment id: String) -> [Alert] {
        activeAlerts.filter { $0.environmentId == id }
    }

    public func operations(forEnvironment id: String) -> [Operation] {
        operations.filter { $0.environmentId == id }
    }
}

extension Environment {
    public var needsAttention: Bool {
        health == .unhealthy || health == .degraded || !attentionAlertIds.isEmpty
    }

    public func portLease(withId id: String) -> PortLease? {
        portLeases.first { $0.id == id }
    }

    public func portLeases(for service: Service) -> [PortLease] {
        service.portLeaseIds.compactMap(portLease(withId:))
    }

    public var totalProcessCount: Int {
        services.reduce(0) { $0 + ($1.run?.processCount ?? 0) }
    }

    public var hasRetainedResources: Bool {
        !services.isEmpty || !portLeases.isEmpty || !infrastructureLeases.isEmpty
    }

    public var hasUnverifiableServices: Bool {
        services.contains { $0.observedState == .unverifiable }
    }

    public var allowsStopRequest: Bool {
        switch observedState {
        case .running, .failed, .orphaned, .degraded:
            true
        case .unknown:
            hasRetainedResources && hasUnverifiableServices
        case .stopped, .starting, .stopping, .exited, .unverifiable:
            false
        }
    }

    public var allowsRebuildRequest: Bool {
        allowsStopRequest
    }

    /// Sorted so URLs render deterministically.
    public var sortedURLs: [(service: String, url: String)] {
        urls.sorted { $0.key < $1.key }.map { (service: $0.key, url: $0.value) }
    }
}

extension WorktreeState {
    public var isClean: Bool {
        !hasTrackedChanges && !hasUntrackedFiles && !hasUnpushedCommits
    }
}

extension LineChanges {
    public var isEmpty: Bool {
        additions == 0 && deletions == 0 && files == 0
    }
}
