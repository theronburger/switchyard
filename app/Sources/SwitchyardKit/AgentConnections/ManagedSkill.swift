import CryptoKit
import Darwin
import Foundation

enum ManagedSkillError: Error {
    case unsafeDirectory
    case unsafeFile
    case missingManifest
    case tooLarge
    case changed
    case installFailed
    case recoveryRequired(String)
}

enum ManagedSkill {
    private static let maximumFileBytes = 2 * 1024 * 1024
    private static let maximumTreeBytes = 8 * 1024 * 1024

    static func exists(_ url: URL) -> Bool {
        var status = Darwin.stat()
        return lstat(url.path, &status) == 0
    }

    static func fingerprint(_ root: URL) throws -> Data {
        try fingerprint(root, requireCurrentUserOwner: false)
    }

    static func fingerprintOwned(_ root: URL) throws -> Data {
        try fingerprint(root, requireCurrentUserOwner: true)
    }

    private static func fingerprint(
        _ root: URL,
        requireCurrentUserOwner: Bool
    ) throws -> Data {
        let entries = try readTree(root, requireCurrentUserOwner: requireCurrentUserOwner)
        guard entries.contains(where: { $0.relativePath == "SKILL.md" }) else {
            throw ManagedSkillError.missingManifest
        }
        var hasher = SHA256()
        for entry in entries {
            hasher.update(data: Data(entry.relativePath.utf8))
            hasher.update(data: Data([0]))
            hasher.update(data: entry.contents)
            hasher.update(data: Data([0]))
        }
        return Data(hasher.finalize())
    }

