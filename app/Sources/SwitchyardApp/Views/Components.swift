import AppKit
import SwiftUI
import SwitchyardKit

private final class SwitchyardScrollConfigurationView: NSView {
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        refresh()
    }

    override func viewDidMoveToSuperview() {
        super.viewDidMoveToSuperview()
        refresh()
    }

    func refresh() {
        DispatchQueue.main.async { [weak self] in
            guard let scrollView = self?.enclosingScrollView else { return }
            scrollView.scrollerStyle = .overlay
            scrollView.autohidesScrollers = true
            scrollView.verticalScroller?.controlSize = .small
            scrollView.horizontalScroller?.controlSize = .small
            scrollView.verticalScroller?.knobStyle = .default
            scrollView.horizontalScroller?.knobStyle = .default
        }
    }
}

private struct SwitchyardScrollConfiguration: NSViewRepresentable {
    func makeNSView(context: Context) -> NSView {
        SwitchyardScrollConfigurationView(frame: .zero)
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        guard let configurationView = nsView as? SwitchyardScrollConfigurationView else { return }
        configurationView.refresh()
    }
}

extension View {
    /// Applies native small overlay scrollers: light, unobtrusive, and hidden while idle.
    func switchyardScrollbars() -> some View {
        background(SwitchyardScrollConfiguration())
    }
}

/// Rounded card container used across the command center.
struct SectionCard<Content: View>: View {
    let title: String
    let systemImage: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Label(title, systemImage: systemImage)
                .font(.headline)
                .foregroundStyle(.secondary)
            content
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}

struct HealthBadge: View {
    let health: Health

    var body: some View {
        Text(health.label)
            .font(.caption.weight(.semibold))
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(health.tint.opacity(0.16), in: Capsule())
            .foregroundStyle(health.tint)
    }
}

struct StatusDot: View {
    let color: Color

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 8, height: 8)
    }
}

struct StatChip: View {
    let value: String
    let label: String
    var tint: Color = .primary

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(value)
                .font(.title3.weight(.semibold))
                .foregroundStyle(tint)
                .monospacedDigit()
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

struct NativeMetric: View {
    let value: String
    let label: String
    var tint: Color = .primary
    var showsDivider = true

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(.title3.weight(.semibold))
                .foregroundStyle(tint)
                .monospacedDigit()
                .lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14)
        .overlay(alignment: .trailing) {
            if showsDivider {
                VerticalSeparator()
            }
        }
    }
}

struct LineChangeMetric: View {
    let changes: LineChanges?
    let label: String
    let systemImage: String
    var showsDivider = true

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Label(label, systemImage: systemImage)
                .font(.caption)
                .foregroundStyle(.secondary)
            if let changes {
                HStack(spacing: 5) {
                    Text("+\(changes.additions)")
                        .foregroundStyle(.green)
                    Text("−\(changes.deletions)")
                        .foregroundStyle(.red)
                }
                .font(.title3.weight(.semibold))
                .monospacedDigit()
                .fixedSize(horizontal: true, vertical: false)
            } else {
                Text("Unavailable")
                    .font(.title3.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
        }
        .frame(minWidth: 142, maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14)
        .overlay(alignment: .trailing) {
            if showsDivider {
                VerticalSeparator()
            }
        }
    }
}

struct NativeFact: View {
    let label: String
    let value: String
    var monospaced = false
    var tint: Color = .primary
    var showsDivider = true
    var copyable = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack(spacing: 6) {
                Text(value)
                    .font(monospaced ? .callout.monospaced() : .callout)
                    .foregroundStyle(tint)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .help(value)
                if copyable {
                    CopyValueButton(value: value, label: label)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .overlay(alignment: .trailing) {
            if showsDivider {
                VerticalSeparator()
            }
        }
    }
}

private struct VerticalSeparator: View {
    var body: some View {
        Rectangle()
            .fill(.separator)
            .frame(width: 1)
            .padding(.vertical, 2)
    }
}

struct FullWidthDisclosure<Label: View, Content: View>: View {
    @Binding var isExpanded: Bool
    @ViewBuilder let label: Label
    @ViewBuilder let content: Content

    init(
        isExpanded: Binding<Bool>,
        @ViewBuilder label: () -> Label,
        @ViewBuilder content: () -> Content
    ) {
        _isExpanded = isExpanded
        self.label = label()
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.14)) {
                    isExpanded.toggle()
                }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .rotationEffect(.degrees(isExpanded ? 90 : 0))
                    label
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            if isExpanded {
                content
            }
        }
    }
}

struct OperationTable: View {
    let operations: [OperationModel]
    let snapshot: StatusSnapshot

    private var newestFirst: [OperationModel] {
        newestFirstOperations(operations)
    }

