import Foundation
import SwitchyardKit

struct JiraIssueSummary: Decodable, Equatable, Sendable {
    let schemaVersion: Int
    let key: String
    let summary: String
    let status: String
    let assignee: String?
    let priority: String?
    let updated: Date
    let url: URL

    private enum CodingKeys: String, CodingKey {
        case schemaVersion, key, summary, status, assignee, priority, updated, url
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try values.decode(Int.self, forKey: .schemaVersion)
        key = try values.decode(String.self, forKey: .key)
        summary = try values.decode(String.self, forKey: .summary)
        status = try values.decode(String.self, forKey: .status)
        assignee = try values.decodeIfPresent(String.self, forKey: .assignee)
        priority = try values.decodeIfPresent(String.self, forKey: .priority)
        url = try values.decode(URL.self, forKey: .url)
        let timestamp = try values.decode(String.self, forKey: .updated)
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let parsedDate = formatter.date(from: timestamp) else {
            throw DecodingError.dataCorruptedError(
                forKey: .updated,
                in: values,
                debugDescription: "invalid Jira timestamp"
            )
        }
        updated = parsedDate
    }

    func validate(expectedKey: String) throws {
        guard schemaVersion == 1,
              key == expectedKey,
              key.range(of: #"^[A-Z][A-Z0-9]+-[1-9][0-9]*$"#, options: .regularExpression) != nil,
              !summary.isEmpty, summary.count <= 2_000,
              !status.isEmpty, status.count <= 500,
              url.scheme == "https",
              url.user == nil,
              url.password == nil,
              url.port == nil,
              url.host?.hasSuffix(".atlassian.net") == true,
              url.host != ".atlassian.net",
              url.path == "/browse/\(key)",
              url.query == nil,
              url.fragment == nil else {
            throw JiraRelayClientError.invalidResponse
        }
    }
}

enum JiraRelayClientError: Error, Equatable {
    case unavailable
    case failed
    case invalidResponse
}

/// Private, owner-authored description of the read-only relay command.
///
/// Switchyard ships no relay of its own and knows no relay path. The owner
/// declares one in `integrations/jira-relay.json` beside the private
/// configuration under Application Support:
///
/// ```json
/// {"schemaVersion": 1, "executable": "/absolute/relay", "arguments": ["--summary"]}
/// ```
///
/// The resolved command is `executable` + `arguments` + the issue key. Without
/// that file the integration is unavailable.
struct JiraRelayConfiguration: Decodable, Equatable, Sendable {
    static let currentSchemaVersion = 1
    static let maximumArguments = 32
    static let maximumArgumentLength = 1_024

    let schemaVersion: Int
    let executable: String
    let arguments: [String]

    init(schemaVersion: Int = Self.currentSchemaVersion, executable: String, arguments: [String] = []) {
        self.schemaVersion = schemaVersion
        self.executable = executable
        self.arguments = arguments
    }

    private enum CodingKeys: String, CodingKey {
        case schemaVersion, executable, arguments
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try values.decode(Int.self, forKey: .schemaVersion)
        executable = try values.decode(String.self, forKey: .executable)
        arguments = try values.decodeIfPresent([String].self, forKey: .arguments) ?? []
    }

    static func standardLocation(channel: SwitchyardChannel = .resolve()) -> URL {
        PrivateConfigurationLocation.standard(channel: channel).directory
            .appending(path: "integrations/jira-relay.json", directoryHint: .notDirectory)
    }

    func validate() throws {
        guard schemaVersion == Self.currentSchemaVersion,
              executable.hasPrefix("/"),
              !executable.contains("\0"),
              arguments.count <= Self.maximumArguments,
              arguments.allSatisfy({ $0.count <= Self.maximumArgumentLength && !$0.contains("\0") }) else {
            throw JiraRelayClientError.unavailable
        }
    }
}

struct JiraRelayCommandResolver: Sendable {
    static let maximumConfigurationBytes = 64 * 1024

    let configurationURL: URL

    init(configurationURL: URL = JiraRelayConfiguration.standardLocation()) {
        self.configurationURL = configurationURL
    }

    func command(issueKey: String) throws -> ExactCommand {
        guard issueKey.range(
            of: #"^[A-Z][A-Z0-9]+-[1-9][0-9]*$"#,
            options: .regularExpression
        ) != nil else {
            throw JiraRelayClientError.invalidResponse
        }
        let configuration = try loadConfiguration()
        let executable = URL(fileURLWithPath: configuration.executable, isDirectory: false)
        guard isTrustedExecutable(executable) else { throw JiraRelayClientError.unavailable }
        return ExactCommand(executableURL: executable, arguments: configuration.arguments + [issueKey])
    }

    /// The configuration must be a regular file owned by the current user
    /// that no other user can write. Symlinks and foreign files are ignored.
    func loadConfiguration() throws -> JiraRelayConfiguration {
        let descriptor = open(configurationURL.path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
        guard descriptor >= 0 else { throw JiraRelayClientError.unavailable }
        defer { close(descriptor) }
        var status = Darwin.stat()
        guard fstat(descriptor, &status) == 0,
              status.st_mode & S_IFMT == S_IFREG,
              status.st_uid == geteuid(),
              Int(status.st_mode) & 0o022 == 0,
              status.st_size <= Self.maximumConfigurationBytes else {
            throw JiraRelayClientError.unavailable
        }
        let handle = FileHandle(fileDescriptor: descriptor, closeOnDealloc: false)
        guard let data = try? handle.readToEnd(), data.count <= Self.maximumConfigurationBytes else {
            throw JiraRelayClientError.unavailable
        }
        guard let configuration = try? JSONDecoder().decode(JiraRelayConfiguration.self, from: data) else {
            throw JiraRelayClientError.unavailable
        }
        try configuration.validate()
        return configuration
    }

    /// The relay executable must be a regular file that only its owner can
    /// write; the owner is the current user or root (system or Homebrew
    /// toolchains). The path is used exactly as declared, never searched; a
    /// symlink (a Homebrew opt link, say) is followed and its target checked.
    private func isTrustedExecutable(_ url: URL) -> Bool {
        var status = Darwin.stat()
        guard stat(url.path, &status) == 0,
              status.st_mode & S_IFMT == S_IFREG,
              status.st_uid == geteuid() || status.st_uid == 0,
              Int(status.st_mode) & 0o022 == 0,
              Int(status.st_mode) & 0o111 != 0 else {
            return false
        }
        return true
    }
}

actor JiraIssueStore {
    static let live = JiraIssueStore()

    private let resolver: JiraRelayCommandResolver
    private let runner: any ExactArgvRunning
    private var cache: [String: JiraIssueSummary] = [:]

    init(
        resolver: JiraRelayCommandResolver = JiraRelayCommandResolver(),
        runner: any ExactArgvRunning = FoundationExactArgvRunner()
    ) {
        self.resolver = resolver
        self.runner = runner
    }

    func issue(key: String, refresh: Bool = false) async throws -> JiraIssueSummary {
        if !refresh, let cached = cache[key] { return cached }
        let command = try resolver.command(issueKey: key)
        let runner = runner
        let result = try await Task.detached(priority: .utility) {
            try runner.run(command)
        }.value
        guard result.exitCode == 0 else { throw JiraRelayClientError.failed }
        let summary: JiraIssueSummary
        do {
            summary = try JSONDecoder().decode(JiraIssueSummary.self, from: result.standardOutput)
            try summary.validate(expectedKey: key)
        } catch {
            throw JiraRelayClientError.invalidResponse
        }
        cache[key] = summary
        return summary
    }
}
