import AppKit
import Foundation
import SwiftUI
import SwitchyardKit

struct JiraIssueReference: Equatable {
    let key: String
    let url: URL

    static func detect(in branch: String?) -> JiraIssueReference? {
        guard let branch,
              let expression = try? NSRegularExpression(pattern: #"(?i)\bDEMO-[0-9]+\b"#),
              let match = expression.firstMatch(
                in: branch,
                range: NSRange(branch.startIndex..<branch.endIndex, in: branch)
              ),
              let range = Range(match.range, in: branch) else { return nil }
        return make(key: String(branch[range]))
    }

    static func resolve(branch: String?, override: String) -> JiraIssueReference? {
        if let key = normalizedKey(override) {
            return make(key: key)
        }
        return detect(in: branch)
    }

    static func normalizedKey(_ candidate: String) -> String? {
        let key = candidate.trimmingCharacters(in: .whitespacesAndNewlines).uppercased()
        guard key.range(of: #"^DEMO-[0-9]+$"#, options: .regularExpression) != nil else { return nil }
        return key
    }

    static func overrideStorageKey(worktreeId: String) -> String {
        "switchyard.jira-override.\(worktreeId)"
    }

    private static func make(key candidate: String) -> JiraIssueReference? {
        guard let key = normalizedKey(candidate),
              let url = URL(string: "https://example.atlassian.net/browse/\(key)") else { return nil }
        return JiraIssueReference(key: key, url: url)
    }
}

struct JiraIssueView: View {
    let worktree: Worktree
    var loadsLiveData: Bool
    @AppStorage private var ticketOverride: String
    @State private var state: JiraIssueState = .idle
    @State private var retry = 0
    @State private var isEditingOverride = false
    @State private var overrideDraft = ""
    @State private var overrideValidationMessage: String?

    init(worktree: Worktree, loadsLiveData: Bool = true) {
        self.worktree = worktree
        self.loadsLiveData = loadsLiveData
        _ticketOverride = AppStorage(
            wrappedValue: "",
            JiraIssueReference.overrideStorageKey(worktreeId: worktree.id)
        )
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 9) {
                Image(systemName: "checklist")
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Jira")
                        .font(.headline)
                    if let issue {
                        Link(issue.key, destination: issue.url)
                            .font(.caption.monospaced())
                            .foregroundStyle(.secondary)
                            .tint(.secondary)
                    } else {
                        Text("No ticket detected")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer()
                stateBadge
                Button {
                    overrideDraft = ticketOverride.isEmpty ? (issue?.key ?? "") : ticketOverride
                    overrideValidationMessage = nil
                    isEditingOverride.toggle()
                } label: {
                    Label("Override ticket", systemImage: "pencil")
                }
                .controlSize(.small)
                if let issue {
                    Link(destination: issue.url) {
                        Label("Open ticket", systemImage: "arrow.up.right.square")
                    }
                }
            }

            if isEditingOverride {
                overrideEditor
            }

            stateContent
        }
        .padding(16)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .task(id: "\(issue?.key ?? "none")-\(retry)") {
            guard loadsLiveData, let issue else {
                state = .idle
                return
            }
            state = .loading
            do {
                state = .loaded(try await JiraIssueStore.live.issue(key: issue.key, refresh: retry > 0))
            } catch {
                state = .failed
            }
        }
    }

    private var issue: JiraIssueReference? {
        JiraIssueReference.resolve(branch: worktree.branch, override: ticketOverride)
    }

    private var overrideEditor: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                TextField("DEMO-830", text: $overrideDraft)
                    .textFieldStyle(.roundedBorder)
                    .frame(maxWidth: 180)
                    .onSubmit(saveOverride)
                Button("Save", action: saveOverride)
                    .buttonStyle(.borderedProminent)
                if !ticketOverride.isEmpty {
                    Button("Use detected") {
                        ticketOverride = ""
                        isEditingOverride = false
                        overrideValidationMessage = nil
                    }
                }
                Button("Cancel") {
                    isEditingOverride = false
                    overrideValidationMessage = nil
                }
            }
            if let overrideValidationMessage {
                Text(overrideValidationMessage)
                    .font(.caption)
                    .foregroundStyle(.red)
            } else {
                Text("Overrides only this worktree; leave blank to use the ticket detected from its branch.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func saveOverride() {
        let trimmed = overrideDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty {
            ticketOverride = ""
            isEditingOverride = false
            overrideValidationMessage = nil
            return
        }
        guard let normalized = JiraIssueReference.normalizedKey(trimmed) else {
            overrideValidationMessage = "Enter a ticket like DEMO-830."
            return
        }
        ticketOverride = normalized
        isEditingOverride = false
        overrideValidationMessage = nil
    }

    @ViewBuilder
    private var stateBadge: some View {
        switch state {
        case .loaded(let summary):
            Text(summary.status)
                .font(.caption2.weight(.medium))
                .padding(.horizontal, 7)
                .padding(.vertical, 3)
                .background(.orange.opacity(0.14), in: Capsule())
                .foregroundStyle(.orange)
        case .loading:
            ProgressView()
                .controlSize(.small)
                .accessibilityLabel("Loading Jira issue")
        case .idle:
            if issue != nil, !loadsLiveData {
                Text("Fixture")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        case .failed:
            Text("Unavailable")
                .font(.caption2.weight(.medium))
                .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var stateContent: some View {
        switch state {
        case .loaded(let summary):
            VStack(alignment: .leading, spacing: 9) {
                Text(summary.summary)
                    .font(.title3.weight(.semibold))
                HStack(spacing: 16) {
                    Label(summary.assignee ?? "Unassigned", systemImage: "person")
                    Label(summary.priority ?? "No priority", systemImage: "flag")
                    Label {
                        Text(summary.updated, style: .relative)
                    } icon: {
                        Image(systemName: "clock")
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        case .loading:
            Text("Loading ticket metadata…")
                .font(.caption)
                .foregroundStyle(.secondary)
        case .idle:
            if issue == nil {
                Text("Choose Override ticket to associate this worktree with Jira.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if !loadsLiveData {
                Text("Live ticket metadata is not loaded in fixture previews.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        case .failed:
            HStack {
                Text("The read-only Jira relay could not load this ticket.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Retry") { retry += 1 }
                    .controlSize(.small)
            }
        }
    }
}

private enum JiraIssueState {
    case idle
    case loading
    case loaded(JiraIssueSummary)
    case failed
}

struct JiraIssueBadge: View {
    let worktree: Worktree
    @AppStorage private var ticketOverride: String

    init(worktree: Worktree) {
        self.worktree = worktree
        _ticketOverride = AppStorage(
            wrappedValue: "",
            JiraIssueReference.overrideStorageKey(worktreeId: worktree.id)
        )
    }

    var body: some View {
        if let issue = JiraIssueReference.resolve(branch: worktree.branch, override: ticketOverride) {
            Link(issue.key, destination: issue.url)
                .font(.caption2.monospaced().weight(.semibold))
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(.quaternary, in: Capsule())
                .foregroundStyle(.secondary)
                .tint(.secondary)
                .help("Open \(issue.key) in Jira")
        }
    }
}

struct JiraIssueCompactStatus: View {
    let worktree: Worktree
    let loadsLiveData: Bool
    @AppStorage private var ticketOverride: String
    @State private var state: JiraIssueState = .idle

    init(worktree: Worktree, loadsLiveData: Bool) {
        self.worktree = worktree
        self.loadsLiveData = loadsLiveData
        _ticketOverride = AppStorage(
            wrappedValue: "",
            JiraIssueReference.overrideStorageKey(worktreeId: worktree.id)
        )
    }

    var body: some View {
        if let issue {
            HStack(spacing: 5) {
                Link(issue.key, destination: issue.url)
                    .font(.caption2.monospaced().weight(.medium))
                    .foregroundStyle(.secondary)
                    .tint(.secondary)
                if case .loaded(let summary) = state {
                    Text(summary.status)
                        .font(.system(size: 9, weight: .semibold))
                        .lineLimit(1)
                        .padding(.horizontal, 5)
                        .padding(.vertical, 2)
                        .background(.quaternary, in: Capsule())
                        .foregroundStyle(.secondary)
                }
            }
            .task(id: issue.key) {
                guard loadsLiveData else { return }
                state = .loading
                do {
                    state = .loaded(try await JiraIssueStore.live.issue(key: issue.key))
                } catch {
                    state = .failed
                }
            }
        }
    }

    private var issue: JiraIssueReference? {
        JiraIssueReference.resolve(branch: worktree.branch, override: ticketOverride)
    }
}
