import Darwin
import Foundation

enum PrivateConfigFileError: Error, Sendable, Equatable {
    case unsafeDirectory
    case unsafeFile
    case changed
    case tooLarge
    case readFailed
    case writeFailed
}

enum PrivateConfigFile {
    static let maximumBytes = 2 * 1024 * 1024

    static func read(_ url: URL) throws -> Data? {
        var status = Darwin.stat()
        guard lstat(url.path, &status) == 0 else {
            if errno == ENOENT { return nil }
            throw PrivateConfigFileError.readFailed
        }
        guard status.st_mode & S_IFMT == S_IFREG,
              status.st_uid == geteuid(),
              Int(status.st_mode) & 0o777 == 0o600,
              status.st_size <= maximumBytes else {
            if status.st_size > maximumBytes { throw PrivateConfigFileError.tooLarge }
            throw PrivateConfigFileError.unsafeFile
        }
        do {
            return try readSecureRuntimeFile(
                at: url,
                maximumBytes: maximumBytes,
                requireOwnerOnlyPermissions: true
            )
        } catch {
            throw PrivateConfigFileError.readFailed
        }
    }

    static func replace(_ url: URL, expected: Data?, with replacement: Data) throws {
        guard replacement.count <= maximumBytes else { throw PrivateConfigFileError.tooLarge }
        try validateDirectory(url.deletingLastPathComponent())

        let temporaryURL = url.deletingLastPathComponent()
            .appending(path: ".switchyard-\(UUID().uuidString).tmp")
        let descriptor = open(
            temporaryURL.path,
            O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
            mode_t(0o600)
        )
        guard descriptor >= 0 else { throw PrivateConfigFileError.writeFailed }
        var keepTemporary = true
        defer {
            close(descriptor)
            if keepTemporary { unlink(temporaryURL.path) }
        }

        try replacement.withUnsafeBytes { buffer in
            var written = 0
            while written < buffer.count {
                let count = Darwin.write(
                    descriptor,
                    buffer.baseAddress?.advanced(by: written),
                    buffer.count - written
                )
                if count < 0 {
                    if errno == EINTR { continue }
                    throw PrivateConfigFileError.writeFailed
                }
                written += count
            }
        }
        guard fsync(descriptor) == 0 else { throw PrivateConfigFileError.writeFailed }

        let current = try read(url)
        guard current == expected else { throw PrivateConfigFileError.changed }
        guard Darwin.rename(temporaryURL.path, url.path) == 0 else {
            throw PrivateConfigFileError.writeFailed
        }
        keepTemporary = false

        let directoryDescriptor = open(
            url.deletingLastPathComponent().path,
            O_RDONLY | O_DIRECTORY | O_CLOEXEC
        )
        if directoryDescriptor >= 0 {
            _ = fsync(directoryDescriptor)
            close(directoryDescriptor)
        }
    }

    private static func validateDirectory(_ url: URL) throws {
        var status = Darwin.stat()
        guard lstat(url.path, &status) == 0,
              status.st_mode & S_IFMT == S_IFDIR,
              status.st_uid == geteuid(),
              Int(status.st_mode) & 0o022 == 0 else {
            throw PrivateConfigFileError.unsafeDirectory
        }
    }
}
