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
    public let mcpState: AgentConnectionState
    public let skillState: AgentConnectionState
    public let detail: String

    public var id: AgentHost { host }
    public var canRepair: Bool { mcpState.canRepair || skillState.canRepair }

    public init(
        host: AgentHost,
        state: AgentConnectionState,
        detail: String,
        mcpState: AgentConnectionState? = nil,
        skillState: AgentConnectionState? = nil
    ) {
        self.host = host
        self.state = state
        self.mcpState = mcpState ?? state
        self.skillState = skillState ?? state
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
    public let skillSourceURL: URL?
    public let codexExecutableURL: URL?
    public let codexConfigURL: URL
    public let codexSkillURL: URL?
    public let claudeConfigURL: URL
    public let claudeExecutableURL: URL?
    public let claudeSkillURL: URL?
    public let claudeConfigDirectoryURL: URL
    public let usesCustomClaudeConfigDirectory: Bool

    public init(
        switchyardExecutableURL: URL,
        codexExecutableURL: URL?,
        codexConfigURL: URL,
        claudeConfigURL: URL,
        claudeExecutableURL: URL? = nil,
        claudeConfigDirectoryURL: URL? = nil,
        skillSourceURL: URL? = nil,
        codexSkillURL: URL? = nil,
        claudeSkillURL: URL? = nil
    ) {
        self.switchyardExecutableURL = switchyardExecutableURL
        self.skillSourceURL = skillSourceURL
        self.codexExecutableURL = codexExecutableURL
        self.codexConfigURL = codexConfigURL
        self.codexSkillURL = codexSkillURL
        self.claudeConfigURL = claudeConfigURL
        self.claudeExecutableURL = claudeExecutableURL
        self.claudeSkillURL = claudeSkillURL
        self.claudeConfigDirectoryURL = claudeConfigDirectoryURL ?? claudeConfigURL.deletingLastPathComponent()
        self.usesCustomClaudeConfigDirectory = claudeConfigDirectoryURL != nil
    }

    public static func standard(
        fileManager: FileManager = .default,
        environment _: [String: String] = ProcessInfo.processInfo.environment,
        bundle: Bundle = .main
    ) -> AgentConnectionPaths {
        let home = fileManager.homeDirectoryForCurrentUser
        let codexRoot = home.appending(path: ".codex")
        let claudeRoot = home.appending(path: ".claude")
        let codexExecutable = findExecutable([
            URL(fileURLWithPath: "/Applications/ChatGPT.app/Contents/Resources/codex"),
            home.appending(path: ".local/bin/codex"),
            URL(fileURLWithPath: "/opt/homebrew/bin/codex"),
            URL(fileURLWithPath: "/usr/local/bin/codex"),
        ], fileManager: fileManager)
        let claudeExecutable = findExecutable([
            home.appending(path: ".local/bin/claude"),
            URL(fileURLWithPath: "/opt/homebrew/bin/claude"),
            URL(fileURLWithPath: "/usr/local/bin/claude"),
        ], fileManager: fileManager)
        let skillSource = bundle.resourceURL?.appending(path: "skills/switchyard")
        return AgentConnectionPaths(
            switchyardExecutableURL: LaunchAgentPaths.standard(fileManager: fileManager).installedBinaryURL,
            codexExecutableURL: codexExecutable,
            codexConfigURL: codexRoot.appending(path: "config.toml"),
            claudeConfigURL: home.appending(path: ".claude.json"),
            claudeExecutableURL: claudeExecutable,
            skillSourceURL: skillSource,
            codexSkillURL: codexRoot.appending(path: "skills/switchyard"),
            claudeSkillURL: claudeRoot.appending(path: "skills/switchyard")
        )
    }

    private static func findExecutable(_ candidates: [URL], fileManager: FileManager) -> URL? {
        candidates.first { fileManager.isExecutableFile(atPath: $0.path) }
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
        inspectAll()
    }

    public func repair(_ host: AgentHost) async -> AgentConnectionReport {
        let repaired = host == .codex ? repairCodex() : repairClaude()
        return AgentConnectionReport(statuses: AgentHost.allCases.map { candidate in
            if candidate == host { return repaired }
            return candidate == .codex ? inspectCodex() : inspectClaude()
        })
    }

    public func repairAll() async -> AgentConnectionReport {
        AgentConnectionReport(statuses: [repairCodex(), repairClaude()])
    }

    private func inspectAll() -> AgentConnectionReport {
        AgentConnectionReport(statuses: [inspectCodex(), inspectClaude()])
    }

    private func inspectCodex() -> AgentConnectionStatus {
        guard paths.codexExecutableURL != nil else {
            return unavailable(.codex, "The Codex CLI could not be found in a known app or executable location.")
        }
        return combined(
            host: .codex,
            mcp: inspectCodexMCP(),
            skill: inspectSkill(for: .codex)
        )
    }

    private func inspectCodexMCP() -> ConnectionComponent {
        guard helperIsInstalled() else { return helperUnavailableComponent() }
        guard let codexExecutableURL = paths.codexExecutableURL else {
            return ConnectionComponent(state: .unavailable, detail: "The Codex CLI is unavailable.")
        }
        do {
            _ = try PrivateConfigFile.read(paths.codexConfigURL)
        } catch {
            return unsafeConfigComponent("Codex")
        }
        let result: ExactCommandResult
        do {
            result = try commandRunner.run(ExactCommand(
                executableURL: codexExecutableURL,
                arguments: ["mcp", "list", "--json"],
                timeout: 5
            ))
        } catch {
            return ConnectionComponent(state: .refused, detail: "Codex MCP configuration could not be inspected safely.")
        }
        guard result.exitCode == 0,
              let servers = try? JSONDecoder().decode([CodexMCPServer].self, from: result.standardOutput) else {
            return ConnectionComponent(state: .refused, detail: "Codex did not provide a valid MCP configuration; no changes will be made.")
        }
        guard let server = servers.first(where: { $0.name == Self.serverName }) else {
            return missingMCPComponent()
        }
        guard server.enabled,
              server.transport.type == "stdio",
              server.transport.command == paths.switchyardExecutableURL.path,
              server.transport.args == ["mcp"],
              !server.transport.hasEnvironment,
              server.transport.environmentVariables.isEmpty,
              server.transport.cwd == nil else {
            return needsRepairMCPComponent()
        }
        return connectedMCPComponent()
    }

    private func repairCodex() -> AgentConnectionStatus {
        let before = inspectCodex()
        guard before.canRepair, let codexExecutableURL = paths.codexExecutableURL else { return before }
        if before.skillState.canRepair, let failure = repairSkillFailure(for: .codex) {
            return refused(.codex, failure, mcp: before.mcpState, skill: .refused)
        }
        guard before.mcpState.canRepair else { return inspectCodex() }

        let original: Data?
        do {
            original = try PrivateConfigFile.read(paths.codexConfigURL)
        } catch {
            return mcpRefused(.codex, "Codex configuration is not safe to repair.")
        }
        guard (try? PrivateConfigFile.read(paths.codexConfigURL)) == original else {
            return mcpRefused(.codex, "Codex configuration changed during repair; no changes were made.")
        }

        var rollbackExpected = original
        if before.mcpState == .needsRepair {
            guard runMutation(executableURL: codexExecutableURL, arguments: ["mcp", "remove", Self.serverName]) else {
                return mcpRefused(.codex, "Codex refused to remove the outdated MCP entry.")
            }
            do {
                rollbackExpected = try PrivateConfigFile.read(paths.codexConfigURL)
            } catch {
                return mcpRefused(.codex, "Codex configuration became unsafe during repair.")
            }
        }
        guard runMutation(
            executableURL: codexExecutableURL,
            arguments: ["mcp", "add", Self.serverName, "--", paths.switchyardExecutableURL.path, "mcp"]
        ) else {
            return rollbackFailure(
                host: .codex,
                configURL: paths.codexConfigURL,
                expected: rollbackExpected,
                original: original,
                commandFailure: "Codex refused the MCP repair."
            )
        }
        return inspectCodex()
    }

    private func inspectClaude() -> AgentConnectionStatus {
        var mcp = inspectClaudeMCP()
        if paths.claudeExecutableURL == nil, mcp.state.canRepair {
            mcp = ConnectionComponent(
                state: .unavailable,
                detail: "The Claude Code CLI could not be found in a known executable location."
            )
        }
        return combined(
            host: .claude,
            mcp: mcp,
            skill: inspectSkill(for: .claude)
        )
    }

    private func inspectClaudeMCP() -> ConnectionComponent {
        guard helperIsInstalled() else { return helperUnavailableComponent() }
        let data: Data?
        do {
            data = try PrivateConfigFile.read(paths.claudeConfigURL)
        } catch PrivateConfigFileError.tooLarge {
            return oversizedConfigComponent("Claude Code")
        } catch {
            return unsafeConfigComponent("Claude Code")
        }
        guard let data else { return missingMCPComponent() }
        guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return ConnectionComponent(state: .refused, detail: "Claude Code configuration is not valid JSON; no changes will be made.")
        }
        guard let serversValue = root["mcpServers"] else { return missingMCPComponent() }
        guard let servers = serversValue as? [String: Any] else {
            return ConnectionComponent(state: .refused, detail: "Claude Code uses an unsupported MCP configuration shape; no changes will be made.")
        }
        guard let serverValue = servers[Self.serverName] else { return missingMCPComponent() }
        guard let server = serverValue as? [String: Any] else { return needsRepairMCPComponent() }
        let environmentIsEmpty: Bool
        if let environmentValue = server["env"] {
            environmentIsEmpty = (environmentValue as? [String: Any])?.isEmpty == true
        } else {
            environmentIsEmpty = true
        }
        guard server["type"] as? String == "stdio",
              server["command"] as? String == paths.switchyardExecutableURL.path,
              server["args"] as? [String] == ["mcp"],
              environmentIsEmpty else {
            return needsRepairMCPComponent()
        }
        return connectedMCPComponent()
    }

    private func repairClaude() -> AgentConnectionStatus {
        let before = inspectClaude()
        guard before.canRepair, let claudeExecutableURL = paths.claudeExecutableURL else { return before }
        if before.skillState.canRepair, let failure = repairSkillFailure(for: .claude) {
            return refused(.claude, failure, mcp: before.mcpState, skill: .refused)
        }
        guard before.mcpState.canRepair else { return inspectClaude() }

        let original: Data?
        do {
            original = try PrivateConfigFile.read(paths.claudeConfigURL)
        } catch {
            return mcpRefused(.claude, "Claude Code configuration is not safe to repair.")
        }
        guard (try? PrivateConfigFile.read(paths.claudeConfigURL)) == original else {
            return mcpRefused(.claude, "Claude Code configuration changed during repair; no changes were made.")
        }

        let environment = paths.usesCustomClaudeConfigDirectory
            ? ["CLAUDE_CONFIG_DIR": paths.claudeConfigDirectoryURL.path]
            : [:]
        var rollbackExpected = original
        if before.mcpState == .needsRepair {
            guard runMutation(
                executableURL: claudeExecutableURL,
                arguments: ["mcp", "remove", Self.serverName, "--scope", "user"],
                environmentOverrides: environment
            ) else {
                return mcpRefused(.claude, "Claude Code refused to remove the outdated MCP entry.")
            }
            do {
                rollbackExpected = try PrivateConfigFile.read(paths.claudeConfigURL)
            } catch {
                return mcpRefused(.claude, "Claude Code configuration became unsafe during repair.")
            }
        }
        guard runMutation(
            executableURL: claudeExecutableURL,
            arguments: ["mcp", "add", "--scope", "user", Self.serverName, "--", paths.switchyardExecutableURL.path, "mcp"],
            environmentOverrides: environment
        ) else {
            return rollbackFailure(
                host: .claude,
                configURL: paths.claudeConfigURL,
                expected: rollbackExpected,
                original: original,
                commandFailure: "Claude Code refused the MCP repair."
            )
        }
        return inspectClaude()
    }

    private func runMutation(
        executableURL: URL,
        arguments: [String],
        environmentOverrides: [String: String] = [:]
    ) -> Bool {
        do {
            let result = try commandRunner.run(ExactCommand(
                executableURL: executableURL,
                arguments: arguments,
                environmentOverrides: environmentOverrides,
                timeout: 15
            ))
            return result.exitCode == 0
        } catch {
            return false
        }
    }

    private func rollbackFailure(
        host: AgentHost,
        configURL: URL,
        expected: Data?,
        original: Data?,
        commandFailure: String
    ) -> AgentConnectionStatus {
        do {
            try PrivateConfigFile.restore(configURL, expected: expected, original: original)
            return mcpRefused(host, "\(commandFailure) Its prior configuration was restored.")
        } catch PrivateConfigFileError.changed {
            return mcpRefused(host, "\(commandFailure) A concurrent configuration change was left untouched.")
        } catch PrivateConfigFileError.recoveryRequired(let path) {
            return mcpRefused(
                host,
                "\(commandFailure) The prior configuration was preserved at \(path); restore it manually after reviewing the concurrent file."
            )
        } catch {
            return mcpRefused(host, "\(commandFailure) Its prior configuration could not be restored safely.")
        }
    }

    private func inspectSkill(for host: AgentHost) -> ConnectionComponent {
        guard let source = paths.skillSourceURL else {
            return ConnectionComponent(state: .connected, detail: "Managed skill installation is not configured for this build.")
        }
        let destination = host == .codex ? paths.codexSkillURL : paths.claudeSkillURL
        guard let destination else {
            return ConnectionComponent(state: .unavailable, detail: "The managed skill destination is unavailable.")
        }
        do {
            let sourceFingerprint = try ManagedSkill.fingerprint(source)
            guard ManagedSkill.exists(destination) else {
                return ConnectionComponent(state: .missing, detail: "The managed Switchyard skill is not installed.")
            }
            let destinationFingerprint = try ManagedSkill.fingerprintOwned(destination)
            let owned = ManagedSkill.isOwned(destination)
            if sourceFingerprint != destinationFingerprint {
                guard owned else {
                    return ConnectionComponent(
                        state: .refused,
                        detail: "A skill directory Switchyard did not install already exists at \(destination.path). Switchyard never replaces it; move it aside to install the managed skill."
                    )
                }
                return ConnectionComponent(state: .needsRepair, detail: "The managed Switchyard skill is out of date.")
            }
            if !owned {
                return ConnectionComponent(state: .needsRepair, detail: "The managed Switchyard skill is current but not yet marked as Switchyard-owned.")
            }
            return ConnectionComponent(state: .connected, detail: "The managed Switchyard skill is current.")
        } catch {
            return ConnectionComponent(state: .refused, detail: "The managed Switchyard skill directory is unsafe.")
        }
    }

    private func repairSkillFailure(for host: AgentHost) -> String? {
        guard let source = paths.skillSourceURL,
              let destination = host == .codex ? paths.codexSkillURL : paths.claudeSkillURL else {
            return "The managed Switchyard skill is not available in this build."
        }
        do {
            try ManagedSkill.install(source: source, destination: destination)
            return nil
        } catch ManagedSkillError.recoveryRequired(let path) {
            return "The prior managed skill was preserved at \(path); restore it manually after reviewing the concurrent directory."
        } catch ManagedSkillError.foreignSkill(let path) {
            return "A skill directory Switchyard did not install already exists at \(path). It was left untouched; move it aside to install the managed skill."
        } catch {
            return "The managed Switchyard skill could not be installed safely."
        }
    }

    private func helperIsInstalled() -> Bool {
        var status = Darwin.stat()
        return lstat(paths.switchyardExecutableURL.path, &status) == 0 &&
            status.st_mode & S_IFMT == S_IFREG &&
            status.st_uid == geteuid() &&
            Int(status.st_mode) & 0o111 != 0
    }

    private func combined(
        host: AgentHost,
        mcp: ConnectionComponent,
        skill: ConnectionComponent
    ) -> AgentConnectionStatus {
        let state: AgentConnectionState
        if mcp.state.canRepair || skill.state.canRepair {
            state = mcp.state == .needsRepair || skill.state == .needsRepair ? .needsRepair : .missing
        } else if mcp.state == .refused || skill.state == .refused {
            state = .refused
        } else if mcp.state == .unavailable || skill.state == .unavailable {
            state = .unavailable
        } else {
            state = .connected
        }
        return AgentConnectionStatus(
            host: host,
            state: state,
            detail: "MCP: \(mcp.detail) Skill: \(skill.detail)",
            mcpState: mcp.state,
            skillState: skill.state
        )
    }

    private func unavailable(_ host: AgentHost, _ detail: String) -> AgentConnectionStatus {
        AgentConnectionStatus(
            host: host,
            state: .unavailable,
            detail: detail,
            mcpState: .unavailable,
            skillState: .unavailable
        )
    }

    private func mcpRefused(_ host: AgentHost, _ detail: String) -> AgentConnectionStatus {
        combined(
            host: host,
            mcp: ConnectionComponent(state: .refused, detail: detail),
            skill: inspectSkill(for: host)
        )
    }

    private func refused(
        _ host: AgentHost,
        _ detail: String,
        mcp: AgentConnectionState = .refused,
        skill: AgentConnectionState = .refused
    ) -> AgentConnectionStatus {
        AgentConnectionStatus(host: host, state: .refused, detail: detail, mcpState: mcp, skillState: skill)
    }

    private func connectedMCPComponent() -> ConnectionComponent {
        ConnectionComponent(state: .connected, detail: "Switchyard MCP uses the installed helper with no stored daemon credential.")
    }

    private func missingMCPComponent() -> ConnectionComponent {
        ConnectionComponent(state: .missing, detail: "Switchyard MCP is not configured.")
    }

    private func needsRepairMCPComponent() -> ConnectionComponent {
        ConnectionComponent(state: .needsRepair, detail: "The MCP entry does not use the exact installed helper and `mcp` argument.")
    }

    private func helperUnavailableComponent() -> ConnectionComponent {
        ConnectionComponent(state: .unavailable, detail: "The installed Switchyard helper is unavailable.")
    }

    private func unsafeConfigComponent(_ hostName: String) -> ConnectionComponent {
        ConnectionComponent(state: .refused, detail: "\(hostName) configuration is not an owner-controlled regular file.")
    }

    private func oversizedConfigComponent(_ hostName: String) -> ConnectionComponent {
        ConnectionComponent(
            state: .refused,
            detail: "\(hostName) configuration exceeds Switchyard's 16 MiB safety limit; no changes will be made."
        )
    }
}

private struct ConnectionComponent {
    let state: AgentConnectionState
    let detail: String
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
            hasEnvironment = try !values.decodeNil(forKey: .env)
        } else {
            hasEnvironment = false
        }
    }
}
