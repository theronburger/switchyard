import Foundation

public enum SwitchyardChannel: String, Sendable, Equatable {
    case development
    case release

    public static func resolve(
        infoDictionary: [String: Any]? = Bundle.main.infoDictionary,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> SwitchyardChannel {
        if let embedded = infoDictionary?["SwitchyardChannel"] as? String,
           let channel = SwitchyardChannel(rawValue: embedded) {
            return channel
        }
        if let requested = environment["SWITCHYARD_CHANNEL"],
           let channel = SwitchyardChannel(rawValue: requested) {
            return channel
        }
        return .development
    }

    public var applicationSupportDirectoryName: String {
        switch self {
        case .development: "Switchyard Development"
        case .release: "Switchyard"
        }
    }

    public var launchAgentLabel: String {
        switch self {
        case .development: "com.theronburger.switchyard.development.daemon"
        case .release: "com.theronburger.switchyard.daemon"
        }
    }

    public var appBundleIdentifier: String {
        switch self {
        case .development: "com.theronburger.switchyard.development"
        case .release: "com.theronburger.switchyard"
        }
    }

    public var permitsAgentRepair: Bool { self == .release }
    public var permitsUpdates: Bool { self == .release }
}
