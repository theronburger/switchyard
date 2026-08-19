import SwiftUI
import SwitchyardKit

struct GitHubPullRequestView: View {
    let worktree: Worktree
    @State private var checksExpanded = false
    @State private var detailsExpanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Image(systemName: "arrow.triangle.branch").foregroundStyle(.secondary)
                Text("GitHub pull request").font(.headline)
                Spacer()
                observationStatus
            }

            switch worktree.pullRequest?.status {
            case .some(.found):
                if let observation = worktree.pullRequest, let pullRequest = observation.pullRequest {
                    foundContent(observation: observation, pullRequest: pullRequest)
                }
            case .some(.none):
                Label("No pull request found for this branch", systemImage: "minus.circle")
                    .foregroundStyle(.secondary)
            case .some(.unavailable):
                unavailableContent
            case .some(.unknown), nil:
                Label("Waiting for the first GitHub check", systemImage: "clock")
                    .foregroundStyle(.secondary)
            }
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    @ViewBuilder
    private var observationStatus: some View {
        if let observation = worktree.pullRequest {
            if observation.stale {
                PRBadge(label: "Last known", systemImage: "clock.badge.exclamationmark", tint: .orange)
            } else if observation.status == .unavailable {
                PRBadge(label: "Unavailable", systemImage: "exclamationmark.triangle", tint: .orange)
            } else {
                Text("Checked \(Format.relative(observation.lastAttemptAt))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func foundContent(observation: PullRequestObservation, pullRequest: PullRequest) -> some View {
        HStack(alignment: .top, spacing: 14) {
            VStack(alignment: .leading, spacing: 5) {
                HStack(spacing: 8) {
                    if let url = URL(string: pullRequest.url) {
                        Link(destination: url) {
                            Text(verbatim: "#\(pullRequest.number)")
                                .font(.callout.monospaced().weight(.semibold))
                        }
                    }
                    Text(pullRequest.title)
                        .font(.title3.weight(.semibold))
                        .textSelection(.enabled)
                }
                Text("\(pullRequest.headBranch) → \(pullRequest.baseBranch)")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            Spacer()
            PullRequestStateBadge(pullRequest: pullRequest)
        }

        HStack(spacing: 8) {
            PRBadge(label: pullRequest.checks.statusLabel, systemImage: pullRequest.checks.statusImage, tint: pullRequest.checks.statusTint)
            PRBadge(label: pullRequest.mergeStatusLabel, systemImage: "arrow.triangle.merge", tint: pullRequest.mergeStatusTint)
            PRBadge(label: pullRequest.reviewDecision.statusLabel, systemImage: "person.2", tint: pullRequest.reviewDecision.statusTint)
            if pullRequest.headRevision != worktree.headRevision {
                PRBadge(label: "Local HEAD differs", systemImage: "arrow.triangle.2.circlepath", tint: .orange)
                    .help("Remote \(pullRequest.headRevision); local \(worktree.headRevision).")
            }
            Spacer()
        }

        if observation.stale {
            Label("Showing the last successful result. \(githubErrorDescription(observation.errorCode))", systemImage: "exclamationmark.arrow.triangle.2.circlepath")
                .font(.callout)
                .foregroundStyle(.orange)
        }

        FullWidthDisclosure(isExpanded: $checksExpanded) {
            HStack(spacing: 8) {
                Text("Checks").font(.callout.weight(.semibold))
                Text("\(pullRequest.checks.total)")
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(.quaternary, in: Capsule())
                Text(checkCountSummary(pullRequest.checks)).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 0) {
                if pullRequest.checks.items.isEmpty {
                    Text(pullRequest.checks.state == .unavailable ? "Check details are temporarily unavailable." : "No checks reported.")
                        .font(.callout).foregroundStyle(.secondary).padding(.vertical, 10)
                }
                ForEach(Array(pullRequest.checks.items.enumerated()), id: \.offset) { index, check in
                    PRCheckRow(check: check)
                    if index < pullRequest.checks.items.count - 1 { Divider() }
                }
            }
            .padding(.top, 8)
        }

        FullWidthDisclosure(isExpanded: $detailsExpanded) {
            Text("Complete pull request state").font(.callout.weight(.semibold))
            Spacer()
        } content: {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Number", value: "#\(pullRequest.number)", monospaced: true, copyable: true)
                KeyValueRow(key: "State", value: pullRequest.primaryStateLabel)
                KeyValueRow(key: "Merge state", value: pullRequest.mergeState.rawValue)
                KeyValueRow(key: "Mergeable", value: pullRequest.mergeable.rawValue)
                KeyValueRow(key: "Review", value: pullRequest.reviewDecision.rawValue)
                KeyValueRow(key: "Base", value: pullRequest.baseBranch, monospaced: true, copyable: true)
                KeyValueRow(key: "Remote head", value: pullRequest.headBranch, monospaced: true, copyable: true)
                KeyValueRow(key: "Remote revision", value: pullRequest.headRevision, monospaced: true, copyable: true)
                KeyValueRow(key: "Created", value: pullRequest.createdAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Updated", value: pullRequest.updatedAt.formatted(date: .abbreviated, time: .standard))
                if let mergedAt = pullRequest.mergedAt { KeyValueRow(key: "Merged", value: mergedAt.formatted(date: .abbreviated, time: .standard)) }
                if let closedAt = pullRequest.closedAt { KeyValueRow(key: "Closed", value: closedAt.formatted(date: .abbreviated, time: .standard)) }
                if let account = observation.account { KeyValueRow(key: "GitHub account", value: account, monospaced: true) }
                if let observedAt = observation.observedAt { KeyValueRow(key: "Last successful check", value: observedAt.formatted(date: .abbreviated, time: .standard)) }
            }
            .padding(.top, 8)
        }
    }

    private var unavailableContent: some View {
        VStack(alignment: .leading, spacing: 5) {
            Label(githubErrorDescription(worktree.pullRequest?.errorCode), systemImage: "exclamationmark.triangle")
                .foregroundStyle(.orange)
            Text("Environment health remains independent from GitHub; Switchyard will retry automatically.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private func checkCountSummary(_ checks: PullRequestChecks) -> String {
        [checks.passing > 0 ? "\(checks.passing) passed" : nil,
         checks.failing > 0 ? "\(checks.failing) failed" : nil,
         checks.pending > 0 ? "\(checks.pending) pending" : nil,
         checks.cancelled > 0 ? "\(checks.cancelled) cancelled" : nil,
         checks.skipping > 0 ? "\(checks.skipping) skipped" : nil]
            .compactMap { $0 }.joined(separator: " · ")
    }
}

struct PullRequestCompactStatus: View {
    let observation: PullRequestObservation?
    var body: some View {
        if let observation, observation.status == .found, let pullRequest = observation.pullRequest {
            HStack(spacing: 4) {
                GitHubPullRequestStateIcon(
                    state: pullRequest.state,
                    draft: pullRequest.draft,
                    tint: observation.stale ? .orange : pullRequest.checks.statusTint
                )
                Text(verbatim: "#\(pullRequest.number)")
                    .foregroundStyle(.secondary)
            }
            .font(.caption2.weight(.semibold))
            .help("GitHub PR #\(pullRequest.number): \(pullRequest.primaryStateLabel) · \(pullRequest.checks.statusLabel)")
        } else if let observation, observation.status == .unavailable {
            Image(systemName: "exclamationmark.triangle")
                .font(.caption2).foregroundStyle(.orange)
                .help(githubErrorDescription(observation.errorCode))
        }
    }
}

private struct PRCheckRow: View {
    let check: PullRequestCheck
    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: check.bucket.statusImage).foregroundStyle(check.bucket.statusTint).frame(width: 16)
            VStack(alignment: .leading, spacing: 1) {
                Text(check.name).font(.callout.weight(.medium))
                Text([check.workflow, check.state.replacingOccurrences(of: "_", with: " ").capitalized].filter { !$0.isEmpty }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if let completedAt = check.completedAt { Text(Format.relative(completedAt)).font(.caption).foregroundStyle(.secondary) }
            if let url = URL(string: check.url), !check.url.isEmpty {
                Link(destination: url) { Image(systemName: "arrow.up.right.square") }.help("Open check on GitHub")
            }
        }
        .padding(.vertical, 7)
        .help(checkTimingDescription)
    }

    private var checkTimingDescription: String {
        let started = check.startedAt?.formatted(date: .abbreviated, time: .standard) ?? "not reported"
        let completed = check.completedAt?.formatted(date: .abbreviated, time: .standard) ?? "not completed"
        return "Started: \(started)\nCompleted: \(completed)"
    }
}

private struct PRBadge: View {
    let label: String
    let systemImage: String
    let tint: Color
    var body: some View {
        Label(label, systemImage: systemImage)
            .font(.caption.weight(.semibold)).foregroundStyle(tint)
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(tint.opacity(0.12), in: Capsule())
    }
}

private struct PullRequestStateBadge: View {
    let pullRequest: PullRequest

    var body: some View {
        HStack(spacing: 5) {
            GitHubPullRequestStateIcon(
                state: pullRequest.state,
                draft: pullRequest.draft,
                tint: pullRequest.primaryStateTint
            )
            Text(pullRequest.primaryStateLabel)
        }
        .font(.caption.weight(.semibold))
        .foregroundStyle(pullRequest.primaryStateTint)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background(pullRequest.primaryStateTint.opacity(0.12), in: Capsule())
    }
}

func githubErrorDescription(_ code: String?) -> String {
    switch code {
    case "github_cli_unavailable": return "GitHub CLI is not installed or cannot be used."
    case "github_auth_unavailable": return "GitHub CLI is not authenticated for github.com."
    case "github_timeout": return "GitHub did not respond before the local timeout."
    case "github_response_invalid": return "GitHub CLI returned an unexpected response."
    default: return "GitHub status is temporarily unavailable."
    }
}

private extension PullRequest {
    var primaryStateLabel: String { state == .merged ? "Merged" : state == .closed ? "Closed" : draft ? "Draft" : "Ready" }
    var primaryStateTint: Color { state == .merged ? .purple : state == .closed || draft ? .secondary : .green }
    var mergeStatusLabel: String {
        switch mergeable {
        case .mergeable: return mergeState == .blocked ? "Merge blocked" : "Mergeable"
        case .conflicting: return "Conflicts"
        case .notApplicable: return "Not applicable"
        case .unknown: return "Merge unknown"
        }
    }
    var mergeStatusTint: Color { mergeable == .conflicting ? .red : mergeable == .mergeable ? (mergeState == .blocked ? .orange : .green) : .secondary }
}

private extension PullRequestChecks {
    var statusLabel: String {
        switch state {
        case .passing: return "CI passing"
        case .failing: return "CI failing"
        case .pending: return "CI pending"
        case .cancelled: return "CI cancelled"
        case .neutral: return "CI neutral"
        case .none: return "No checks"
        case .unavailable: return "Checks unavailable"
        case .unknown: return "CI unknown"
        }
    }
    var statusImage: String { state == .passing ? "checkmark.circle.fill" : state == .failing ? "xmark.circle.fill" : state == .pending ? "clock.fill" : "questionmark.circle" }
    var statusTint: Color { state == .passing ? .green : state == .failing ? .red : state == .pending ? .orange : .secondary }
}

private extension PullRequestReviewDecision {
    var statusLabel: String {
        switch self {
        case .approved: return "Approved"
        case .changesRequested: return "Changes requested"
        case .reviewRequired: return "Review required"
        case .notApplicable: return "No review"
        case .unknown: return "Review unknown"
        }
    }
    var statusTint: Color { self == .approved ? .green : self == .changesRequested ? .red : self == .reviewRequired ? .orange : .secondary }
}

private extension PullRequestCheckBucket {
    var statusImage: String {
        switch self {
        case .pass: return "checkmark.circle.fill"
        case .fail: return "xmark.circle.fill"
        case .pending: return "clock.fill"
        case .cancel: return "minus.circle.fill"
        case .skipping: return "arrow.right.circle"
        case .unknown: return "questionmark.circle"
        }
    }
    var statusTint: Color { self == .pass ? .green : self == .fail ? .red : self == .pending ? .orange : .secondary }
}