    static func install(source: URL, destination: URL) throws {
        let entries = try readTree(source, requireCurrentUserOwner: false)
        guard entries.contains(where: { $0.relativePath == "SKILL.md" }) else {
            throw ManagedSkillError.missingManifest
        }

        let parent = destination.deletingLastPathComponent()
        try FileManager.default.createDirectory(
            at: parent,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        try validateDirectory(parent, requireCurrentUserOwner: true)

        let staged = parent.appending(path: ".switchyard-skill-\(UUID().uuidString)")
        let backup = parent.appending(path: ".switchyard-skill-backup-\(UUID().uuidString)")
        var stagedExists = false
        var backupExists = false
        defer {
            if stagedExists { try? FileManager.default.removeItem(at: staged) }
            if backupExists { try? FileManager.default.removeItem(at: backup) }
        }

        try FileManager.default.createDirectory(
            at: staged,
            withIntermediateDirectories: false,
            attributes: [.posixPermissions: 0o700]
        )
        stagedExists = true
        for entry in entries {
            let target = staged.appending(path: entry.relativePath)
            try FileManager.default.createDirectory(
                at: target.deletingLastPathComponent(),
                withIntermediateDirectories: true,
                attributes: [.posixPermissions: 0o700]
            )
            try write(entry.contents, to: target)
        }
        guard try fingerprint(staged) == fingerprint(source) else {
            throw ManagedSkillError.changed
        }

        if exists(destination) {
            _ = try readTree(destination, requireCurrentUserOwner: true)
            guard exclusiveRename(destination, backup) else { throw ManagedSkillError.installFailed }
            backupExists = true
        }
        guard exclusiveRename(staged, destination) else {
            if backupExists, exclusiveRename(backup, destination) {
                backupExists = false
            } else if backupExists {
                backupExists = false
                throw ManagedSkillError.recoveryRequired(backup.path)
            }
            throw ManagedSkillError.installFailed
        }
        stagedExists = false
        if backupExists {
            try FileManager.default.removeItem(at: backup)
            backupExists = false
        }
    }

    private static func readTree(
        _ root: URL,
        requireCurrentUserOwner: Bool
    ) throws -> [SkillEntry] {
        try validateDirectory(root, requireCurrentUserOwner: requireCurrentUserOwner)
        var entries: [SkillEntry] = []
        var totalBytes = 0
        try collectEntries(
            root: root,
            directory: root,
            requireCurrentUserOwner: requireCurrentUserOwner,
            entries: &entries,
            totalBytes: &totalBytes
        )
        return entries.sorted { $0.relativePath < $1.relativePath }
    }

    private static func collectEntries(
        root: URL,
        directory: URL,
        requireCurrentUserOwner: Bool,
        entries: inout [SkillEntry],
        totalBytes: inout Int
    ) throws {
        try validateDirectory(directory, requireCurrentUserOwner: requireCurrentUserOwner)
        let children = try FileManager.default.contentsOfDirectory(
            at: directory,
            includingPropertiesForKeys: nil,
            options: []
        ).sorted { $0.lastPathComponent < $1.lastPathComponent }
        for child in children {
            var status = Darwin.stat()
            guard lstat(child.path, &status) == 0 else { throw ManagedSkillError.changed }
            switch status.st_mode & S_IFMT {
            case S_IFDIR:
                try collectEntries(
                    root: root,
                    directory: child,
                    requireCurrentUserOwner: requireCurrentUserOwner,
                    entries: &entries,
                    totalBytes: &totalBytes
                )
            case S_IFREG:
                let contents = try readFile(child, requireCurrentUserOwner: requireCurrentUserOwner)
                totalBytes += contents.count
                guard totalBytes <= maximumTreeBytes else { throw ManagedSkillError.tooLarge }
                let rootComponents = root.standardizedFileURL.pathComponents
                let childComponents = child.standardizedFileURL.pathComponents
                guard childComponents.starts(with: rootComponents),
                      childComponents.count > rootComponents.count else {
                    throw ManagedSkillError.changed
                }
                let relative = childComponents.dropFirst(rootComponents.count).joined(separator: "/")
                entries.append(SkillEntry(relativePath: relative, contents: contents))
            default:
                throw ManagedSkillError.unsafeFile
            }
        }
    }

    private static func readFile(_ url: URL, requireCurrentUserOwner: Bool) throws -> Data {
        let descriptor = open(url.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else { throw ManagedSkillError.unsafeFile }
        defer { close(descriptor) }
        var status = Darwin.stat()
        guard fstat(descriptor, &status) == 0,
              status.st_mode & S_IFMT == S_IFREG,
              Int(status.st_mode) & 0o022 == 0,
              !requireCurrentUserOwner || status.st_uid == geteuid(),
              status.st_size <= maximumFileBytes else {
            throw ManagedSkillError.unsafeFile
        }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 16 * 1024)
        while true {
            let count = buffer.withUnsafeMutableBytes { pointer in
                Darwin.read(descriptor, pointer.baseAddress, pointer.count)
            }
            if count < 0 {
                if errno == EINTR { continue }
                throw ManagedSkillError.unsafeFile
            }
            if count == 0 { break }
            data.append(contentsOf: buffer[0..<count])
            guard data.count <= maximumFileBytes else { throw ManagedSkillError.tooLarge }
        }
        return data
    }

    private static func write(_ contents: Data, to url: URL) throws {
        let descriptor = open(
            url.path,
            O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW | O_CLOEXEC,
            mode_t(0o600)
        )
        guard descriptor >= 0 else { throw ManagedSkillError.installFailed }
        defer { close(descriptor) }
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
                    throw ManagedSkillError.installFailed
                }
                written += count
            }
        }
        guard fsync(descriptor) == 0 else { throw ManagedSkillError.installFailed }
    }

    private static func validateDirectory(
        _ url: URL,
        requireCurrentUserOwner: Bool
    ) throws {
        let descriptor = open(url.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else { throw ManagedSkillError.unsafeDirectory }
        defer { close(descriptor) }
        var status = Darwin.stat()
        guard fstat(descriptor, &status) == 0,
              status.st_mode & S_IFMT == S_IFDIR,
              Int(status.st_mode) & 0o022 == 0,
              !requireCurrentUserOwner || status.st_uid == geteuid() else {
            throw ManagedSkillError.unsafeDirectory
        }
    }

    private static func exclusiveRename(_ source: URL, _ destination: URL) -> Bool {
        renamex_np(source.path, destination.path, UInt32(RENAME_EXCL)) == 0
    }
}

private struct SkillEntry {
    let relativePath: String
    let contents: Data
}
