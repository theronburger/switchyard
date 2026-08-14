import SwiftUI
import SwitchyardKit

/// Full detail for one environment: worktree, services, URLs, resources,
/// infrastructure, alerts, and recent operations.
struct EnvironmentDetailView: View {
    let environment: EnvironmentModel
    let snapshot: StatusSnapshot

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                header
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 360), spacing: 14)], alignment: .leading, spacing: 14) {
                    servicesCard
                    worktreeCard
                    urlsCard
                    resourcesCard
                    infrastructureCard
                    alertsCard
                    operationsCard
                }
            }
            .padding(20)
        }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline) {
            VStack(alignment: .leading, spacing: 4) {
                Text(environment.displayName)
                    .font(.largeTitle.bold())
                HStack(spacing: 8) {
                    if let repository = snapshot.repository(for: environment) {
                        Text(repository.displayName)
                            .foregroundStyle(.secondary)
                    }
                    if let branch = snapshot.worktree(for: environment)?.branch {
                        Text(branch)
                            .font(.callout.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 4) {
                HealthBadge(health: environment.health)
                Text("wants \(environment.desiredState.label.lowercased()) · is \(environment.observedState.label.lowercased())")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Text("revision \(environment.revision)")
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
            }
        }
    }

    private var servicesCard: some View {
        SectionCard(title: "Services", systemImage: "server.rack") {
            if environment.services.isEmpty {
                Text("No services requested.")
                    .foregroundStyle(.secondary)
            }
            ForEach(environment.services) { service in
                ServiceRow(service: service, leases: environment.portLeases(for: service))
                if service.id != environment.services.last?.id {
                    Divider()
                }
            }
        }
    }

    private var worktreeCard: some View {
        SectionCard(title: "Worktree", systemImage: "arrow.triangle.branch") {
            if let worktree = snapshot.worktree(for: environment) {
                VStack(alignment: .leading, spacing: 6) {
                    KeyValueRow(key: "Path", value: worktree.path, monospaced: true)
                    KeyValueRow(key: "Branch", value: worktree.branch ?? "detached", monospaced: true)
                    KeyValueRow(key: "Head", value: Format.shortRevision(worktree.headRevision), monospaced: true)
                    KeyValueRow(key: "Role", value: worktree.isPrimary ? "Primary checkout" : "Linked worktree")
                    HStack {
                        Text("Git state")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                        Spacer()
                        GitStateChips(state: worktree.git)
                    }
                }
            } else {
                Text("The worktree for this environment is no longer reported.")
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var urlsCard: some View {
        SectionCard(title: "URLs", systemImage: "link") {
            if environment.sortedURLs.isEmpty {
                Text("No URLs published.")
                    .foregroundStyle(.secondary)
            }
            ForEach(environment.sortedURLs, id: \.service) { entry in
                HStack {
                    Text(entry.service)
                        .font(.callout)
                    Spacer()
                    if let url = URL(string: entry.url) {
                        Link(entry.url, destination: url)
                            .font(.callout.monospaced())
                    } else {
                        Text(entry.url)
                            .font(.callout.monospaced())
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
    }

    private var resourcesCard: some View {
        SectionCard(title: "Resources", systemImage: "gauge.with.dots.needle.50percent") {
            HStack(spacing: 10) {
                StatChip(value: Format.cpu(environment.resources.cpuPercent), label: "logical group")
                StatChip(value: Format.memory(environment.resources.memoryBytes), label: "memory")
                StatChip(value: "\(environment.totalProcessCount)", label: "processes")
            }
        }
    }

    private var infrastructureCard: some View {
        SectionCard(title: "Infrastructure", systemImage: "shippingbox") {
            if environment.infrastructureLeases.isEmpty {
                Text("No infrastructure leases.")
                    .foregroundStyle(.secondary)
            }
            ForEach(environment.infrastructureLeases) { lease in
                HStack {
                    VStack(alignment: .leading, spacing: 1) {
                        Text(lease.displayName)
                            .font(.callout.weight(.medium))
                        Text("\(lease.kind) · \(lease.scope) scope")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    Text(lease.state)
                        .font(.caption)
                    Text(lease.ownership)
                        .font(.caption2.weight(.medium))
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(
                            (lease.ownership == "owned" ? Color.green : Color.gray).opacity(0.15),
                            in: Capsule()
                        )
                        .foregroundStyle(lease.ownership == "owned" ? .green : .secondary)
                }
            }
        }
    }

    private var alertsCard: some View {
        SectionCard(title: "Alerts", systemImage: "bell.badge") {
            let alerts = snapshot.alerts(forEnvironment: environment.id)
            if alerts.isEmpty {
                Label("No active alerts.", systemImage: "checkmark.circle")
                    .foregroundStyle(.green)
            }
            ForEach(alerts) { alert in
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: alert.severity.systemImage)
                        .foregroundStyle(alert.severity.tint)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(alert.summary)
                            .font(.callout)
                        Text("\(alert.code) · seen \(alert.occurrences)× · last \(Format.relative(alert.lastSeenAt))")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
    }

    private var operationsCard: some View {
        SectionCard(title: "Recent operations", systemImage: "clock.arrow.circlepath") {
            let operations = snapshot.operations(forEnvironment: environment.id)
            if operations.isEmpty {
                Text("No recorded operations.")
                    .foregroundStyle(.secondary)
            }
            ForEach(operations) { operation in
                HStack {
                    VStack(alignment: .leading, spacing: 1) {
                        Text(operation.kind)
                            .font(.callout.monospaced())
                        Text("updated \(Format.relative(operation.updatedAt))")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    Text(operation.state.label)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(operation.state.tint)
                    if let error = operation.error {
                        Image(systemName: "exclamationmark.circle")
                            .foregroundStyle(.red)
                            .help("\(error.code): \(error.message)")
                    }
                }
            }
        }
    }
}

struct ServiceRow: View {
    let service: Service
    let leases: [PortLease]

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            StatusDot(color: service.health.tint)
                .padding(.top, 5)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(service.displayName)
                        .font(.callout.weight(.semibold))
                    Text(service.kind)
                        .font(.caption2.weight(.medium))
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(.quaternary, in: Capsule())
                }
                HStack(spacing: 8) {
                    Text(service.observedState.label)
                        .foregroundStyle(service.observedState.tint)
                    if let run = service.run {
                        if run.restartCount > 0 {
                            Text("\(run.restartCount) restarts")
                                .foregroundStyle(.orange)
                        }
                        Text("\(run.processCount) procs")
                        Text(Format.cpu(run.cpuPercent))
                        Text(Format.memory(run.memoryBytes))
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                if !leases.isEmpty {
                    HStack(spacing: 6) {
                        ForEach(leases) { lease in
                            Text("\(String(lease.port)) \(lease.purpose)")
                                .font(.caption2.monospaced())
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .background(.blue.opacity(0.1), in: Capsule())
                                .foregroundStyle(.blue)
                        }
                    }
                }
            }
            Spacer()
            HealthBadge(health: service.health)
        }
    }
}
