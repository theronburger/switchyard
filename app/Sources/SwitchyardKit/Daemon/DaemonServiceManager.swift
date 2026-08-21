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
    private let channel: SwitchyardChannel
    private var fingerprintCache = BoundedFileFingerprintCache()

    public init(
        binaryProvider: (any DaemonBinaryProviding)? = nil,
        commandRunner: any ExactArgvRunning = FoundationExactArgvRunner(),
        paths: LaunchAgentPaths = .standard(),
        launchctlURL: URL = URL(fileURLWithPath: "/bin/launchctl"),
        userID: uid_t = getuid(),
        fileManager: FileManager = .default,
        channel: SwitchyardChannel = .release
    ) {
        // The provider shares the manager's channel so a release-channel manager
        // can never be handed an environment-overridden daemon executable.
        self.binaryProvider = binaryProvider ?? BundleDaemonBinaryProvider(channel: channel)
        self.commandRunner = commandRunner
        self.paths = paths
        self.launchctlURL = launchctlURL
        self.userID = userID
        self.fileManager = fileManager
        self.channel = channel
    }

    public func inspect() async throws -> DaemonRegistrationStatus {
        guard fileManager.fileExists(atPath: paths.launchAgentURL.path) else {
            return .notRegistered
        }
        guard isOwnedInstalledExecutable(at: paths.installedBinaryURL) else {
            return .notFound
        }
        guard commandLinkMatches() else {
            return .outdated
        }
        if let packagedBinary = try packagedBinaryIfAvailable() {
            guard isRegularExecutable(at: packagedBinary.sourceURL) else {
                throw DaemonServiceError.binaryInvalid
            }
            let plan = try LaunchAgentPlanBuilder.make(
                binary: packagedBinary,
                paths: paths,
                userID: userID,
                channel: channel
            )
            guard propertyListMatches(plan.propertyList),
                  try binariesMatch(packagedBinary.sourceURL, paths.installedBinaryURL) else {
                return .outdated
            }
        }
        let result = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: ["print", serviceTarget]
        ))
        return result.exitCode == 0 ? .enabled : .requiresApproval
    }

    public func install() async throws {
        let plan = try makeInstallPlan()
        let changes = try materialize(plan)
        let printed = try commandRunner.run(ExactCommand(
            executableURL: launchctlURL,
            arguments: ["print", plan.serviceTarget]
        ))
        if printed.exitCode == 0 {
            if changes.propertyListChanged {
                _ = try commandRunner.run(ExactCommand(
                    executableURL: launchctlURL,
                    arguments: ["bootout", plan.serviceTarget]
                ))
                try runLaunchctl(["bootstrap", plan.userDomain, plan.paths.launchAgentURL.path])
            } else {
                try runLaunchctl(["kickstart", "-k", plan.serviceTarget])
            }
        } else {
            try runLaunchctl(["bootstrap", plan.userDomain, plan.paths.launchAgentURL.path])
        }
    }

    public func kickstart() async throws {
        try runLaunchctl(["kickstart", "-k", serviceTarget])
    }

    public func repair() async throws {
        let plan = try makeInstallPlan()
        _ = try materialize(plan)
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
            userID: userID,
            channel: channel
        )
    }

    private var serviceTarget: String {
        "gui/\(userID)/\(channel.launchAgentLabel)"
    }

    private struct MaterializedChanges {
        let propertyListChanged: Bool
    }

    private func materialize(_ plan: LaunchAgentInstallPlan) throws -> MaterializedChanges {
        try verifyDaemonBinary(at: plan.binary.sourceURL, expectedVersion: plan.binary.expectedVersion)
        try createPrivateDirectory(plan.paths.installedBinaryURL.deletingLastPathComponent())
        try createPrivateDirectory(plan.paths.standardOutputURL.deletingLastPathComponent())
        try createPrivateDirectory(plan.paths.launchAgentURL.deletingLastPathComponent())
        var installedBinaryMatches = false
        if isOwnedInstalledExecutable(at: plan.paths.installedBinaryURL) {
            installedBinaryMatches = try binariesMatch(plan.binary.sourceURL, plan.paths.installedBinaryURL)
        }
        if !installedBinaryMatches {
            try installBinaryAtomically(plan.binary.sourceURL, at: plan.paths.installedBinaryURL)
        }
        try verifyDaemonBinary(at: plan.paths.installedBinaryURL, expectedVersion: plan.binary.expectedVersion)
        try installCommandLinkIfNeeded()
        let propertyListChanged = !propertyListMatches(plan.propertyList)
        if propertyListChanged {
            try writeAtomically(plan.propertyList, to: plan.paths.launchAgentURL, permissions: 0o600)
        }
        return MaterializedChanges(
            propertyListChanged: propertyListChanged
        )
    }

    private func verifyDaemonBinary(at url: URL, expectedVersion: String) throws {
        guard isRegularExecutable(at: url) else {
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

    private func installCommandLinkIfNeeded() throws {
        guard let commandLinkURL = paths.commandLinkURL else { return }
        let directoryURL = commandLinkURL.deletingLastPathComponent()
        do {
            try fileManager.createDirectory(
                at: directoryURL,
                withIntermediateDirectories: true,
                attributes: [.posixPermissions: 0o755]
            )
        } catch {
            throw DaemonServiceError.commandLinkFailed
        }
        var directoryStatus = Darwin.stat()
        guard lstat(directoryURL.path, &directoryStatus) == 0,
              directoryStatus.st_mode & S_IFMT == S_IFDIR,
              directoryStatus.st_uid == getuid(),
              Int(directoryStatus.st_mode) & 0o022 == 0 else {
            throw DaemonServiceError.commandLinkFailed
        }
        var linkStatus = Darwin.stat()
        if lstat(commandLinkURL.path, &linkStatus) == 0 {
            guard linkStatus.st_mode & S_IFMT == S_IFLNK,
                  linkStatus.st_uid == getuid(),
                  commandLinkMatches() else {
                throw DaemonServiceError.commandLinkConflict
            }
            return
        }
        guard errno == ENOENT,
              symlink(paths.installedBinaryURL.path, commandLinkURL.path) == 0 else {
            throw DaemonServiceError.commandLinkFailed
        }
        guard commandLinkMatches() else {
            throw DaemonServiceError.commandLinkFailed
        }
    }

    private func commandLinkMatches() -> Bool {
        guard let commandLinkURL = paths.commandLinkURL else { return true }
        var linkStatus = Darwin.stat()
        guard lstat(commandLinkURL.path, &linkStatus) == 0,
              linkStatus.st_mode & S_IFMT == S_IFLNK,
              linkStatus.st_uid == getuid(),
              let destination = try? fileManager.destinationOfSymbolicLink(atPath: commandLinkURL.path) else {
            return false
        }
        return destination == paths.installedBinaryURL.path
    }

    private func packagedBinaryIfAvailable() throws -> DaemonBinary? {
        do {
            return try binaryProvider.daemonBinary()
        } catch DaemonBinarySourceError.notPackaged {
            return nil
        }
    }

    private func isRegularExecutable(at url: URL) -> Bool {
        var fileStatus = Darwin.stat()
        return lstat(url.path, &fileStatus) == 0
            && fileStatus.st_mode & S_IFMT == S_IFREG
            && access(url.path, X_OK) == 0
    }

    private func isOwnedInstalledExecutable(at url: URL) -> Bool {
        var fileStatus = Darwin.stat()
        return lstat(url.path, &fileStatus) == 0
            && fileStatus.st_mode & S_IFMT == S_IFREG
            && fileStatus.st_uid == getuid()
            && Int(fileStatus.st_mode) & 0o777 == 0o700
            && access(url.path, X_OK) == 0
    }

    private func binariesMatch(_ packagedURL: URL, _ installedURL: URL) throws -> Bool {
        do {
            let packagedDigest = try fingerprintCache.fingerprint(at: packagedURL)
            let installedDigest = try fingerprintCache.fingerprint(at: installedURL)
            return packagedDigest == installedDigest
        } catch {
            throw DaemonServiceError.binaryInvalid
        }
    }

    private func propertyListMatches(_ expectedData: Data) -> Bool {
        var fileStatus = Darwin.stat()
        guard lstat(paths.launchAgentURL.path, &fileStatus) == 0,
              fileStatus.st_mode & S_IFMT == S_IFREG,
              fileStatus.st_uid == getuid() else {
            return false
        }
        let installedData: Data
        do {
            installedData = try readSecureRuntimeFile(
                at: paths.launchAgentURL,
                maximumBytes: 64 * 1024,
                requireOwnerOnlyPermissions: true
            )
        } catch {
            return false
        }
        guard let installed = try? PropertyListSerialization.propertyList(
            from: installedData,
            options: [],
            format: nil
        ), let expected = try? PropertyListSerialization.propertyList(
            from: expectedData,
            options: [],
            format: nil
        ), let installedObject = installed as? NSObject else {
            return false
        }
        return installedObject.isEqual(expected)
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
    case commandLinkConflict
    case commandLinkFailed
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
        case .commandLinkConflict:
            return "the sy command already exists and is not owned by Switchyard"
        case .commandLinkFailed:
            return "the sy command could not be installed safely"
        case .propertyListWriteFailed:
            return "the Switchyard user LaunchAgent could not be written atomically"
        case .launchctlFailed:
            return "macOS did not accept the Switchyard user LaunchAgent operation"
        }
    }
}
