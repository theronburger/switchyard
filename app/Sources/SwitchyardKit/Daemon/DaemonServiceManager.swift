import Darwin
import Foundation

public protocol DaemonServiceManaging: Sendable {
    func inspect() async throws -> DaemonRegistrationStatus
    func install() async throws
    func kickstart() async throws
    func repair() async throws
}

public actor LaunchAgentServiceManager: DaemonServiceManaging {
    private let binaryProvider: any DaemonBinaryProviding
    private let commandRunner: any ExactArgvRunning
    private let paths: LaunchAgentPaths
    private let launchctlURL: URL
    private let userID: uid_t
    private let fileManager: FileManager

    public init(
        binaryProvider: any DaemonBinaryProviding = BundleDaemonBinaryProvider(),
        commandRunner: any ExactArgvRunning = FoundationExactArgvRunner(),
        paths: LaunchAgentPaths = .standard(),
        launchctlURL: URL = URL(fileURLWithPath: "/bin/launchctl"),
        userID: uid_t = getuid(),
        fileManager: FileManager = .default
    ) {
        self.binaryProvider = binaryProvider
        self.commandRunner = commandRunner
        self.paths = paths
        self.launchctlURL = launchctlURL
        self.userID = userID
        self.fileManager = fileManager
    }

    public func inspect() async throws -> DaemonRegistrationStatus {
        guard fileManager.fileExists(atPath: paths.launchAgentURL.path) else {
            return .notRegistered
        }
        guard fileManager.isExecutableFile(atPath: paths.installedBinaryURL.path) else {
            return .notFound
        }
        let result = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: ["print", serviceTarget]
        ))
        return result.exitCode == 0 ? .enabled : .requiresApproval
    }

    public func install() async throws {
        let plan = try makeInstallPlan()
        try materialize(plan)
        let printed = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: ["print", plan.serviceTarget]
        ))
        if printed.exitCode == 0 {
            try runLaunchctl(["kickstart", "-k", plan.serviceTarget])
        } else {
            try runLaunchctl(["bootstrap", plan.userDomain, plan.paths.launchAgentURL.path])
        }
    }

    public func kickstart() async throws {
        try runLaunchctl(["kickstart", "-k", serviceTarget])
    }

    public func repair() async throws {
        let plan = try makeInstallPlan()
        try materialize(plan)
        _ = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: ["bootout", plan.serviceTarget]
        ))
        try runLaunchctl(["bootstrap", plan.userDomain, plan.paths.launchAgentURL.path])
        try runLaunchctl(["kickstart", "-k", plan.serviceTarget])
    }

    public func makeInstallPlan() throws -> LaunchAgentInstallPlan {
        try LaunchAgentPlanBuilder.make(
            binary: binaryProvider.daemonBinary(),
            paths: paths,
            userID: userID
        )
    }

    private var serviceTarget: String {
        "gui/\(userID)/\(LaunchAgentPlanBuilder.label)"
    }

    private func materialize(_ plan: LaunchAgentInstallPlan) throws {
        try verifyDaemonBinary(at: plan.binary.sourceURL, expectedVersion: plan.binary.expectedVersion)
        try createPrivateDirectory(plan.paths.installedBinaryURL.deletingLastPathComponent())
        try createPrivateDirectory(plan.paths.standardOutputURL.deletingLastPathComponent())
        try createPrivateDirectory(plan.paths.launchAgentURL.deletingLastPathComponent())
        try installBinaryAtomically(plan.binary.sourceURL, at: plan.paths.installedBinaryURL)
        try verifyDaemonBinary(at: plan.paths.installedBinaryURL, expectedVersion: plan.binary.expectedVersion)
        try writeAtomically(plan.propertyList, to: plan.paths.launchAgentURL, permissions: 0o600)
    }

    private func verifyDaemonBinary(at url: URL, expectedVersion: String) throws {
        var fileStatus = Darwin.stat()
        guard lstat(url.path, &fileStatus) == 0,
              fileStatus.st_mode & S_IFMT == S_IFREG,
              access(url.path, X_OK) == 0 else {
            throw DaemonServiceError.binaryInvalid
        }
        let result = try commandRunner.run(ExactCommand(executableURL: url, arguments: ["version"]))
        guard result.exitCode == 0,
              let version = try? JSONDecoder().decode(DaemonVersionOutput.self, from: result.standardOutput),
              version.schemaVersion == contractSchemaVersion,
              version.version == expectedVersion else {
            throw DaemonServiceError.binaryVersionMismatch
        }
    }

    private func createPrivateDirectory(_ url: URL) throws {
        try fileManager.createDirectory(
            at: url,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
    }

    private func installBinaryAtomically(_ sourceURL: URL, at destinationURL: URL) throws {
        let temporaryURL = destinationURL
            .deletingLastPathComponent()
            .appending(path: ".switchyard-install-\(UUID().uuidString)")
        defer { try? fileManager.removeItem(at: temporaryURL) }
        do {
            try fileManager.copyItem(at: sourceURL, to: temporaryURL)
            try fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: temporaryURL.path)
            guard fileManager.contentsEqual(atPath: sourceURL.path, andPath: temporaryURL.path) else {
                throw DaemonServiceError.binaryCopyFailed
            }
            guard Darwin.rename(temporaryURL.path, destinationURL.path) == 0 else {
                throw DaemonServiceError.binaryCopyFailed
            }
        } catch let error as DaemonServiceError {
            throw error
        } catch {
            throw DaemonServiceError.binaryCopyFailed
        }
    }

    private func writeAtomically(_ data: Data, to destinationURL: URL, permissions: Int) throws {
        let temporaryURL = destinationURL
            .deletingLastPathComponent()
            .appending(path: ".switchyard-plist-\(UUID().uuidString)")
        defer { try? fileManager.removeItem(at: temporaryURL) }
        do {
            try data.write(to: temporaryURL, options: .withoutOverwriting)
            try fileManager.setAttributes([.posixPermissions: permissions], ofItemAtPath: temporaryURL.path)
            guard Darwin.rename(temporaryURL.path, destinationURL.path) == 0 else {
                throw DaemonServiceError.propertyListWriteFailed
            }
        } catch let error as DaemonServiceError {
            throw error
        } catch {
            throw DaemonServiceError.propertyListWriteFailed
        }
    }

    private func runLaunchctl(_ arguments: [String]) throws {
        let result = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: arguments
        ))
        guard result.exitCode == 0 else {
            throw DaemonServiceError.launchctlFailed
        }
    }
}

private struct DaemonVersionOutput: Decodable {
    let schemaVersion: Int
    let version: String
}

public enum DaemonServiceError: Error, Sendable, Equatable, CustomStringConvertible {
    case binaryInvalid
    case binaryVersionMismatch
    case binaryCopyFailed
    case propertyListWriteFailed
    case launchctlFailed

    public var description: String {
        switch self {
        case .binaryInvalid:
            return "the packaged Switchyard daemon is not a regular executable"
        case .binaryVersionMismatch:
            return "the packaged Switchyard daemon version is incompatible with this app"
        case .binaryCopyFailed:
            return "the Switchyard daemon could not be installed atomically"
        case .propertyListWriteFailed:
            return "the Switchyard user LaunchAgent could not be written atomically"
        case .launchctlFailed:
            return "macOS did not accept the Switchyard user LaunchAgent operation"
        }
    }
}
