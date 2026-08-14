import Foundation

public let requiredDaemonVersion = "0.1.0-dev"

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

public struct BundleDaemonBinaryProvider: DaemonBinaryProviding {
    private let bundle: Bundle
    private let developmentOverride: String?

    public init(
        bundle: Bundle = .main,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) {
        self.bundle = bundle
        self.developmentOverride = environment["SWITCHYARD_DAEMON_BINARY"]
    }

    public func daemonBinary() throws -> DaemonBinary {
        if let developmentOverride, !developmentOverride.isEmpty {
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
        throw DaemonBinarySourceError.notPackaged
    }
}

public enum DaemonBinarySourceError: Error, Sendable, Equatable, CustomStringConvertible {
    case notPackaged

    public var description: String {
        "This SwiftPM build does not contain the Switchyard daemon. Package it as Contents/Resources/SwitchyardDaemon or set SWITCHYARD_DAEMON_BINARY for development."
    }
}
