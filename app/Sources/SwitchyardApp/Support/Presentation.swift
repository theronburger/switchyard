import SwiftUI
import SwitchyardKit

// SwiftUI also declares `Environment` and `Alert`; Foundation declares
// `Operation`. Aliases keep the contract types unambiguous in view code.
typealias EnvironmentModel = SwitchyardKit.Environment
typealias AlertModel = SwitchyardKit.Alert
typealias OperationModel = SwitchyardKit.Operation

enum Format {
    static func memory(_ bytes: Int64) -> String {
        bytes.formatted(.byteCount(style: .memory))
    }

    static func cpu(_ percent: Double) -> String {
        "\(percent.formatted(.number.precision(.fractionLength(0...1))))% CPU"
    }

    static func relative(_ date: Date) -> String {
        date.formatted(.relative(presentation: .named))
    }

    static func shortRevision(_ revision: String) -> String {
        String(revision.prefix(8))
    }
}

extension Health {
    var tint: Color {
        switch self {
        case .healthy: .green
        case .degraded: .orange
        case .unhealthy: .red
        case .starting: .blue
        case .notApplicable, .unknown: .gray
        }
    }

    var label: String {
        switch self {
        case .healthy: "Healthy"
        case .degraded: "Degraded"
        case .unhealthy: "Unhealthy"
        case .starting: "Starting"
        case .notApplicable: "N/A"
        case .unknown: "Unknown"
        }
    }
}

extension ObservedState {
    var label: String {
        switch self {
        case .stopped: "Stopped"
        case .starting: "Starting"
        case .running: "Running"
        case .stopping: "Stopping"
        case .exited: "Exited"
        case .failed: "Failed"
        case .orphaned: "Orphaned"
        case .degraded: "Degraded"
        case .unverifiable: "Unverifiable"
        case .unknown: "Unknown"
        }
    }

    var tint: Color {
        switch self {
        case .running: .green
        case .starting, .stopping: .blue
        case .exited, .failed: .red
        case .orphaned, .degraded, .unverifiable: .orange
        case .stopped, .unknown: .gray
        }
    }
}

extension DesiredState {
    var label: String {
        switch self {
        case .running: "Running"
        case .stopped: "Stopped"
        case .failed: "Failed"
        case .orphaned: "Orphaned"
        case .unknown: "Unknown"
        }
    }
}

extension AlertSeverity {
    var systemImage: String {
        switch self {
        case .error: "exclamationmark.octagon.fill"
        case .warning: "exclamationmark.triangle.fill"
        case .info, .unknown: "info.circle.fill"
        }
    }

    var tint: Color {
        switch self {
        case .error: .red
        case .warning: .orange
        case .info, .unknown: .blue
        }
    }
}

extension OperationState {
    var label: String {
        switch self {
        case .pending: "Pending"
        case .running: "Running"
        case .succeeded: "Succeeded"
        case .failed: "Failed"
        case .cancelled: "Cancelled"
        case .unknown: "Unknown"
        }
    }

    var tint: Color {
        switch self {
        case .succeeded: .green
        case .running, .pending: .blue
        case .failed: .red
        case .cancelled, .unknown: .gray
        }
    }
}

extension DaemonLifecycleState {
    var systemImage: String {
        switch self {
        case .ready: "checkmark.circle.fill"
        case .repairing, .checkingRegistration, .startingDaemon,
             .locatingEndpoint, .connecting: "arrow.triangle.2.circlepath"
        case .idle: "circle.dashed"
        default: "exclamationmark.triangle.fill"
        }
    }

    var tint: Color {
        if isOperational { return .green }
        if needsUserAction { return .orange }
        return .secondary
    }
}
