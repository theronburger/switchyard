import Foundation

public protocol EndpointDescriptorProviding: Sendable {
    func loadDescriptor(from url: URL) throws -> EndpointDescriptor
}

public struct SecureEndpointDescriptorProvider: EndpointDescriptorProviding {
    private let loader: EndpointDescriptorLoader

    public init(loader: EndpointDescriptorLoader = EndpointDescriptorLoader()) {
        self.loader = loader
    }

    public func loadDescriptor(from url: URL) throws -> EndpointDescriptor {
        try loader.load(from: url)
    }
}

public protocol BearerTokenProviding: Sendable {
    func loadToken(from url: URL) throws -> BearerToken
}

public struct SecureBearerTokenProvider: BearerTokenProviding {
    public init() {}

    public func loadToken(from url: URL) throws -> BearerToken {
        try BearerToken.load(from: url)
    }
}

public struct DaemonConnection: Sendable {
    public let descriptor: EndpointDescriptor
    public let client: DaemonClient

    public init(descriptor: EndpointDescriptor, client: DaemonClient) {
        self.descriptor = descriptor
        self.client = client
    }
}

public protocol RuntimeConnectionEstablishing: Sendable {
    func connect() throws -> DaemonConnection
}

public struct RuntimeConnectionFactory: RuntimeConnectionEstablishing {
    public let location: DaemonEndpointLocation
    private let descriptorProvider: any EndpointDescriptorProviding
    private let processIdentityProvider: any ProcessIdentityProviding
    private let tokenProvider: any BearerTokenProviding
    private let transport: any DaemonTransport

    public init(
        location: DaemonEndpointLocation = .standard(),
        descriptorProvider: any EndpointDescriptorProviding = SecureEndpointDescriptorProvider(),
        processIdentityProvider: any ProcessIdentityProviding = DarwinProcessIdentityProvider(),
        tokenProvider: any BearerTokenProviding = SecureBearerTokenProvider(),
        transport: any DaemonTransport = URLSessionDaemonTransport()
    ) {
        self.location = location
        self.descriptorProvider = descriptorProvider
        self.processIdentityProvider = processIdentityProvider
        self.tokenProvider = tokenProvider
        self.transport = transport
    }

    public func connect() throws -> DaemonConnection {
        let descriptor: EndpointDescriptor
        do {
            descriptor = try descriptorProvider.loadDescriptor(from: location.descriptorURL)
        } catch let error as EndpointDescriptorError {
            throw RuntimeConnectionError.descriptor(error)
        } catch {
            throw RuntimeConnectionError.descriptorUnavailable
        }

        let processIdentity: ProcessIdentity
        do {
            processIdentity = try processIdentityProvider.processIdentity(forPID: descriptor.pid)
        } catch {
            throw RuntimeConnectionError.processIdentityUnavailable
        }
        guard processIdentity.pid == descriptor.pid,
              abs(processIdentity.startedAt.timeIntervalSince(descriptor.processStartedAt)) < 0.000_001 else {
            throw RuntimeConnectionError.processIdentityMismatch
        }

        let token: BearerToken
        do {
            token = try tokenProvider.loadToken(from: location.tokenURL)
        } catch let error as BearerTokenError {
            throw RuntimeConnectionError.token(error)
        } catch {
            throw RuntimeConnectionError.tokenUnavailable
        }
        let client = try DaemonClient(descriptor: descriptor, token: token, transport: transport)
        return DaemonConnection(descriptor: descriptor, client: client)
    }
}

public enum RuntimeConnectionError: Error, Sendable, CustomStringConvertible {
    case descriptor(EndpointDescriptorError)
    case descriptorUnavailable
    case processIdentityUnavailable
    case processIdentityMismatch
    case token(BearerTokenError)
    case tokenUnavailable

    public var description: String {
        switch self {
        case .descriptor(let error):
            return error.description
        case .descriptorUnavailable:
            return "daemon endpoint descriptor is unavailable"
        case .processIdentityUnavailable:
            return "daemon process identity could not be verified"
        case .processIdentityMismatch:
            return "daemon process identity does not match the endpoint descriptor"
        case .token(let error):
            return error.description
        case .tokenUnavailable:
            return "daemon token is unavailable"
        }
    }

    /// A readable descriptor that belongs to a different contract generation.
    /// The daemon and app must be brought to the same exact version; retrying
    /// or repairing the endpoint alone cannot fix it.
    public var requiresUpgrade: Bool {
        if case .descriptor(.unsupportedSchemaVersion) = self { return true }
        return false
    }

    public var descriptorIsMissing: Bool {
        switch self {
        case .descriptor(.missing):
            return true
        default:
            return false
        }
    }

    public var retryableWhileDaemonStarts: Bool {
        if descriptorIsMissing { return true }
        switch self {
        case .processIdentityUnavailable, .processIdentityMismatch:
            return true
        default:
            return false
        }
    }
}
