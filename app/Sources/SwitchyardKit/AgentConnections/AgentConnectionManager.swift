import Darwin
import Foundation

public enum AgentHost: String, Sendable, Equatable, Hashable, CaseIterable, Identifiable {
    case codex
    case claude

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .codex: "Codex"
        case .claude: "Claude Code"
        }
    }
}

public enum AgentConnectionState: String, Sendable, Equatable {
    case connected
    case missing
    case needsRepair
    case unavailable
    case refused

    public var canRepair: Bool {
        self == .missing || self == .needsRepair
    }
}

public struct AgentConnectionStatus: Sendable, Equatable, Identifiable {
    public let host: AgentHost
    public let state: AgentConnectionState
    public let detail: String

    public var id: AgentHost { host }

    public init(host: AgentHost, state: AgentConnectionState, detail: String) {
        self.host = host
        self.state = state
        self.detail = detail
    }
}

public struct AgentConnectionReport: Sendable, Equatable {
    public let statuses: [AgentConnectionStatus]

    public init(statuses: [AgentConnectionStatus]) {
        self.statuses = statuses
    }

    public func status(for host: AgentHost) -> AgentConnectionStatus? {
        statuses.first { $0.host == host }
    }
}

public protocol AgentConnectionManaging: Sendable {
    func inspect() async -> AgentConnectionReport
    func repair(_ host: AgentHost) async -> AgentConnectionReport
    func repairAll() async -> AgentConnectionReport
}

public struct AgentConnectionPaths: Sendable, Equatable {
    public let switchyardExecutableURL: URL
    public let codexExecutableURL: URL?
    public let codexConfigURL: URL
    public let claudeConfigURL: URL
    public let claudeExecutableURL: URL?
    public let claudeConfigDirectoryURL: URL

    public init(
        switchyardExecutableURL: URL,
        codexExecutableURL: URL?,
        codexConfigURL: URL,
        claudeConfigURL: URL,
        claudeExecutableURL: URL? = nil,
        claudeConfigDirectoryURL: URL? = nil
    ) {
        self.switchyardExecutableURL = switchyardExecutableURL
        self.codexExecutableURL = codexExecutableURL
        self.codexConfigURL = codexConfigURL
        self.claudeConfigURL = claudeConfigURL
        self.claudeExecutableURL = claudeExecutableURL
        self.claudeConfigDirectoryURL = claudeConfigDirectoryURL ?? claudeConfigURL.deletingLastPathComponent()
    }

