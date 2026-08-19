import Darwin
import Foundation

public struct ExactCommand: Sendable, Equatable {
    public let executableURL: URL
    public let arguments: [String]
    public let environmentOverrides: [String: String]
    public let timeout: TimeInterval
    public let maximumOutputBytes: Int

    public init(
        executableURL: URL,
        arguments: [String],
        environmentOverrides: [String: String] = [:],
        timeout: TimeInterval = 15,
        maximumOutputBytes: Int = 64 * 1024
    ) {
        self.executableURL = executableURL
        self.arguments = arguments
        self.environmentOverrides = environmentOverrides
        self.timeout = timeout
        self.maximumOutputBytes = maximumOutputBytes
    }
}

public struct ExactCommandResult: Sendable, Equatable {
    public let exitCode: Int32
    public let standardOutput: Data

    public init(exitCode: Int32, standardOutput: Data = Data()) {
        self.exitCode = exitCode
        self.standardOutput = standardOutput
    }
}

public protocol ExactArgvRunning: Sendable {
    func run(_ command: ExactCommand) throws -> ExactCommandResult
}

public struct FoundationExactArgvRunner: ExactArgvRunning {
    private let baseEnvironment: [String: String]

    public init(environment: [String: String] = ProcessInfo.processInfo.environment) {
        self.baseEnvironment = environment
    }

    public func run(_ command: ExactCommand) throws -> ExactCommandResult {
        guard command.timeout > 0, command.maximumOutputBytes >= 0 else {
            throw ExactCommandError.invalidLimits
        }

        let process = Process()
        let output = Pipe()
        let capturedOutput = BoundedCommandOutput(limit: command.maximumOutputBytes)
        let terminated = DispatchSemaphore(value: 0)
        process.executableURL = command.executableURL
        process.arguments = command.arguments
        process.environment = Self.sanitizedEnvironment(
            executableURL: command.executableURL,
            overrides: command.environmentOverrides,
            base: baseEnvironment
        )
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        process.standardInput = FileHandle.nullDevice
        output.fileHandleForReading.readabilityHandler = { handle in
            let data = handle.availableData
            if !data.isEmpty { capturedOutput.append(data) }
        }
        process.terminationHandler = { _ in terminated.signal() }

        do {
            try process.run()
        } catch {
            output.fileHandleForReading.readabilityHandler = nil
            throw ExactCommandError.couldNotLaunch
        }

        let deadline = DispatchTime.now() + command.timeout
        if terminated.wait(timeout: deadline) == .timedOut {
            process.terminate()
            if terminated.wait(timeout: .now() + 1) == .timedOut {
                Darwin.kill(process.processIdentifier, SIGKILL)
                _ = terminated.wait(timeout: .now() + 1)
            }
            output.fileHandleForReading.readabilityHandler = nil
            throw ExactCommandError.timedOut
        }

        output.fileHandleForReading.readabilityHandler = nil
        capturedOutput.append(output.fileHandleForReading.readDataToEndOfFile())
        guard !capturedOutput.exceededLimit else {
            throw ExactCommandError.outputTooLarge
        }
        return ExactCommandResult(
            exitCode: process.terminationStatus,
            standardOutput: capturedOutput.data
        )
    }

    static func sanitizedEnvironment(
        executableURL: URL,
        overrides: [String: String],
        base: [String: String]
    ) -> [String: String] {
        let allowedNames = ["HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"]
        var environment = Dictionary(uniqueKeysWithValues: allowedNames.compactMap { name in
            base[name].map { (name, $0) }
        })
        var pathComponents = [
            executableURL.deletingLastPathComponent().path,
            base["HOME"].map { "\($0)/.local/bin" },
            "/opt/homebrew/bin",
            "/usr/local/bin",
            "/usr/bin",
            "/bin",
            "/usr/sbin",
            "/sbin",
        ].compactMap { $0 }
        var seen = Set<String>()
        pathComponents = pathComponents.filter { seen.insert($0).inserted }
        environment["PATH"] = pathComponents.joined(separator: ":")
        for (name, value) in overrides where isValidEnvironmentName(name) {
            environment[name] = value
        }
        return environment
    }

    private static func isValidEnvironmentName(_ name: String) -> Bool {
        guard let first = name.utf8.first,
              (first == 95 || (65...90).contains(first) || (97...122).contains(first)) else {
            return false
        }
        return name.utf8.dropFirst().allSatisfy {
            $0 == 95 || (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0)
        }
    }
}

private final class BoundedCommandOutput: @unchecked Sendable {
    private let lock = NSLock()
    private let limit: Int
    private var storage = Data()
    private var overflowed = false

    init(limit: Int) {
        self.limit = limit
    }

    func append(_ data: Data) {
        lock.lock()
        defer { lock.unlock() }
        guard !overflowed else { return }
        let remaining = max(0, limit - storage.count)
        if data.count > remaining {
            storage.append(data.prefix(remaining))
            overflowed = true
        } else {
            storage.append(data)
        }
    }

    var data: Data {
        lock.lock()
        defer { lock.unlock() }
        return storage
    }

    var exceededLimit: Bool {
        lock.lock()
        defer { lock.unlock() }
        return overflowed
    }
}

public enum ExactCommandError: Error, Sendable, Equatable, CustomStringConvertible {
    case couldNotLaunch
    case outputTooLarge
    case timedOut
    case invalidLimits
    case failed

    public var description: String {
        switch self {
        case .couldNotLaunch:
            return "the local service command could not be launched"
        case .outputTooLarge:
            return "the local service command returned too much data"
        case .timedOut:
            return "the local service command timed out"
        case .invalidLimits:
            return "the local service command has invalid execution limits"
        case .failed:
            return "the local service command failed"
        }
    }
}
