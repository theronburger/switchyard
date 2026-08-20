import Foundation

public struct LaunchAgentPaths: Sendable, Equatable {
    public let installedBinaryURL: URL
    public let commandLinkURL: URL?
    public let launchAgentURL: URL
    public let standardOutputURL: URL
    public let standardErrorURL: URL

    public init(
        installedBinaryURL: URL,
        commandLinkURL: URL? = nil,
        launchAgentURL: URL,
        standardOutputURL: URL,
        standardErrorURL: URL
    ) {
        self.installedBinaryURL = installedBinaryURL
        self.commandLinkURL = commandLinkURL
        self.launchAgentURL = launchAgentURL
        self.standardOutputURL = standardOutputURL
        self.standardErrorURL = standardErrorURL
    }

    public static func standard(
        fileManager: FileManager = .default,
        channel: SwitchyardChannel = .release
    ) -> LaunchAgentPaths {
        let home = fileManager.homeDirectoryForCurrentUser
        let applicationSupport = fileManager
            .urls(for: .applicationSupportDirectory, in: .userDomainMask)
            .first ?? home.appending(path: "Library/Application Support")
        let switchyardDirectory = applicationSupport.appending(path: channel.applicationSupportDirectoryName)
        let logsDirectory = switchyardDirectory.appending(path: "logs")
        return LaunchAgentPaths(
            installedBinaryURL: switchyardDirectory.appending(path: "bin/switchyard"),
            launchAgentURL: home.appending(path: "Library/LaunchAgents/\(channel.launchAgentLabel).plist"),
            standardOutputURL: logsDirectory.appending(path: "daemon.stdout.log"),
            standardErrorURL: logsDirectory.appending(path: "daemon.stderr.log")
        )
    }
}

public struct LaunchAgentInstallPlan: Sendable, Equatable {
    public let label: String
    public let userDomain: String
    public let serviceTarget: String
    public let binary: DaemonBinary
    public let paths: LaunchAgentPaths
    public let propertyList: Data

    public init(
        label: String,
        userDomain: String,
        serviceTarget: String,
        binary: DaemonBinary,
        paths: LaunchAgentPaths,
        propertyList: Data
    ) {
        self.label = label
        self.userDomain = userDomain
        self.serviceTarget = serviceTarget
        self.binary = binary
        self.paths = paths
        self.propertyList = propertyList
    }
}

public enum LaunchAgentPlanBuilder {
    public static let label = "com.theronburger.switchyard.daemon"
    public static let appBundleIdentifier = "com.theronburger.switchyard"

    public static func make(
        binary: DaemonBinary,
        paths: LaunchAgentPaths,
        userID: uid_t,
        channel: SwitchyardChannel = .release
    ) throws -> LaunchAgentInstallPlan {
        let userDomain = "gui/\(userID)"
        let serviceTarget = "\(userDomain)/\(channel.launchAgentLabel)"
        let propertyListObject: [String: Any] = [
            "Label": channel.launchAgentLabel,
            "ProgramArguments": [paths.installedBinaryURL.path, "daemon"],
            "RunAtLoad": true,
            "KeepAlive": true,
            "AssociatedBundleIdentifiers": [channel.appBundleIdentifier],
            "EnvironmentVariables": ["SWITCHYARD_CHANNEL": channel.rawValue],
            "StandardOutPath": paths.standardOutputURL.path,
            "StandardErrorPath": paths.standardErrorURL.path,
        ]
        let propertyList = try PropertyListSerialization.data(
            fromPropertyList: propertyListObject,
            format: .xml,
            options: 0
        )
        return LaunchAgentInstallPlan(
            label: channel.launchAgentLabel,
            userDomain: userDomain,
            serviceTarget: serviceTarget,
            binary: binary,
            paths: paths,
            propertyList: propertyList
        )
    }
}
