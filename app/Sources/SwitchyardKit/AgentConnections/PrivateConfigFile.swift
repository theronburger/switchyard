import Darwin
import Foundation

enum PrivateConfigFileError: Error, Sendable, Equatable {
    case unsafeDirectory
    case unsafeFile
    case changed
    case tooLarge
    case recoveryRequired(String)
    case readFailed
    case writeFailed
}

enum PrivateConfigFile {
    // Claude Code accumulates per-project state in ~/.claude.json. Keep a
    // finite defensive bound without rejecting established installations.
    static let maximumBytes = 16 * 1024 * 1024

    static func read(_ url: URL) throws -> Data? {
        let directoryURL = url.deletingLastPathComponent()
        do {
            try validateDirectory(directoryURL)
        } catch PrivateConfigFileError.unsafeDirectory where missingDirectory(directoryURL) {
            // A host CLI that has never run may not have created its private
            // configuration directory yet. Accept exactly one missing path
            // component only when its existing parent is owner-controlled.
            try validateDirectory(directoryURL.deletingLastPathComponent())
            return nil
        }
        let descriptor = open(url.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else {
            if errno == ENOENT { return nil }
            throw PrivateConfigFileError.readFailed
        }
        defer { close(descriptor) }
        return try read(descriptor: descriptor)
    }

    static func restore(
        _ url: URL,
        expected: Data?,
        original: Data?,
        beforeReplacement: () -> Void = {}
    ) throws {
        if let original, original.count > maximumBytes {
            throw PrivateConfigFileError.tooLarge
        }
        let directoryURL = url.deletingLastPathComponent()
        try validateDirectory(directoryURL)

        let stagedURL: URL?
        if let original {
            stagedURL = try stage(original, in: directoryURL)
        } else {
            stagedURL = nil
        }
        var removeStaged = stagedURL != nil
        defer {
            if removeStaged, let stagedURL { unlink(stagedURL.path) }
        }

        let recoveryURL = directoryURL.appending(path: ".switchyard-config-recovery-\(UUID().uuidString)")
        var recoveryExists = false
        defer {
            if recoveryExists { unlink(recoveryURL.path) }
        }

        let current = try read(url)
        guard current == expected else { throw PrivateConfigFileError.changed }

        if current != nil {
            guard exclusiveRename(url, recoveryURL) else {
                let failure = errno
                throw failure == ENOENT ? PrivateConfigFileError.changed : PrivateConfigFileError.writeFailed
            }
            recoveryExists = true
            guard try read(recoveryURL) == expected else {
                if exclusiveRename(recoveryURL, url) {
                    recoveryExists = false
                } else {
                    recoveryExists = false
                    if let stagedURL {
                        removeStaged = false
                        throw PrivateConfigFileError.recoveryRequired(stagedURL.path)
                    }
                }
                throw PrivateConfigFileError.changed
            }
        } else if try read(url) != nil {
            throw PrivateConfigFileError.changed
        }

        if let stagedURL {
            beforeReplacement()
            guard exclusiveRename(stagedURL, url) else {
                let failure = errno
                if recoveryExists {
                    if exclusiveRename(recoveryURL, url) {
                        recoveryExists = false
                    } else {
                        recoveryExists = false
                        removeStaged = false
                        throw PrivateConfigFileError.recoveryRequired(stagedURL.path)
                    }
                }
                throw failure == EEXIST ? PrivateConfigFileError.changed : PrivateConfigFileError.writeFailed
            }
            removeStaged = false
        }

        if recoveryExists {
            guard unlink(recoveryURL.path) == 0 else { throw PrivateConfigFileError.writeFailed }
            recoveryExists = false
        }
        try syncDirectory(directoryURL)
    }

    private static func read(descriptor: Int32) throws -> Data {
        var status = Darwin.stat()
        guard fstat(descriptor, &status) == 0,
              status.st_mode & S_IFMT == S_IFREG,
              status.st_uid == geteuid(),
              Int(status.st_mode) & 0o022 == 0 else {
            throw PrivateConfigFileError.unsafeFile
        }
        guard status.st_size <= maximumBytes else { throw PrivateConfigFileError.tooLarge }

        var contents = Data()
        var buffer = [UInt8](repeating: 0, count: 16 * 1024)
        while true {
            let count = buffer.withUnsafeMutableBytes { pointer in
                Darwin.read(descriptor, pointer.baseAddress, pointer.count)
            }
            if count < 0 {
                if errno == EINTR { continue }
                throw PrivateConfigFileError.readFailed
            }
            if count == 0 { break }
            contents.append(contentsOf: buffer[0..<count])
            guard contents.count <= maximumBytes else { throw PrivateConfigFileError.tooLarge }
        }
        var completed = Darwin.stat()
        guard fstat(descriptor, &completed) == 0,
              sameFileSnapshot(status, completed),
              contents.count == Int(status.st_size) else {
            throw PrivateConfigFileError.changed
        }
        return contents
    }

    private static func stage(_ contents: Data, in directoryURL: URL) throws -> URL {
        let stagedURL = directoryURL.appending(path: ".switchyard-config-\(UUID().uuidString).tmp")
        let descriptor = open(
            stagedURL.path,
            O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
            mode_t(0o600)
        )
        guard descriptor >= 0 else { throw PrivateConfigFileError.writeFailed }
        var succeeded = false
        defer {
            close(descriptor)
            if !succeeded { unlink(stagedURL.path) }
        }
        try contents.withUnsafeBytes { buffer in
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
        succeeded = true
        return stagedURL
    }

    private static func validateDirectory(_ url: URL) throws {
        let descriptor = open(url.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else { throw PrivateConfigFileError.unsafeDirectory }
        defer { close(descriptor) }
        var status = Darwin.stat()
        guard fstat(descriptor, &status) == 0,
              status.st_mode & S_IFMT == S_IFDIR,
              status.st_uid == geteuid(),
              Int(status.st_mode) & 0o022 == 0 else {
            throw PrivateConfigFileError.unsafeDirectory
        }
    }

    private static func missingDirectory(_ url: URL) -> Bool {
        var status = Darwin.stat()
        guard lstat(url.path, &status) != 0 else { return false }
        return errno == ENOENT
    }

    private static func exclusiveRename(_ source: URL, _ destination: URL) -> Bool {
        renamex_np(source.path, destination.path, UInt32(RENAME_EXCL)) == 0
    }

    private static func syncDirectory(_ url: URL) throws {
        let descriptor = open(url.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else { throw PrivateConfigFileError.writeFailed }
        defer { close(descriptor) }
        guard fsync(descriptor) == 0 else { throw PrivateConfigFileError.writeFailed }
    }

    private static func sameFileSnapshot(_ first: Darwin.stat, _ second: Darwin.stat) -> Bool {
        first.st_dev == second.st_dev &&
            first.st_ino == second.st_ino &&
            first.st_size == second.st_size &&
            first.st_mode == second.st_mode &&
            first.st_uid == second.st_uid &&
            first.st_mtimespec.tv_sec == second.st_mtimespec.tv_sec &&
            first.st_mtimespec.tv_nsec == second.st_mtimespec.tv_nsec &&
            first.st_ctimespec.tv_sec == second.st_ctimespec.tv_sec &&
            first.st_ctimespec.tv_nsec == second.st_ctimespec.tv_nsec
    }
}