    var body: some View {
        if operations.isEmpty {
            Text("No recorded operations.")
                .foregroundStyle(.secondary)
        } else {
            ScrollView(.horizontal) {
                VStack(spacing: 0) {
                    operationHeader
                    Divider()
                    ForEach(newestFirst) { operation in
                        OperationTableRow(operation: operation, snapshot: snapshot)
                        if operation.id != newestFirst.last?.id {
                            Divider()
                        }
                    }
                }
                .frame(minWidth: 760)
            }
            .scrollIndicators(.hidden)
        }
    }

    private var operationHeader: some View {
        HStack(spacing: 10) {
            Text("")
                .frame(width: 18)
            tableHeader("Updated", width: 124)
            tableHeader("Repository", width: 94)
            tableHeader("Worktree", width: 210)
            tableHeader("Type", width: 164)
            tableHeader("State", width: 86, alignment: .trailing)
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
    }

    private func tableHeader(
        _ value: String,
        width: CGFloat,
        alignment: Alignment = .leading
    ) -> some View {
        Text(value)
            .font(.caption2.weight(.semibold))
            .foregroundStyle(.secondary)
            .frame(width: width, alignment: alignment)
    }
}

func newestFirstOperations(_ operations: [OperationModel]) -> [OperationModel] {
    operations.sorted {
        if $0.updatedAt != $1.updatedAt { return $0.updatedAt > $1.updatedAt }
        if $0.createdAt != $1.createdAt { return $0.createdAt > $1.createdAt }
        return $0.id > $1.id
    }
}

struct OperationRowPresentation {
    let operation: OperationModel
    let environment: EnvironmentModel?
    let repositoryName: String
    let worktreeName: String

    init(operation: OperationModel, snapshot: StatusSnapshot) {
        self.operation = operation
        environment = operation.environmentId.flatMap(snapshot.environment(withId:))
        repositoryName = environment.flatMap(snapshot.repository(for:))?.displayName ?? "Global"
        worktreeName = environment.flatMap(snapshot.worktree(for:))?.branch ?? "—"
    }

    var hoverDetail: String {
        var lines = [
            operation.kind,
            "State: \(operation.state.label)",
            "Repository: \(repositoryName)",
            "Worktree: \(worktreeName)",
            "Created: \(operation.createdAt.formatted(date: .abbreviated, time: .standard))",
            "Updated: \(operation.updatedAt.formatted(date: .abbreviated, time: .standard))",
            "Operation: \(operation.id)",
        ]
        if let environment {
            lines.append("Environment: \(environment.displayName)")
            if let targetId = environment.targetId {
                lines.append("Target: \(targetId)")
            }
        }
        if let revision = operation.environmentRevision {
            lines.append("Environment revision: \(revision)")
        }
        if let phase = operation.phase, !phase.isEmpty {
            lines.append("Phase: \(phase)")
        }
        if let error = operation.error {
            lines.append("\(error.code): \(error.message)")
            if let diagnostic = error.diagnostic, !diagnostic.isEmpty {
                lines.append("Diagnostic: \(diagnostic)")
            }
            if let logReference = error.logReference, !logReference.isEmpty {
                lines.append("Logs: \(logReference)")
            }
            if let nextAction = error.nextAction, !nextAction.isEmpty {
                lines.append("Next action: \(nextAction)")
            }
        }
        return lines.joined(separator: "\n")
    }
}

private struct OperationTableRow: View {
    let operation: OperationModel
    let snapshot: StatusSnapshot
    @State private var hovered = false

    private var presentation: OperationRowPresentation {
        OperationRowPresentation(operation: operation, snapshot: snapshot)
    }

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: systemImage)
                .foregroundStyle(operation.state.tint)
                .frame(width: 18)
            Text(operation.updatedAt.formatted(.dateTime.month(.abbreviated).day().hour().minute()))
                .monospacedDigit()
                .frame(width: 124, alignment: .leading)
            Text(repositoryName)
                .lineLimit(1)
                .frame(width: 94, alignment: .leading)
            Text(worktreeName)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(width: 210, alignment: .leading)
            Text(operation.kind)
                .font(.callout.monospaced())
                .lineLimit(1)
                .frame(width: 164, alignment: .leading)
            Text(operation.state.label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(operation.state.tint)
                .frame(width: 86, alignment: .trailing)
        }
        .font(.callout)
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(hovered ? Color.accentColor.opacity(0.08) : Color.clear)
        .contentShape(Rectangle())
        .onHover { hovered = $0 }
        .help(hoverDetail)
    }

    private var repositoryName: String {
        presentation.repositoryName
    }

    private var worktreeName: String {
        presentation.worktreeName
    }

    private var systemImage: String {
        switch operation.state {
        case .succeeded: "checkmark.circle.fill"
        case .failed: "exclamationmark.circle.fill"
        case .pending, .running: "arrow.triangle.2.circlepath.circle.fill"
        case .cancelled: "xmark.circle.fill"
        case .unknown: "questionmark.circle.fill"
        }
    }

    private var hoverDetail: String {
        presentation.hoverDetail
    }
}

