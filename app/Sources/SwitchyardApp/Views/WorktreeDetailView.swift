import SwiftUI
import SwitchyardKit

struct WorktreeDetailView: View {
    @Bindable var model: AppModel
    let repository: Repository
    let worktree: Worktree
    let snapshot: StatusSnapshot

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                header
                Divider()
                EnvironmentActionBanner(model: model)
                StartEnvironmentView(model: model, snapshot: snapshot, initialWorktreeId: worktree.id)
                worktreeFacts
                repositoryFacts
            }
            .padding(28)
            .frame(maxWidth: 1_080, alignment: .leading)
        }
        .frame(maxWidth: .infinity, alignment: .top)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .center, spacing: 12) {
                Image(systemName: "arrow.triangle.branch")
                    .font(.title2)
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 3) {
                    Text(worktree.branch ?? "Detached HEAD")
                        .font(.largeTitle.bold())
                    Text("\(repository.displayName) · \(worktree.path)")
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                Spacer()
                Text("Stopped")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
            }

            HStack(spacing: 0) {
                NativeMetric(value: worktree.isPrimary ? "Primary" : "Linked", label: "Checkout")
                NativeMetric(value: Format.shortRevision(worktree.headRevision), label: "Head")
                NativeMetric(value: worktree.git.isClean ? "Clean" : "Changes", label: "Git state", tint: worktree.git.isClean ? .green : .orange)
                NativeMetric(value: "0", label: "Processes")
                NativeMetric(value: "0 KB", label: "Memory")
            }
            .padding(.vertical, 14)
            .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
    }

    private var worktreeFacts: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Path", value: worktree.path, monospaced: true)
                KeyValueRow(key: "Branch", value: worktree.branch ?? "Detached HEAD", monospaced: true)
                KeyValueRow(key: "Head revision", value: worktree.headRevision, monospaced: true)
                KeyValueRow(key: "Worktree ID", value: worktree.id, monospaced: true)
                KeyValueRow(key: "Primary checkout", value: worktree.isPrimary ? "Yes" : "No")
                Divider()
                GitStateDetail(state: worktree.git)
            }
            .padding(.top, 12)
        } label: {
            Label("Complete worktree state", systemImage: "arrow.triangle.branch")
                .font(.headline)
        }
    }

    private var repositoryFacts: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 7) {
                KeyValueRow(key: "Name", value: repository.displayName)
                KeyValueRow(key: "Root", value: repository.rootPath, monospaced: true)
                KeyValueRow(key: "Remote", value: repository.remote, monospaced: true)
                KeyValueRow(key: "Adapter", value: repository.adapter, monospaced: true)
                KeyValueRow(key: "Repository ID", value: repository.id, monospaced: true)
            }
            .padding(.top, 12)
        } label: {
            Label("Repository identity", systemImage: "externaldrive")
                .font(.headline)
        }
    }
}

struct GitStateDetail: View {
    let state: WorktreeState

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack {
                Text("Git state").foregroundStyle(.secondary)
                Spacer()
                GitStateChips(state: state)
            }
            KeyValueRow(key: "Tracked changes", value: state.hasTrackedChanges ? "Yes" : "No")
            KeyValueRow(key: "Untracked files", value: state.hasUntrackedFiles ? "Yes" : "No")
            KeyValueRow(key: "Unpushed commits", value: state.hasUnpushedCommits ? "Yes" : "No")
            KeyValueRow(key: "Locked", value: state.locked ? "Yes" : "No")
            KeyValueRow(key: "Prunable", value: state.prunable ? "Yes" : "No")
        }
        .font(.callout)
    }
}