    public static func standard(
        fileManager: FileManager = .default,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> AgentConnectionPaths {
        let home = fileManager.homeDirectoryForCurrentUser
        let codexCandidates = [
            URL(fileURLWithPath: "/Applications/ChatGPT.app/Contents/Resources/codex"),
            home.appending(path: ".local/bin/codex"),
            URL(fileURLWithPath: "/opt/homebrew/bin/codex"),
            URL(fileURLWithPath: "/usr/local/bin/codex"),
        ]
        let personalClaudeRoot = home.appending(path: ".claude-personal")
        let claudeRoot = environment["CLAUDE_CONFIG_DIR"].map(URL.init(fileURLWithPath:)) ?? (
            fileManager.fileExists(atPath: personalClaudeRoot.appending(path: ".claude.json").path)
                ? personalClaudeRoot
                : home
        )
        let claudeCandidates = [
            home.appending(path: ".local/bin/claude"),
            URL(fileURLWithPath: "/opt/homebrew/bin/claude"),
            URL(fileURLWithPath: "/usr/local/bin/claude"),
        ]
        let codexExecutable = codexCandidates
            .map { $0.resolvingSymlinksInPath() }
            .first { fileManager.isExecutableFile(atPath: $0.path) }
        let claudeExecutable = claudeCandidates
            .map { $0.resolvingSymlinksInPath() }
            .first { fileManager.isExecutableFile(atPath: $0.path) }
        return AgentConnectionPaths(
            switchyardExecutableURL: LaunchAgentPaths.standard(fileManager: fileManager).installedBinaryURL,
            codexExecutableURL: codexExecutable,
            codexConfigURL: home.appending(path: ".codex/config.toml"),
            claudeConfigURL: claudeRoot.appending(path: ".claude.json"),
            claudeExecutableURL: claudeExecutable,
            claudeConfigDirectoryURL: claudeRoot
        )
    }
}

public actor AgentConnectionManager: AgentConnectionManaging {
    public static let serverName = "switchyard"

    private let paths: AgentConnectionPaths
    private let commandRunner: any ExactArgvRunning

    public init(
        paths: AgentConnectionPaths = .standard(),
        commandRunner: any ExactArgvRunning = FoundationExactArgvRunner()
    ) {
        self.paths = paths
        self.commandRunner = commandRunner
    }

    public func inspect() async -> AgentConnectionReport {
        AgentConnectionReport(statuses: [inspectCodex(), inspectClaude()])
    }

    public func repair(_ host: AgentHost) async -> AgentConnectionReport {
        let repaired: AgentConnectionStatus
        switch host {
        case .codex:
            repaired = repairCodex()
        case .claude:
            repaired = repairClaude()
        }
        return AgentConnectionReport(statuses: AgentHost.allCases.map { candidate in
            if candidate == host { return repaired }
            return candidate == .codex ? inspectCodex() : inspectClaude()
        })
    }

    public func repairAll() async -> AgentConnectionReport {
        AgentConnectionReport(statuses: [repairCodex(), repairClaude()])
    }

    private func inspectCodex() -> AgentConnectionStatus {
        guard helperIsInstalled() else { return helperUnavailable(.codex) }
        guard let codexExecutableURL = paths.codexExecutableURL else {
            return AgentConnectionStatus(
                host: .codex,
                state: .unavailable,
                detail: "The Codex CLI could not be found in a known app or executable location."
            )
        }
        do {
            _ = try PrivateConfigFile.read(paths.codexConfigURL)
        } catch {
            return unsafeConfig(.codex)
        }
        let result: ExactCommandResult
        do {
            result = try commandRunner.run(ExactCommand(
                executableURL: codexExecutableURL,
                arguments: ["mcp", "list", "--json"]
            ))
        } catch {
            return refused(.codex, "Codex MCP configuration could not be inspected safely.")
        }
        guard result.exitCode == 0,
              let servers = try? JSONDecoder().decode([CodexMCPServer].self, from: result.standardOutput) else {
            return refused(.codex, "Codex did not provide a valid MCP configuration; no changes will be made.")
        }
        guard let server = servers.first(where: { $0.name == Self.serverName }) else {
            return missing(.codex)
        }
        guard server.enabled,
              server.transport.type == "stdio",
              server.transport.command == paths.switchyardExecutableURL.path,
              server.transport.args == ["mcp"],
              !server.transport.hasEnvironment,
              server.transport.environmentVariables.isEmpty,
              server.transport.cwd == nil else {
            return needsRepair(.codex)
        }
        return connected(.codex)
    }

    private func repairCodex() -> AgentConnectionStatus {
        let before = inspectCodex()
        guard before.state.canRepair,
              let codexExecutableURL = paths.codexExecutableURL else { return before }
        let expected: Data?
        do {
            expected = try PrivateConfigFile.read(paths.codexConfigURL)
            guard try PrivateConfigFile.read(paths.codexConfigURL) == expected else {
                return refused(.codex, "Codex configuration changed during repair; no changes were made.")
            }
        } catch {
            return unsafeConfig(.codex)
        }
        let result: ExactCommandResult
        do {
            result = try commandRunner.run(ExactCommand(
                executableURL: codexExecutableURL,
                arguments: [
                    "mcp", "add", Self.serverName, "--",
                    paths.switchyardExecutableURL.path, "mcp",
                ]
            ))
        } catch {
            return refused(.codex, "Codex could not apply the MCP repair.")
        }
        guard result.exitCode == 0 else {
            return refused(.codex, "Codex refused the MCP repair; its configuration was left to Codex.")
        }
        return inspectCodex()
    }

    private func inspectClaude() -> AgentConnectionStatus {
        guard helperIsInstalled() else { return helperUnavailable(.claude) }
        let data: Data?
        do {
            data = try PrivateConfigFile.read(paths.claudeConfigURL)
        } catch {
            return unsafeConfig(.claude)
        }
        guard let data else {
            guard paths.claudeExecutableURL != nil else {
                return AgentConnectionStatus(
                    host: .claude,
                    state: .unavailable,
                    detail: "The Claude Code CLI could not be found in a known executable location."
                )
            }
            return missing(.claude)
        }
        guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return refused(.claude, "Claude Code configuration is not valid JSON; no changes will be made.")
        }
        guard let serversValue = root["mcpServers"] else { return missing(.claude) }
        guard let servers = serversValue as? [String: Any] else {
            return refused(.claude, "Claude Code uses an unsupported MCP configuration shape; no changes will be made.")
        }
        guard let serverValue = servers[Self.serverName] else { return missing(.claude) }
        guard let server = serverValue as? [String: Any] else { return needsRepair(.claude) }
        let environmentIsEmpty: Bool
        if let environmentValue = server["env"] {
            guard let environment = environmentValue as? [String: Any] else {
                return needsRepair(.claude)
            }
            environmentIsEmpty = environment.isEmpty
        } else {
            environmentIsEmpty = true
        }
        guard server["type"] as? String == "stdio",
              server["command"] as? String == paths.switchyardExecutableURL.path,
              server["args"] as? [String] == ["mcp"],
              environmentIsEmpty else {
            return needsRepair(.claude)
        }
        return connected(.claude)
    }

