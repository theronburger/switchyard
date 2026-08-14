import Darwin
import Foundation

public struct ProcessIdentity: Sendable, Equatable {
    public let pid: Int
    public let startedAt: Date

    public init(pid: Int, startedAt: Date) {
        self.pid = pid
        self.startedAt = startedAt
    }
}

public protocol ProcessIdentityProviding: Sendable {
    func processIdentity(forPID pid: Int) throws -> ProcessIdentity
}

public struct DarwinProcessIdentityProvider: ProcessIdentityProviding {
    public init() {}

    public func processIdentity(forPID pid: Int) throws -> ProcessIdentity {
        guard pid > 0, let processID = Int32(exactly: pid) else {
            throw ProcessIdentityError.invalidPID
        }

        var query: [Int32] = [CTL_KERN, KERN_PROC, KERN_PROC_PID, processID]
        var process = kinfo_proc()
        var processSize = MemoryLayout<kinfo_proc>.stride
        let result = query.withUnsafeMutableBufferPointer { queryBuffer in
            sysctl(
                queryBuffer.baseAddress,
                UInt32(queryBuffer.count),
                &process,
                &processSize,
                nil,
                0
            )
        }
        guard result == 0,
              processSize >= MemoryLayout<kinfo_proc>.stride,
              process.kp_proc.p_pid == processID else {
            throw ProcessIdentityError.unavailable
        }

        let startTime = process.kp_proc.p_starttime
        let seconds = TimeInterval(startTime.tv_sec)
        let microseconds = TimeInterval(startTime.tv_usec) / 1_000_000
        let startedAt = Date(timeIntervalSince1970: seconds + microseconds)
        guard startedAt.timeIntervalSince1970 > 0 else {
            throw ProcessIdentityError.unavailable
        }
        return ProcessIdentity(pid: pid, startedAt: startedAt)
    }
}

public enum ProcessIdentityError: Error, Sendable, Equatable, CustomStringConvertible {
    case invalidPID
    case unavailable

    public var description: String {
        switch self {
        case .invalidPID:
            return "daemon process identifier is invalid"
        case .unavailable:
            return "daemon process identity is unavailable"
        }
    }
}
