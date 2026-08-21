import Foundation

/// Private configuration reads, validation, and acceptance (D-025). The daemon
/// owns every revision; the app only requests validation of the desired file
/// and accepts one exact digest at one exact expected revision.
public protocol ConfigurationActionSubmitting: Sendable {
    func configuration() async throws -> ConfigurationStatus
    func validateConfiguration(_ request: ConfigurationValidationRequest) async throws -> ConfigurationStatus
    func acceptConfiguration(_ request: ConfigurationAcceptanceRequest) async throws -> ConfigurationStatus
    func mutateRepositoryConfiguration(_ request: ConfigurationRepositoryMutationRequest) async throws -> ConfigurationStatus
}

public struct LiveConfigurationActionClient: ConfigurationActionSubmitting {
    private let connectionFactory: any RuntimeConnectionEstablishing

    public init(connectionFactory: any RuntimeConnectionEstablishing = RuntimeConnectionFactory()) {
        self.connectionFactory = connectionFactory
    }

    public func configuration() async throws -> ConfigurationStatus {
        try await verifiedClient().configuration()
    }

    public func validateConfiguration(_ request: ConfigurationValidationRequest) async throws -> ConfigurationStatus {
        try await verifiedClient().validateConfiguration(request)
    }

    public func acceptConfiguration(_ request: ConfigurationAcceptanceRequest) async throws -> ConfigurationStatus {
        try await verifiedClient().acceptConfiguration(request)
    }

    public func mutateRepositoryConfiguration(_ request: ConfigurationRepositoryMutationRequest) async throws -> ConfigurationStatus {
        try await verifiedClient().mutateRepositoryConfiguration(request)
    }

    private func verifiedClient() async throws -> DaemonClient {
        let connection = try connectionFactory.connect()
        _ = try await connection.client.handshake()
        return connection.client
    }
}
