import CryptoKit
import Darwin
import Foundation

struct BoundedFileFingerprintCache {
    static let maximumFileBytes: Int64 = 128 * 1024 * 1024

    private var digests: [StrongFileMetadata: Data] = [:]

    mutating func fingerprint(at url: URL) throws -> Data {
        var inspected = Darwin.stat()
        guard lstat(url.path, &inspected) == 0,
              inspected.st_mode & S_IFMT == S_IFREG else {
            throw FileFingerprintError.unsafeFile
        }
        let inspectedMetadata = StrongFileMetadata(inspected)
        guard inspectedMetadata.size <= Self.maximumFileBytes else {
            throw FileFingerprintError.fileTooLarge
        }
        if let digest = digests[inspectedMetadata] {
            return digest
        }

        let descriptor = open(url.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else {
            throw FileFingerprintError.unsafeFile
        }
        defer { close(descriptor) }

        var opened = Darwin.stat()
        guard fstat(descriptor, &opened) == 0,
              opened.st_mode & S_IFMT == S_IFREG,
              StrongFileMetadata(opened) == inspectedMetadata else {
            throw FileFingerprintError.changedWhileReading
        }

        var hasher = SHA256()
        var bytesReadTotal: Int64 = 0
        var buffer = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let bytesRead = buffer.withUnsafeMutableBytes { pointer in
                Darwin.read(descriptor, pointer.baseAddress, pointer.count)
            }
            if bytesRead < 0 {
                if errno == EINTR { continue }
                throw FileFingerprintError.readFailed
            }
            if bytesRead == 0 { break }
            bytesReadTotal += Int64(bytesRead)
            guard bytesReadTotal <= Self.maximumFileBytes else {
                throw FileFingerprintError.fileTooLarge
            }
            hasher.update(data: Data(buffer[0..<bytesRead]))
        }
        var completed = Darwin.stat()
        guard fstat(descriptor, &completed) == 0,
              StrongFileMetadata(completed) == inspectedMetadata,
              bytesReadTotal == inspectedMetadata.size else {
            throw FileFingerprintError.changedWhileReading
        }

        let digest = Data(hasher.finalize())
        if digests.count >= 64 {
            digests.removeAll(keepingCapacity: true)
        }
        digests[inspectedMetadata] = digest
        return digest
    }
}

private struct StrongFileMetadata: Hashable {
    let device: UInt64
    let inode: UInt64
    let size: Int64
    let mode: UInt32
    let modifiedSeconds: Int64
    let modifiedNanoseconds: Int64
    let changedSeconds: Int64
    let changedNanoseconds: Int64

    init(_ status: Darwin.stat) {
        device = UInt64(status.st_dev)
        inode = UInt64(status.st_ino)
        size = status.st_size
        mode = UInt32(status.st_mode)
        modifiedSeconds = Int64(status.st_mtimespec.tv_sec)
        modifiedNanoseconds = Int64(status.st_mtimespec.tv_nsec)
        changedSeconds = Int64(status.st_ctimespec.tv_sec)
        changedNanoseconds = Int64(status.st_ctimespec.tv_nsec)
    }
}

enum FileFingerprintError: Error {
    case unsafeFile
    case fileTooLarge
    case changedWhileReading
    case readFailed
}
