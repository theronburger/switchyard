import Darwin
import Foundation

public enum SecureFileProblem: Sendable, Equatable {
    case missing
    case unreadable
    case symlink
    case notRegular
    case insecurePermissions(octal: String)
    case oversized(limitBytes: Int)
    case changedWhileOpening

    public var detail: String {
        switch self {
        case .missing:
            return "file does not exist"
        case .unreadable:
            return "file could not be read"
        case .symlink:
            return "symbolic links are not accepted"
        case .notRegular:
            return "file is not a regular file"
        case .insecurePermissions(let octal):
            return "file must be owner-only (0600), found \(octal)"
        case .oversized(let limitBytes):
            return "file exceeds \(limitBytes) bytes"
        case .changedWhileOpening:
            return "file changed while it was being opened"
        }
    }
}

public struct SecureFileError: Error, Sendable, Equatable, CustomStringConvertible {
    public let path: String
    public let problem: SecureFileProblem

    public var description: String { "\(path): \(problem.detail)" }
}

public func readSecureRuntimeFile(
    at url: URL,
    maximumBytes: Int,
    requireOwnerOnlyPermissions: Bool = true
) throws -> Data {
    let path = url.path
    var inspected = Darwin.stat()
    guard lstat(path, &inspected) == 0 else {
        throw SecureFileError(
            path: path,
            problem: errno == ENOENT ? .missing : .unreadable
        )
    }
    let fileType = inspected.st_mode & S_IFMT
    if fileType == S_IFLNK {
        throw SecureFileError(path: path, problem: .symlink)
    }
    guard fileType == S_IFREG else {
        throw SecureFileError(path: path, problem: .notRegular)
    }
    try validatePrivateMode(
        inspected.st_mode,
        path: path,
        required: requireOwnerOnlyPermissions,
        changed: false
    )
    guard inspected.st_size <= maximumBytes else {
        throw SecureFileError(path: path, problem: .oversized(limitBytes: maximumBytes))
    }

    let fileDescriptor = open(path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
    guard fileDescriptor >= 0 else {
        throw SecureFileError(
            path: path,
            problem: errno == ELOOP ? .symlink : .unreadable
        )
    }
    defer { close(fileDescriptor) }

    var opened = Darwin.stat()
    guard fstat(fileDescriptor, &opened) == 0 else {
        throw SecureFileError(path: path, problem: .unreadable)
    }
    guard opened.st_dev == inspected.st_dev,
          opened.st_ino == inspected.st_ino,
          opened.st_mode & S_IFMT == S_IFREG else {
        throw SecureFileError(path: path, problem: .changedWhileOpening)
    }
    try validatePrivateMode(
        opened.st_mode,
        path: path,
        required: requireOwnerOnlyPermissions,
        changed: true
    )

    var contents = Data()
    var buffer = [UInt8](repeating: 0, count: 16 * 1024)
    while true {
        let bytesRead = buffer.withUnsafeMutableBytes { pointer in
            Darwin.read(fileDescriptor, pointer.baseAddress, pointer.count)
        }
        if bytesRead < 0 {
            if errno == EINTR { continue }
            throw SecureFileError(path: path, problem: .unreadable)
        }
        if bytesRead == 0 { break }
        contents.append(contentsOf: buffer[0..<bytesRead])
        if contents.count > maximumBytes {
            throw SecureFileError(path: path, problem: .oversized(limitBytes: maximumBytes))
        }
    }
    return contents
}

private func validatePrivateMode(
    _ mode: mode_t,
    path: String,
    required: Bool,
    changed: Bool
) throws {
    guard required else { return }
    let permissions = Int(mode) & 0o777
    guard permissions != 0o600 else { return }
    throw SecureFileError(
        path: path,
        problem: changed ? .changedWhileOpening : .insecurePermissions(octal: String(permissions, radix: 8))
    )
}