struct KeyValueRow: View {
    let key: String
    let value: String
    var monospaced = false
    var copyable = false

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(key)
                .foregroundStyle(.secondary)
            Spacer(minLength: 12)
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(value)
                    .font(monospaced ? .body.monospaced() : .body)
                    .textSelection(.enabled)
                    .multilineTextAlignment(.trailing)
                if copyable {
                    CopyValueButton(value: value, label: key)
                }
            }
        }
        .font(.callout)
    }
}

struct CopyValueButton: View {
    let value: String
    let label: String
    @State private var copied = false

    var body: some View {
        Button {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(value, forType: .string)
            copied = true
            Task {
                try? await Task.sleep(for: .seconds(1.2))
                copied = false
            }
        } label: {
            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                .font(.caption)
                .frame(width: 16, height: 16)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(copied ? Color.green : Color.secondary)
        .help(copied ? "Copied" : "Copy \(label)")
        .accessibilityLabel(copied ? "Copied \(label)" : "Copy \(label)")
    }
}

struct GitStateChips: View {
    let state: WorktreeState

    var body: some View {
        HStack(spacing: 6) {
            if state.isClean && !state.locked && !state.prunable {
                chip("Clean", tint: .green)
            }
            if state.hasTrackedChanges { chip("Modified", tint: .orange) }
            if state.hasUntrackedFiles { chip("Untracked", tint: .orange) }
            if state.hasUnpushedCommits { chip("Unpushed", tint: .blue) }
            if state.locked { chip("Locked", tint: .purple) }
            if state.prunable { chip("Prunable", tint: .red) }
        }
    }

    private func chip(_ label: String, tint: Color) -> some View {
        Text(label)
            .font(.caption2.weight(.medium))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(tint.opacity(0.15), in: Capsule())
            .foregroundStyle(tint)
    }
}

struct LineChangeBadges: View {
    let committed: LineChanges
    let uncommitted: LineChanges

    var body: some View {
        HStack(spacing: 5) {
            if !committed.isEmpty {
                badge(systemImage: "arrow.triangle.branch", changes: committed)
                    .help("Committed since the branch diverged from its configured base: \(detail(committed)).")
            }
            if !uncommitted.isEmpty {
                badge(systemImage: "laptopcomputer", changes: uncommitted)
                    .help("Uncommitted tracked and untracked work: \(detail(uncommitted)).")
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func badge(systemImage: String, changes: LineChanges) -> some View {
        HStack(spacing: 4) {
            Image(systemName: systemImage)
                .foregroundStyle(.secondary)
            Text("+\(changes.additions)")
                .foregroundStyle(.green)
            Text("−\(changes.deletions)")
                .foregroundStyle(.red)
                .monospacedDigit()
        }
        .font(.caption2)
        .padding(.horizontal, 6)
        .padding(.vertical, 3)
        .background(.secondary.opacity(0.14), in: Capsule())
    }

    private func detail(_ changes: LineChanges) -> String {
        "\(changes.additions) additions, \(changes.deletions) deletions across \(changes.files) files"
    }
}

struct AlertSummaryRow: View {
    let alert: AlertModel
    @State private var expanded = false

    var body: some View {
        FullWidthDisclosure(isExpanded: $expanded) {
            HStack(alignment: .top, spacing: 9) {
                Image(systemName: alert.severity.systemImage)
                    .foregroundStyle(alert.severity.tint)
                VStack(alignment: .leading, spacing: 2) {
                    Text(alert.summary)
                        .font(.callout.weight(.medium))
                    Text(alert.code)
                        .font(.caption.monospaced())
                        .foregroundStyle(.secondary)
                }
                Spacer()
            }
        } content: {
            VStack(alignment: .leading, spacing: 6) {
                KeyValueRow(key: "Alert ID", value: alert.id, monospaced: true, copyable: true)
                KeyValueRow(key: "Environment ID", value: alert.environmentId ?? "Global", monospaced: true, copyable: alert.environmentId != nil)
                KeyValueRow(key: "Workload ID", value: alert.serviceId ?? "Not workload-scoped", monospaced: true, copyable: alert.serviceId != nil)
                KeyValueRow(key: "Severity", value: alert.severity.rawValue)
                KeyValueRow(key: "Status", value: alert.status.rawValue)
                KeyValueRow(key: "Occurrences", value: "\(alert.occurrences)", monospaced: true)
                KeyValueRow(key: "First seen", value: alert.firstSeenAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Last seen", value: alert.lastSeenAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 7)
        }
    }
}
