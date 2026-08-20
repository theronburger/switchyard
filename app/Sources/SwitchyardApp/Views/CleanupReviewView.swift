import SwiftUI
import SwitchyardKit

struct CleanupReviewSheet: View {
    @Bindable var model: AppModel
    @Binding var isPresented: Bool
    @State private var selected = Set<String>()

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            header
            Divider()
            content
            Divider()
            footer
        }
        .padding(22)
        .frame(minWidth: 680, idealWidth: 760, minHeight: 460, idealHeight: 560)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Review cleanup")
                .font(.title2.bold())
            Text("Only resources with verified Switchyard ownership can be selected. Protected and changed resources remain untouched.")
                .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var content: some View {
        switch model.cleanupActionState {
        case .idle, .planning:
            VStack(spacing: 12) {
                ProgressView()
                Text("Building a fresh cleanup plan…")
                    .foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        case .review(let plan), .applying(let plan):
            planView(plan)
                .task(id: plan.id) {
                    if selected.isEmpty {
                        selected = Set(plan.candidates.map(\.id))
                    }
                }
        case .completed(let result):
            ContentUnavailableView(
                "Cleanup complete",
                systemImage: "checkmark.circle",
                description: Text("Removed \(result.removals.filter(\.removed).count) of \(result.removals.count) selected resources.")
            )
        case .failed(let message):
            ContentUnavailableView(
                "Cleanup could not continue",
                systemImage: "exclamationmark.triangle",
                description: Text(message)
            )
        }
    }

    private func planView(_ plan: CleanupPlan) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                if plan.candidates.isEmpty {
                    ContentUnavailableView(
                        "Nothing to remove",
                        systemImage: "sparkles",
                        description: Text("No stale positively owned resources were found.")
                    )
                } else {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Removable")
                            .font(.headline)
                        ForEach(plan.candidates) { candidate in
                            Toggle(isOn: selectionBinding(candidate.id)) {
                                HStack(alignment: .firstTextBaseline) {
                                    VStack(alignment: .leading, spacing: 3) {
                                        Text(candidate.path)
                                            .font(.callout.monospaced())
                                            .textSelection(.enabled)
                                        Text("\(candidate.profileKey) · \(candidate.worktreeId)")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                    Text(ByteCountFormatter.string(fromByteCount: candidate.bytes, countStyle: .file))
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .toggleStyle(.checkbox)
                            .padding(10)
                            .background(.secondary.opacity(0.07), in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                }
                if !plan.protected.isEmpty {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Protected")
                            .font(.headline)
                        ForEach(plan.protected) { item in
                            HStack(alignment: .firstTextBaseline) {
                                Image(systemName: "lock.fill")
                                    .foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(item.path)
                                        .font(.callout.monospaced())
                                        .textSelection(.enabled)
                                    Text(protectionLabel(item.reason))
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                            .padding(10)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(.secondary.opacity(0.04), in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                }
            }
        }
    }

    private var footer: some View {
        HStack {
            if case .review(let plan) = model.cleanupActionState {
                Text("Plan expires \(plan.expiresAt.formatted(date: .omitted, time: .shortened))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            Button("Close") { isPresented = false }
            switch model.cleanupActionState {
            case .review:
                Button("Remove selected") {
                    Task { await model.applyCleanup(candidateIds: selected) }
                }
                .buttonStyle(.borderedProminent)
                .disabled(selected.isEmpty)
            case .failed:
                Button("Review again") {
                    Task { await model.planCleanup() }
                }
            case .applying:
                ProgressView()
                    .controlSize(.small)
            default:
                EmptyView()
            }
        }
    }

    private func selectionBinding(_ id: String) -> Binding<Bool> {
        Binding(
            get: { selected.contains(id) },
            set: { enabled in
                if enabled { selected.insert(id) } else { selected.remove(id) }
            }
        )
    }

    private func protectionLabel(_ reason: String) -> String {
        switch reason {
        case "current": "Current workspace preparation"
        case "unverified": "Ownership could not be verified"
        case "foreign-or-modified": "Contains foreign or modified content"
        default: "Protected"
        }
    }
}
