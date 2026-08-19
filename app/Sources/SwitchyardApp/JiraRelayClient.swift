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
              url.host == "example.atlassian.net",
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

struct JiraRelayCommandResolver: Sendable {
    let homeDirectory: URL
    let environment: [String: String]

    init(
        homeDirectory: URL = FileManager.default.homeDirectoryForCurrentUser,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) {
        self.homeDirectory = homeDirectory
        self.environment = environment
    }

    func command(issueKey: String) throws -> ExactCommand {
        guard issueKey.range(
            of: #"^[A-Z][A-Z0-9]+-[1-9][0-9]*$"#,
            options: .regularExpression
        ) != nil else {
            throw JiraRelayClientError.invalidResponse
        }
        let relayRoot = environment["SWITCHYARD_JIRA_RELAY_ROOT"].map {
            URL(fileURLWithPath: $0, isDirectory: true)
        } ?? homeDirectory.appending(path: "Developer/jira-mcp-relay", directoryHint: .isDirectory)
        let script = relayRoot.appending(path: "dist/src/issue-summary.js", directoryHint: .notDirectory)
        guard regularFile(script) else { throw JiraRelayClientError.unavailable }

        let configuredNode = environment["SWITCHYARD_NODE_BINARY"].map {
            URL(fileURLWithPath: $0, isDirectory: false)
        }
        let candidates = [configuredNode].compactMap { $0 } + [
            URL(fileURLWithPath: "/opt/homebrew/bin/node"),
            URL(fileURLWithPath: "/usr/local/bin/node"),
            homeDirectory.appending(path: ".local/bin/node", directoryHint: .notDirectory),
        ]
        guard let node = candidates
            .map({ $0.resolvingSymlinksInPath() })
            .first(where: { regularFile($0) && FileManager.default.isExecutableFile(atPath: $0.path) }) else {
            throw JiraRelayClientError.unavailable
        }
        return ExactCommand(executableURL: node, arguments: [script.path, issueKey])
    }

    private func regularFile(_ url: URL) -> Bool {
        guard let values = try? url.resourceValues(forKeys: [.isRegularFileKey]) else { return false }
        return values.isRegularFile == true
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