    private func repairClaude() -> AgentConnectionStatus {
        let before = inspectClaude()
        guard before.state.canRepair,
              let claudeExecutableURL = paths.claudeExecutableURL else { return before }
        let expected: Data?
        do {
            expected = try PrivateConfigFile.read(paths.claudeConfigURL)
        } catch {
            return unsafeConfig(.claude)
        }
        guard (try? PrivateConfigFile.read(paths.claudeConfigURL)) == expected else {
            return refused(.claude, "Claude Code configuration changed during repair; no changes were made.")
        }

        let environment = ["CLAUDE_CONFIG_DIR": paths.claudeConfigDirectoryURL.path]
        var removed: Data?
        if before.state == .needsRepair {
            let removal: ExactCommandResult
            do {
                removal = try commandRunner.run(ExactCommand(
                    executableURL: claudeExecutableURL,
                    arguments: ["mcp", "remove", Self.serverName, "--scope", "user"],
                    environmentOverrides: environment
                ))
            } catch {
                return refused(.claude, "Claude Code could not remove the outdated MCP entry.")
            }
            guard removal.exitCode == 0 else {
                return refused(.claude, "Claude Code refused to remove the outdated MCP entry.")
            }
            do {
                removed = try PrivateConfigFile.read(paths.claudeConfigURL)
            } catch {
                return unsafeConfig(.claude)
            }
        }

        let addition: ExactCommandResult
        do {
            addition = try commandRunner.run(ExactCommand(
                executableURL: claudeExecutableURL,
                arguments: [
                    "mcp", "add", "--scope", "user", Self.serverName, "--",
                    paths.switchyardExecutableURL.path, "mcp",
                ],
                environmentOverrides: environment
            ))
        } catch {
            restoreClaudeAfterFailedRepair(original: expected, removed: removed)
            return refused(.claude, "Claude Code could not apply the MCP repair.")
        }
        guard addition.exitCode == 0 else {
            restoreClaudeAfterFailedRepair(original: expected, removed: removed)
            return refused(.claude, "Claude Code refused the MCP repair.")
        }
        return inspectClaude()
    }

    private func restoreClaudeAfterFailedRepair(original: Data?, removed: Data?) {
        guard let original, let removed else { return }
        try? PrivateConfigFile.replace(paths.claudeConfigURL, expected: removed, with: original)
    }

    private func helperIsInstalled() -> Bool {
        var status = Darwin.stat()
        return lstat(paths.switchyardExecutableURL.path, &status) == 0 &&
            status.st_mode & S_IFMT == S_IFREG &&
            status.st_uid == geteuid() &&
            Int(status.st_mode) & 0o111 != 0
    }

    private func connected(_ host: AgentHost) -> AgentConnectionStatus {
        AgentConnectionStatus(
            host: host,
            state: .connected,
            detail: "Switchyard MCP uses the installed helper with no stored daemon credential."
        )
    }

    private func missing(_ host: AgentHost) -> AgentConnectionStatus {
        AgentConnectionStatus(
            host: host,
            state: .missing,
            detail: "Switchyard MCP is not configured for \(host.displayName)."
        )
    }

    private func needsRepair(_ host: AgentHost) -> AgentConnectionStatus {
        AgentConnectionStatus(
            host: host,
            state: .needsRepair,
            detail: "The Switchyard MCP entry does not use the exact installed helper and `mcp` argument."
        )
    }

    private func helperUnavailable(_ host: AgentHost) -> AgentConnectionStatus {
        AgentConnectionStatus(
            host: host,
            state: .unavailable,
            detail: "The installed Switchyard helper is unavailable. Repair the app helper first."
        )
    }

    private func unsafeConfig(_ host: AgentHost) -> AgentConnectionStatus {
        refused(host, "The host configuration is not an owner-only regular file; Switchyard will not edit it.")
    }

    private func refused(_ host: AgentHost, _ detail: String) -> AgentConnectionStatus {
        AgentConnectionStatus(host: host, state: .refused, detail: detail)
    }
}

private struct CodexMCPServer: Decodable {
    let name: String
    let enabled: Bool
    let transport: CodexMCPTransport
}

private struct CodexMCPTransport: Decodable {
    let type: String
    let command: String?
    let args: [String]?
    let environmentVariables: [String]
    let cwd: String?
    let hasEnvironment: Bool

    private enum CodingKeys: String, CodingKey {
        case type
        case command
        case args
        case env
        case environmentVariables = "env_vars"
        case cwd
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        type = try values.decode(String.self, forKey: .type)
        command = try values.decodeIfPresent(String.self, forKey: .command)
        args = try values.decodeIfPresent([String].self, forKey: .args)
        environmentVariables = try values.decodeIfPresent([String].self, forKey: .environmentVariables) ?? []
        cwd = try values.decodeIfPresent(String.self, forKey: .cwd)
        if values.contains(.env) {
            hasEnvironment = !(try values.decodeNil(forKey: .env))
        } else {
            hasEnvironment = false
        }
    }
}
