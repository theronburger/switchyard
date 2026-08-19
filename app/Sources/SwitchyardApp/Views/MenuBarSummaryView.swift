import AppKit
import SwiftUI
import SwitchyardKit

struct MenuBarSummaryView: View {
    @Bindable var model: AppModel
    @SwiftUI.Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().padding(.vertical, 10)
            content
            Divider().padding(.vertical, 10)
            actions
        }
        .padding(14)
        .frame(width: 420)
        .task { model.startPolling() }
    }

    private var header: some View {
        HStack(spacing: 10) {
            Image(systemName: model.lifecycleState.systemImage)
                .font(.title3)
                .foregroundStyle(model.lifecycleState.tint)
                .symbolEffect(.rotate, isActive: model.lifecycleState == .repairing)
            VStack(alignment: .leading, spacing: 1) {
                Text("Switchyard")
                    .font(.headline)
                Text("\(model.lifecycleState.displayName) · \(model.dataSourceDescription)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if let refreshed = model.lastRefreshedAt {
                Text("Updated \(Format.relative(refreshed))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.phase {
        case .idle, .loading:
            ProgressView("Loading status…")
                .controlSize(.small)
                .frame(maxWidth: .infinity, minHeight: 88, alignment: .center)
        case .failed(let message):
            VStack(alignment: .leading, spacing: 6) {
                Label("Daemon unavailable", systemImage: "bolt.horizontal.circle")
                    .foregroundStyle(.orange)
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(4)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        case .empty:
            Text("No repositories or worktrees are currently reported.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 72, alignment: .leading)
        case .loaded:
            loadedContent
        }
    }

    private var loadedContent: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let summary = model.summary, let snapshot = model.snapshot {
                HStack {
                    MenuMetric(value: "\(summary.runningCount)", label: "Running")
                    Spacer()
                    MenuMetric(value: "\(summary.attentionCount)", label: "Attention")
                    Spacer()
                    MenuMetric(value: "\(snapshot.environments.reduce(0) { $0 + $1.totalProcessCount })", label: "Processes")
                    Spacer()
                    MenuMetric(value: Format.memory(summary.totalMemoryBytes), label: "Memory", alignment: .trailing)
                }
                Divider()
                worktreeList(snapshot)
            }
        }
    }

    private func worktreeList(_ snapshot: StatusSnapshot) -> some View {
        let worktreeCount = snapshot.repositories.reduce(0) { $0 + $1.worktrees.count }
        return ScrollView {
            VStack(spacing: 3) {
                ForEach(snapshot.repositories) { repository in
                    ForEach(repository.worktrees) { worktree in
                        let environment = snapshot.environment(for: worktree)
                        Button {
                            if let environment {
                                show(.environment(environment.id))
                            } else {
                                show(.worktree(repositoryId: repository.id, worktreeId: worktree.id))
                            }
                        } label: {
                            MenuWorktreeRow(
                                repository: repository,
                                worktree: worktree,
                                environment: environment,
                                alertCount: environment.map { snapshot.alerts(forEnvironment: $0.id).count } ?? 0,
                                selected: isSelected(repository: repository, worktree: worktree, environment: environment)
                            )
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .switchyardScrollbars()
        }
        .frame(height: min(CGFloat(worktreeCount) * 57, 430))
    }

    private var actions: some View {
        VStack(spacing: 9) {
            Button {
                show(model.selection ?? .overview)
            } label: {
                Label("Open Switchyard", systemImage: "macwindow")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)

            HStack(spacing: 8) {
                Button {
                    show(.connectionDoctor)
                } label: {
                    Label("Doctor", systemImage: "stethoscope")
                        .frame(maxWidth: .infinity)
                }
                Button {
                    Task { await model.refresh() }
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                        .frame(maxWidth: .infinity)
                }
                if model.canRepairAllConnections {
                    Button {
                        Task { await model.repairAll() }
                    } label: {
                        Label("Repair", systemImage: "wrench.and.screwdriver")
                            .frame(maxWidth: .infinity)
                    }
                }
                Button {
                    NSApplication.shared.terminate(nil)
                } label: {
                    Label("Quit", systemImage: "power")
                        .frame(maxWidth: .infinity)
                }
            }
            .buttonStyle(.bordered)
        }
    }

    private func show(_ selection: SidebarSelection) {
        model.selection = selection
        CommandCenterWindowPresenter.open(using: openWindow)
    }

    private func isSelected(
        repository: Repository,
        worktree: Worktree,
        environment: EnvironmentModel?
    ) -> Bool {
        if let environment, model.selection == .environment(environment.id) { return true }
        return model.selection == .worktree(repositoryId: repository.id, worktreeId: worktree.id)
    }
}

private struct MenuMetric: View {
    let value: String
    let label: String
    var alignment: HorizontalAlignment = .leading

    var body: some View {
        VStack(alignment: alignment, spacing: 1) {
            Text(value)
                .font(.title3.weight(.semibold))
                .monospacedDigit()
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}

private struct MenuWorktreeRow: View {
    let repository: Repository
    let worktree: Worktree
    let environment: EnvironmentModel?
    let alertCount: Int
    let selected: Bool

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            StatusDot(color: environment?.health.tint ?? .secondary)
                .padding(.top, 5)
            VStack(alignment: .leading, spacing: 3) {
                Text(worktree.branch ?? URL(fileURLWithPath: worktree.path).lastPathComponent)
                    .font(.callout.weight(.medium))
                    .lineLimit(1)
                Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 3) {
                Text(environment?.observedState.label ?? "Stopped")
                    .font(.caption)
                    .foregroundStyle(environment?.observedState.tint ?? .secondary)
                if alertCount > 0 {
                    Label("\(alertCount)", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.orange)
                }
                PullRequestCompactStatus(observation: worktree.pullRequest)
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 9)
        .background(background, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    private var detail: String {
        guard let environment else {
            return "\(repository.displayName) · no owned resources"
        }
        return "\(environment.totalProcessCount) processes · \(Format.memory(environment.resources.memoryBytes))"
    }

    private var background: Color {
        guard selected else { return .clear }
        return Color.accentColor.opacity(0.08)
    }
}
