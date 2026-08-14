import SwiftUI
import SwitchyardKit

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
            Divider().padding(.vertical, 2)
        }
    }
}

struct NativeFact: View {
    let label: String
    let value: String
    var monospaced = false
    var tint: Color = .primary

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(monospaced ? .callout.monospaced() : .callout)
                .foregroundStyle(tint)
                .lineLimit(1)
                .truncationMode(.middle)
                .help(value)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .overlay(alignment: .trailing) {
            Divider().padding(.vertical, 1)
        }
    }
}

struct KeyValueRow: View {
    let key: String
    let value: String
    var monospaced = false

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            Text(key)
                .foregroundStyle(.secondary)
            Spacer(minLength: 12)
            Text(value)
                .font(monospaced ? .body.monospaced() : .body)
                .textSelection(.enabled)
                .multilineTextAlignment(.trailing)
        }
        .font(.callout)
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

struct OperationSummaryRow: View {
    let operation: OperationModel

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                KeyValueRow(key: "Operation ID", value: operation.id, monospaced: true)
                KeyValueRow(key: "Environment ID", value: operation.environmentId ?? "Global", monospaced: true)
                KeyValueRow(
                    key: "Environment revision",
                    value: operation.environmentRevision.map(String.init) ?? "Not reported",
                    monospaced: true
                )
                KeyValueRow(key: "Created", value: operation.createdAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Updated", value: operation.updatedAt.formatted(date: .abbreviated, time: .standard))
                if let error = operation.error {
                    Divider()
                    KeyValueRow(key: "Error code", value: error.code, monospaced: true)
                    KeyValueRow(key: "Message", value: error.message)
                    KeyValueRow(key: "Retryable", value: error.retryable ? "Yes" : "No")
                    if let value = error.resourceKind { KeyValueRow(key: "Resource kind", value: value) }
                    if let value = error.resourceId { KeyValueRow(key: "Resource ID", value: value, monospaced: true) }
                    if let value = error.currentState { KeyValueRow(key: "Current state", value: value) }
                    if let value = error.requestedState { KeyValueRow(key: "Requested state", value: value) }
                }
            }
            .padding(.top, 7)
        } label: {
            HStack(spacing: 9) {
                Image(systemName: operation.state == .failed ? "exclamationmark.circle.fill" : "checkmark.circle.fill")
                    .foregroundStyle(operation.state.tint)
                VStack(alignment: .leading, spacing: 1) {
                    Text(operation.kind)
                        .font(.callout.monospaced())
                    Text("Updated \(Format.relative(operation.updatedAt))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Text(operation.state.label)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(operation.state.tint)
            }
        }
    }
}

struct AlertSummaryRow: View {
    let alert: AlertModel

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                KeyValueRow(key: "Alert ID", value: alert.id, monospaced: true)
                KeyValueRow(key: "Environment ID", value: alert.environmentId ?? "Global", monospaced: true)
                KeyValueRow(key: "Service ID", value: alert.serviceId ?? "Not service-scoped", monospaced: true)
                KeyValueRow(key: "Severity", value: alert.severity.rawValue)
                KeyValueRow(key: "Status", value: alert.status.rawValue)
                KeyValueRow(key: "Occurrences", value: "\(alert.occurrences)", monospaced: true)
                KeyValueRow(key: "First seen", value: alert.firstSeenAt.formatted(date: .abbreviated, time: .standard))
                KeyValueRow(key: "Last seen", value: alert.lastSeenAt.formatted(date: .abbreviated, time: .standard))
            }
            .padding(.top, 7)
        } label: {
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
        }
    }
}
