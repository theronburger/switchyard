import Foundation

public let requiredDaemonVersion = Bundle.main.object(
    forInfoDictionaryKey: "CFBundleShortVersionString"
) as? String ?? "0.1.0-dev"

public struct DaemonBinary: Sendable, Equatable {
    public let sourceURL: URL
    public let expectedVersion: String

    public init(sourceURL: URL, expectedVersion: String = requiredDaemonVersion) {
        self.sourceURL = sourceURL
        self.expectedVersion = expectedVersion
    }
}

public protocol DaemonBinaryProviding: Sendable {
    func daemonBinary() throws -> DaemonBinary
}

/// Resolves the daemon executable the app is allowed to install.
///
/// The `SWITCHYARD_DAEMON_BINARY` override exists only so a SwiftPM
/// development build can point at a locally built daemon. It is honoured
/// solely on the `development` channel; a release bundle resolves its channel
/// from its embedded `Info.plist`, so no environment variable can redirect a
/// released app to a foreign executable.
public struct BundleDaemonBinaryProvider: DaemonBinaryProviding {
    public static let developmentOverrideVariable = "SWITCHYARD_DAEMON_BINARY"

    private let bundle: Bundle
    private let developmentOverride: String?
    private let channel: SwitchyardChannel

    public init(
        bundle: Bundle = .main,
        environment: [String: String] = ProcessInfo.processInfo.environment,
        channel: SwitchyardChannel = .resolve()
    ) {
        self.bundle = bundle
        self.developmentOverride = environment[Self.developmentOverrideVariable]
        self.channel = channel
    }

    public var honoursDevelopmentOverride: Bool {
        channel == .development
    }

    public func daemonBinary() throws -> DaemonBinary {
        if honoursDevelopmentOverride, let developmentOverride, !developmentOverride.isEmpty {
            return DaemonBinary(sourceURL: URL(fileURLWithPath: developmentOverride))
        }
        if let packagedBinary = bundle.resourceURL?.appending(path: "SwitchyardDaemon"),
           FileManager.default.fileExists(atPath: packagedBinary.path) {
            return DaemonBinary(sourceURL: packagedBinary)
        }
        if let auxiliaryExecutable = bundle.url(forAuxiliaryExecutable: "switchyard") {
            return DaemonBinary(sourceURL: auxiliaryExecutable)
        }
        if let resourceURL = bundle.resourceURL?.appending(path: "Helpers/switchyard"),
           FileManager.default.fileExists(atPath: resourceURL.path) {
            return DaemonBinary(sourceURL: resourceURL)
        }
        throw DaemonBinarySourceError.notPackaged(channel: channel)
    }
}

public enum DaemonBinarySourceError: Error, Sendable, Equatable, CustomStringConvertible {
    case notPackaged(channel: SwitchyardChannel)

    public var description: String {
        switch self {
        case .notPackaged(.development):
            "This SwiftPM build does not contain the Switchyard daemon. Package it as Contents/Resources/SwitchyardDaemon or set SWITCHYARD_DAEMON_BINARY for development."
        case .notPackaged(.release):
            "This release bundle does not contain the Switchyard daemon at Contents/Resources/SwitchyardDaemon. Reinstall Switchyard; release builds never load a daemon from the environment."
        }
    }
}
