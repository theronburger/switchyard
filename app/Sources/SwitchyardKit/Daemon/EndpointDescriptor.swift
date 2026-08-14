import Foundation

/// The daemon's published endpoint descriptor (D-015): an atomically written,
/// owner-only JSON file describing the authenticated loopback HTTP endpoint.
///
/// The descriptor deliberately carries no secret material; the bearer token
/// lives in a separate mode-`0600` file loaded through ``BearerToken``.
public struct EndpointDescriptor: Codable, Sendable, Equatable {
    public static let supportedSchemaVersion = 1
    public static let supportedTransport = "http"

    public let schemaVersion: Int
    public let endpoint: String
    public let daemonInstanceId: String
    public let daemonVersion: String
    public let pid: Int
    public let processStartedAt: Date
    public let generatedAt: Date

    public init(
        schemaVersion: Int,
        endpoint: String,
        daemonInstanceId: String,
        daemonVersion: String,
        pid: Int,
        processStartedAt: Date,
        generatedAt: Date
    ) {
        self.schemaVersion = schemaVersion
        self.endpoint = endpoint
        self.daemonInstanceId = daemonInstanceId
        self.daemonVersion = daemonVersion
        self.pid = pid
        self.processStartedAt = processStartedAt
        self.generatedAt = generatedAt
    }

    public init(
        schemaVersion: Int,
        transport: String,
        host: String,
        port: Int,
        daemonVersion: String,
        instanceId: String,
        pid: Int? = nil,
        createdAt: Date? = nil
    ) {
        let publicationTime = createdAt ?? Date()
        self.init(
            schemaVersion: schemaVersion,
            endpoint: "\(transport)://\(host):\(port)",
            daemonInstanceId: instanceId,
            daemonVersion: daemonVersion,
            pid: pid ?? Int(ProcessInfo.processInfo.processIdentifier),
            processStartedAt: publicationTime,
            generatedAt: publicationTime
        )
    }

    /// Validates the descriptor against the invariants every client enforces:
    /// exact schema version, HTTP transport, strict loopback host, sane port.
    public func validate() throws {
        guard schemaVersion == Self.supportedSchemaVersion else {
            throw EndpointDescriptorError.unsupportedSchemaVersion(schemaVersion)
        }
        guard !daemonInstanceId.isEmpty, !daemonVersion.isEmpty, pid > 0 else {
            throw EndpointDescriptorError.malformed("daemon identity fields are missing")
        }
        guard processStartedAt <= generatedAt else {
            throw EndpointDescriptorError.malformed("process start is later than publication")
        }

        guard let components = URLComponents(string: endpoint) else {
            throw EndpointDescriptorError.malformed("endpoint is not a URL")
        }
        guard components.scheme == Self.supportedTransport else {
            throw EndpointDescriptorError.unsupportedTransport(components.scheme ?? "")
        }
        guard components.host == "127.0.0.1" else {
            throw EndpointDescriptorError.nonLoopbackHost(components.host ?? "")
        }
        guard let endpointPort = components.port, (1...65535).contains(endpointPort) else {
            throw EndpointDescriptorError.invalidPort(components.port ?? 0)
        }
        guard components.user == nil,
              components.password == nil,
              components.path.isEmpty,
              components.query == nil,
              components.fragment == nil,
              endpoint == "http://127.0.0.1:\(endpointPort)" else {
            throw EndpointDescriptorError.malformed("endpoint must be a canonical loopback origin")
        }
    }

    /// Strict loopback addresses only. Hostnames such as `localhost` are
    /// rejected so no DNS resolution can redirect the client.
    public static func isLoopback(_ host: String) -> Bool {
        host == "127.0.0.1"
    }

    /// The validated loopback base URL. Never carries credentials.
    public func loopbackBaseURL() throws -> URL {
        try validate()
        guard let url = URL(string: endpoint) else {
            throw EndpointDescriptorError.malformed("could not form a URL from host and port")
        }
        return url
    }

    public var transport: String { URLComponents(string: endpoint)?.scheme ?? "" }
    public var host: String { URLComponents(string: endpoint)?.host ?? "" }
    public var port: Int { URLComponents(string: endpoint)?.port ?? 0 }
    public var instanceId: String { daemonInstanceId }
    public var createdAt: Date { generatedAt }
}

public enum EndpointDescriptorError: Error, Equatable, CustomStringConvertible {
    case unreadable(String)
    case malformed(String)
    case unsupportedSchemaVersion(Int)
    case unsupportedTransport(String)
    case nonLoopbackHost(String)
    case invalidPort(Int)
    case insecurePermissions(octal: String)

    public var description: String {
        switch self {
        case .unreadable(let path):
            return "endpoint descriptor is unreadable at \(path)"
        case .malformed(let detail):
            return "endpoint descriptor is malformed: \(detail)"
        case .unsupportedSchemaVersion(let version):
            return "endpoint descriptor schema version \(version) is not supported"
        case .unsupportedTransport(let transport):
            return "endpoint descriptor transport \(transport) is not supported"
        case .nonLoopbackHost(let host):
            return "endpoint descriptor host \(host) is not a loopback address"
        case .invalidPort(let port):
            return "endpoint descriptor port \(port) is out of range"
        case .insecurePermissions(let octal):
            return "endpoint descriptor must be owner-only (0600), found \(octal)"
        }
    }
}

/// Loads and validates endpoint descriptors from disk.
public struct EndpointDescriptorLoader: Sendable {
    public let requirePrivatePermissions: Bool

    public init(requirePrivatePermissions: Bool = true) {
        self.requirePrivatePermissions = requirePrivatePermissions
    }

    public func load(from url: URL, fileManager: FileManager = .default) throws -> EndpointDescriptor {
        if requirePrivatePermissions {
            let permissions: String?
            do {
                permissions = try ownerOnlyPermissions(at: url, fileManager: fileManager)
            } catch {
                throw EndpointDescriptorError.unreadable(url.path)
            }
            if let permissions {
                throw EndpointDescriptorError.insecurePermissions(octal: permissions)
            }
        }
        let data: Data
        do {
            data = try Data(contentsOf: url)
        } catch {
            throw EndpointDescriptorError.unreadable(url.path)
        }
        let descriptor: EndpointDescriptor
        do {
            descriptor = try ContractDecoder().decode(EndpointDescriptor.self, from: data)
        } catch {
            throw EndpointDescriptorError.malformed(String(describing: error))
        }
        try descriptor.validate()
        return descriptor
    }
}

/// Where the daemon publishes its runtime files on this machine.
public struct DaemonEndpointLocation: Sendable, Equatable {
    public let descriptorURL: URL
    public let tokenURL: URL

    public init(descriptorURL: URL, tokenURL: URL) {
        self.descriptorURL = descriptorURL
        self.tokenURL = tokenURL
    }

    /// `~/Library/Application Support/Switchyard/daemon/`.
    public static func standard(fileManager: FileManager = .default) -> DaemonEndpointLocation {
        let base = fileManager
            .urls(for: .applicationSupportDirectory, in: .userDomainMask)
            .first ?? fileManager.homeDirectoryForCurrentUser.appending(path: "Library/Application Support")
        let daemonDirectory = base.appending(path: "Switchyard/daemon")
        return DaemonEndpointLocation(
            descriptorURL: daemonDirectory.appending(path: "runtime.json"),
            tokenURL: daemonDirectory.appending(path: "token")
        )
    }
}
