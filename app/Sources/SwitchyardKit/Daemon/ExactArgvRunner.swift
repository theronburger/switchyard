import Foundation

public struct ExactCommand: Sendable, Equatable {
    public let executableURL: URL
    public let arguments: [String]

    public init(executableURL: URL, arguments: [String]) {
        self.executableURL = executableURL
        self.arguments = arguments
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
    public init() {}

    public func run(_ command: ExactCommand) throws -> ExactCommandResult {
        let process = Process()
        let output = Pipe()
        process.executableURL = command.executableURL
        process.arguments = command.arguments
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        process.standardInput = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw ExactCommandError.couldNotLaunch
        }
        let standardOutput = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard standardOutput.count <= 64 * 1024 else {
            throw ExactCommandError.outputTooLarge
        }
        return ExactCommandResult(exitCode: process.terminationStatus, standardOutput: standardOutput)
    }
}

public enum ExactCommandError: Error, Sendable, Equatable, CustomStringConvertible {
    case couldNotLaunch
    case outputTooLarge
    case failed

    public var description: String {
        switch self {
        case .couldNotLaunch:
            return "the local service command could not be launched"
        case .outputTooLarge:
            return "the local service command returned too much data"
        case .failed:
            return "the local service command failed"
        }
    }
}
