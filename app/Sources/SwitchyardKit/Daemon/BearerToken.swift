import Foundation

/// The daemon bearer token from the mode-`0600` token file (D-015).
///
/// The raw value is deliberately hard to leak: descriptions are redacted, the
/// token never participates in URLs, and the only sanctioned escape hatch is
/// `authorizationHeaderValue`, which exists solely for the `Authorization`
/// request header.
public struct BearerToken: Sendable, Equatable {
    private let rawValue: String

    public init(rawValue: String) throws {
        let trimmed = rawValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.rangeOfCharacter(from: .whitespacesAndNewlines) == nil else {
            throw BearerTokenError.containsWhitespace
        }
        let standardBase64 = trimmed
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        let paddedBase64 = standardBase64 + String(repeating: "=", count: (4 - standardBase64.count % 4) % 4)
        guard let decoded = Data(base64Encoded: paddedBase64), decoded.count == 32 else {
            throw BearerTokenError.invalidFormat
        }
        let canonical = decoded.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
        guard canonical == trimmed else {
            throw BearerTokenError.invalidFormat
        }
        self.rawValue = trimmed
    }

    /// Loads the token from disk, refusing group- or world-accessible files.
    public static func load(
        from url: URL,
        fileManager: FileManager = .default,
        requirePrivatePermissions: Bool = true
    ) throws -> BearerToken {
        _ = fileManager
        let data: Data
        do {
            data = try readSecureRuntimeFile(
                at: url,
                maximumBytes: 4 * 1024,
                requireOwnerOnlyPermissions: requirePrivatePermissions
            )
        } catch let error as SecureFileError {
            if case .insecurePermissions(let octal) = error.problem {
                throw BearerTokenError.insecurePermissions(octal: octal)
            }
            throw BearerTokenError.unreadable(url.path)
        } catch {
            throw BearerTokenError.unreadable(url.path)
        }
        guard let contents = String(data: data, encoding: .utf8) else {
            throw BearerTokenError.invalidFormat
        }
        return try BearerToken(rawValue: contents)
    }

    /// The only place the raw secret leaves this type; callers must attach it
    /// exclusively to the `Authorization` header of loopback requests.
    public var authorizationHeaderValue: String {
        "Bearer \(rawValue)"
    }
}

extension BearerToken: CustomStringConvertible, CustomDebugStringConvertible {
    public var description: String { "BearerToken(redacted)" }
    public var debugDescription: String { description }
}

public enum BearerTokenError: Error, Equatable, CustomStringConvertible {
    case invalidFormat
    case containsWhitespace
    case unreadable(String)
    case insecurePermissions(octal: String)

    public var description: String {
        switch self {
        case .invalidFormat:
            return "daemon token must be a canonical 256-bit base64url value"
        case .containsWhitespace:
            return "daemon token contains interior whitespace"
        case .unreadable(let path):
            return "daemon token file is unreadable at \(path)"
        case .insecurePermissions(let octal):
            return "daemon token file must be owner-only (0600), found \(octal)"
        }
    }
}
