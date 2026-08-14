import SwiftUI
import SwitchyardKit

struct StartEnvironmentView: View {
    private static let availableServiceIds = ["organizer", "nonprofit-service"]

    @Bindable var model: AppModel
    let snapshot: StatusSnapshot
    @State private var selectedWorktreeId = ""
    @State private var selectedServiceIds = Set(availableServiceIds)

    var body: some View {
        SectionCard(title: "Start environment", systemImage: "play.square.stack") {
            if worktrees.isEmpty {
                Text("No discovered worktrees are available.")
                    .foregroundStyle(.secondary)
            } else {
                Picker("Worktree", selection: $selectedWorktreeId) {
                    ForEach(worktrees) { option in
                        Text(option.label).tag(option.id)
                    }
                }
                HStack(spacing: 18) {
                    ForEach(Self.availableServiceIds, id: \.self) { serviceId in
                        Toggle(serviceId, isOn: serviceBinding(serviceId))
                            .toggleStyle(.checkbox)
                    }
                    Spacer()
                    Button {
                        let services = Self.availableServiceIds.filter(selectedServiceIds.contains)
                        Task {
                            await model.startEnvironment(
                                worktreeId: selectedWorktreeId,
                                serviceIds: services
                            )
                        }
                    } label: {
                        Label(
                            model.environmentActionState.isSubmitting ? "Submitting…" : "Start selected",
                            systemImage: "play.fill"
                        )
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(
                        selectedWorktreeId.isEmpty ||
                            selectedServiceIds.isEmpty ||
                            !model.canSubmitEnvironmentAction
                    )
                }
            }
        }
        .onAppear(perform: selectFirstAvailableWorktree)
        .onChange(of: worktrees.map(\.id)) { _, _ in
            selectFirstAvailableWorktree()
        }
    }

    private var worktrees: [WorktreeOption] {
        snapshot.repositories.flatMap { repository in
            repository.worktrees.map { worktree in
                WorktreeOption(
                    id: worktree.id,
                    label: "\(repository.displayName) · \(worktree.branch ?? worktree.path)"
                )
            }
        }
        .sorted { $0.label.localizedStandardCompare($1.label) == .orderedAscending }
    }

    private func serviceBinding(_ serviceId: String) -> Binding<Bool> {
        Binding(
            get: { selectedServiceIds.contains(serviceId) },
            set: { selected in
                if selected {
                    selectedServiceIds.insert(serviceId)
                } else {
                    selectedServiceIds.remove(serviceId)
                }
            }
        )
    }

    private func selectFirstAvailableWorktree() {
        guard !worktrees.contains(where: { $0.id == selectedWorktreeId }) else { return }
        selectedWorktreeId = worktrees.first?.id ?? ""
    }
}

struct EnvironmentActionBanner: View {
    @Bindable var model: AppModel

    @ViewBuilder
    var body: some View {
        switch model.environmentActionState {
        case .idle:
            EmptyView()
        case .submitting(let kind):
            SectionCard(title: "Environment action", systemImage: "arrow.triangle.2.circlepath") {
                ProgressView("Submitting \(kind.rawValue)…")
            }
        case .accepted(let submission):
            SectionCard(title: "Environment action", systemImage: "checkmark.circle") {
                HStack(alignment: .top) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("\(submission.kind.rawValue.capitalized) accepted")
                            .font(.callout.weight(.semibold))
                        Text("Operation \(submission.receipt.operationId)")
                            .font(.caption.monospaced())
                            .textSelection(.enabled)
                        if let operation = model.submittedOperation {
                            Text("\(operation.state.label) · updated \(Format.relative(operation.updatedAt))")
                                .font(.caption)
                                .foregroundStyle(operation.state.tint)
                        } else {
                            Text("Accepted \(Format.relative(submission.receipt.acceptedAt)) · awaiting status snapshot")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    Spacer()
                    Button("Dismiss") { model.dismissEnvironmentAction() }
                        .controlSize(.small)
                }
            }
        case .failed(let kind, let message):
            SectionCard(title: "Environment action", systemImage: "exclamationmark.triangle") {
                HStack(alignment: .top) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("\(kind.rawValue.capitalized) was not submitted")
                            .font(.callout.weight(.semibold))
                        Text(message)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("Dismiss") { model.dismissEnvironmentAction() }
                        .controlSize(.small)
                }
            }
        }
    }
}

private struct WorktreeOption: Identifiable {
    let id: String
    let label: String
}
