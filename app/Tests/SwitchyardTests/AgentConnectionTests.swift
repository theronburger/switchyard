import Foundation
import Testing
@testable import SwitchyardKit

struct AgentConnectionTests {
    @Test
    func `standard paths always use standard Claude profile`() {
        let paths = AgentConnectionPaths.standard(environment: [
            "CLAUDE_CONFIG_DIR": "/tmp/should-not-be-used",
        ])

        #expect(paths.claudeConfigURL == FileManager.default.homeDirectoryForCurrentUser.appending(path: ".claude.json"))
        #expect(!paths.usesCustomClaudeConfigDirectory)
    }

    @Test
    func `first run Codex with no config directory remains repairable`() async throws {
        let fixture = try AgentFixture()
        try FileManager.default.removeItem(at: fixture.codexConfig.deletingLastPathComponent())
        let connected = fixture.root.appending(path: "codex-connected")
        let runner = TestExactRunner { command in
            if command.arguments == ["mcp", "list", "--json"] {
                let payload = FileManager.default.fileExists(atPath: connected.path)
                    ? try codexServerList(helper: fixture.helper)
                    : Data("[]".utf8)
                return ExactCommandResult(exitCode: 0, standardOutput: payload)
            }
            #expect(command.arguments == [
                "mcp", "add", "switchyard", "--", fixture.helper.path, "mcp",
            ])
            try writePrivate(Data(), to: connected)
            try writePrivate(Data("[mcp_servers.switchyard]\n".utf8), to: fixture.codexConfig)
            return ExactCommandResult(exitCode: 0)
        }
        let manager = AgentConnectionManager(paths: fixture.paths(), commandRunner: runner)

        let before = try #require(await manager.inspect().status(for: .codex))
        #expect(before.mcpState == .missing)
        #expect(before.canRepair)

        let repaired = try #require(await manager.repair(.codex).status(for: .codex))
        #expect(repaired.mcpState == .connected)
    }

    @Test
    func `repair installs managed skill and uses standard Claude environment`() async throws {
        let fixture = try AgentFixture()
        let source = fixture.root.appending(path: "bundle/skills/switchyard")
        try writePrivate(Data("---\nname: switchyard\n---\n".utf8), to: source.appending(path: "SKILL.md"))
        _ = try ManagedSkill.fingerprint(source)
        let claudeSkill = fixture.root.appending(path: "claude/skills/switchyard")
        try writePrivate(Data("{\"foreign\":true}\n".utf8), to: fixture.claudeConfig)

        let runner = TestExactRunner { command in
            if command.executableURL == fixture.codex {
                #expect(command.arguments == ["mcp", "list", "--json"])
                return ExactCommandResult(exitCode: 0, standardOutput: Data("[]".utf8))
            }
            #expect(command.environmentOverrides.isEmpty)
            #expect(command.arguments == [
                "mcp", "add", "--scope", "user", "switchyard", "--",
                fixture.helper.path, "mcp",
            ])
            let connected = """
            {"foreign":true,"mcpServers":{"switchyard":{"type":"stdio","command":"\(fixture.helper.path)","args":["mcp"],"env":{}}}}
            """
            try writePrivate(Data(connected.utf8), to: fixture.claudeConfig)
            return ExactCommandResult(exitCode: 0)
        }
        let manager = AgentConnectionManager(
            paths: fixture.paths(
                skillSourceURL: source,
                claudeSkillURL: claudeSkill
            ),
            commandRunner: runner
        )

        let before = try #require(await manager.inspect().status(for: .claude))
        #expect(before.mcpState == .missing)
        #expect(before.skillState == .missing)

        let repaired = try #require(await manager.repair(.claude).status(for: .claude))
        #expect(repaired.state == .connected)
        #expect(repaired.mcpState == .connected)
        #expect(repaired.skillState == .connected)
        #expect(FileManager.default.fileExists(atPath: claudeSkill.appending(path: "SKILL.md").path))
        #expect(runner.commands.filter { $0.executableURL == fixture.claude }.count == 1)
    }

    @Test
    func `failed repair leaves concurrent Claude change untouched`() async throws {
        let fixture = try AgentFixture()
        let original = """
        {"mcpServers":{"switchyard":{"type":"stdio","command":"/old/switchyard","args":["mcp"]}},"foreign":true}
        """
        try writePrivate(Data(original.utf8), to: fixture.claudeConfig)
        let concurrent = Data("{\"concurrent\":true}\n".utf8)
        let runner = TestExactRunner { command in
            if command.arguments.contains("remove") {
                try writePrivate(Data("{\"foreign\":true}\n".utf8), to: fixture.claudeConfig)
                return ExactCommandResult(exitCode: 0)
            }
            try writePrivate(concurrent, to: fixture.claudeConfig)
            return ExactCommandResult(exitCode: 1)
        }
        let manager = AgentConnectionManager(paths: fixture.paths(), commandRunner: runner)

        let report = await manager.repair(.claude)

        let status = try #require(report.status(for: .claude))
        #expect(status.state == .refused)
        #expect(try Data(contentsOf: fixture.claudeConfig) == concurrent)
    }

    @Test
    func `rollback collision preserves and reports the recovery file`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let config = root.appending(path: "config.json")
        let expected = Data("{\"after-remove\":true}\n".utf8)
        let original = Data("{\"original\":true}\n".utf8)
        let concurrent = Data("{\"concurrent\":true}\n".utf8)
        try writePrivate(expected, to: config)

        do {
            try PrivateConfigFile.restore(
                config,
                expected: expected,
                original: original,
                beforeReplacement: {
                    try! writePrivate(concurrent, to: config)
                }
            )
            Issue.record("rollback collision was accepted")
        } catch PrivateConfigFileError.recoveryRequired(let path) {
            #expect(try Data(contentsOf: config) == concurrent)
            #expect(try Data(contentsOf: URL(fileURLWithPath: path)) == original)
        } catch {
            Issue.record("unexpected rollback error: \(error)")
        }
    }

    @Test
    func `private config uses no-follow owner-controlled reads`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let config = root.appending(path: "config.toml")
        try Data("model = \"keep\"\n".utf8).write(to: config)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: config.path)
        #expect(try PrivateConfigFile.read(config) != nil)

        let target = root.appending(path: "target.toml")
        try writePrivate(Data("foreign\n".utf8), to: target)
        try FileManager.default.removeItem(at: config)
        try FileManager.default.createSymbolicLink(at: config, withDestinationURL: target)
        do {
            _ = try PrivateConfigFile.read(config)
            Issue.record("symlinked config was accepted")
        } catch {
            #expect(try Data(contentsOf: target) == Data("foreign\n".utf8))
        }
    }

    @Test
    func `established Claude config above the old cap remains inspectable`() async throws {
        let fixture = try AgentFixture()
        var config = Data("{\"mcpServers\":{}}".utf8)
        config.append(Data(repeating: 0x20, count: 3 * 1024 * 1024))
        try writePrivate(config, to: fixture.claudeConfig)
        let runner = TestExactRunner { command in
            if command.executableURL == fixture.codex {
                return ExactCommandResult(exitCode: 0, standardOutput: Data("[]".utf8))
            }
            return ExactCommandResult(exitCode: 0)
        }
        let manager = AgentConnectionManager(paths: fixture.paths(), commandRunner: runner)

        let status = try #require(await manager.inspect().status(for: .claude))

        #expect(status.mcpState == .missing)
        #expect(!status.detail.contains("owner-controlled regular file"))
        #expect(PrivateConfigFile.maximumBytes == 16 * 1024 * 1024)
    }

    @Test
    func `managed skill refuses symlink destination`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let source = root.appending(path: "source")
        let destination = root.appending(path: "skills/switchyard")
        let foreign = root.appending(path: "foreign")
        try writePrivate(Data("---\nname: switchyard\n---\n".utf8), to: source.appending(path: "SKILL.md"))
        try writePrivate(Data("leave me\n".utf8), to: foreign)
        try FileManager.default.createDirectory(at: destination.deletingLastPathComponent(), withIntermediateDirectories: true)
        try FileManager.default.createSymbolicLink(at: destination, withDestinationURL: foreign)
        defer { try? FileManager.default.removeItem(at: root) }

        do {
            try ManagedSkill.install(source: source, destination: destination)
            Issue.record("symlinked skill destination was replaced")
        } catch {
            #expect(try Data(contentsOf: foreign) == Data("leave me\n".utf8))
        }
    }

    @Test
    func `exact runner sanitizes environment and enforces bounds`() throws {
        let environment = FoundationExactArgvRunner.sanitizedEnvironment(
            executableURL: URL(fileURLWithPath: "/opt/homebrew/bin/claude"),
            overrides: [:],
            base: [
                "HOME": "/Users/example",
                "USER": "example",
                "SSH_AUTH_SOCK": "/private/agent.sock",
                "PATH": "/hostile/bin",
            ]
        )
        #expect(environment["PATH"] == "/opt/homebrew/bin:/Users/example/.local/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
        #expect(environment["SSH_AUTH_SOCK"] == nil)

        let runner = FoundationExactArgvRunner(environment: ["HOME": "/Users/example"])
        do {
            _ = try runner.run(ExactCommand(
                executableURL: URL(fileURLWithPath: "/bin/echo"),
                arguments: ["too-large"],
                maximumOutputBytes: 2
            ))
            Issue.record("oversized output was accepted")
        } catch let error as ExactCommandError {
            #expect(error == .outputTooLarge)
        }

        do {
            _ = try runner.run(ExactCommand(
                executableURL: URL(fileURLWithPath: "/bin/sleep"),
                arguments: ["1"],
                timeout: 0.05
            ))
            Issue.record("timed out command completed")
        } catch let error as ExactCommandError {
            #expect(error == .timedOut)
        }
    }

    @Test
    func `concurrent repair requests serialize host mutations`() async throws {
        let fixture = try AgentFixture()
        let codexMarker = fixture.root.appending(path: "codex-connected")
        try writePrivate(Data("{}\n".utf8), to: fixture.claudeConfig)
        let probe = ConcurrencyProbe()
        let runner = TestExactRunner { command in
            probe.enter()
            defer { probe.leave() }
            Thread.sleep(forTimeInterval: 0.02)
            if command.executableURL == fixture.codex {
                if command.arguments == ["mcp", "list", "--json"] {
                    let payload = FileManager.default.fileExists(atPath: codexMarker.path)
                        ? try codexServerList(helper: fixture.helper)
                        : Data("[]".utf8)
                    return ExactCommandResult(exitCode: 0, standardOutput: payload)
                }
                try writePrivate(Data(), to: codexMarker)
                return ExactCommandResult(exitCode: 0)
            }
            let connected = """
            {"mcpServers":{"switchyard":{"type":"stdio","command":"\(fixture.helper.path)","args":["mcp"],"env":{}}}}
            """
            try writePrivate(Data(connected.utf8), to: fixture.claudeConfig)
            return ExactCommandResult(exitCode: 0)
        }
        let manager = AgentConnectionManager(paths: fixture.paths(), commandRunner: runner)

        async let codexRepair = manager.repair(.codex)
        async let claudeRepair = manager.repair(.claude)
        let reports = await [codexRepair, claudeRepair]

        #expect(reports.count == 2)
        #expect(probe.maximumActive == 1)
    }
}

private final class TestExactRunner: ExactArgvRunning, @unchecked Sendable {
    private let lock = NSLock()
    private var recorded: [ExactCommand] = []
    private let operation: (ExactCommand) throws -> ExactCommandResult

    init(operation: @escaping (ExactCommand) throws -> ExactCommandResult) {
        self.operation = operation
    }

    var commands: [ExactCommand] {
        lock.withLock { recorded }
    }

    func run(_ command: ExactCommand) throws -> ExactCommandResult {
        lock.withLock { recorded.append(command) }
        return try operation(command)
    }
}

private final class AgentFixture {
    let root: URL
    let helper: URL
    let codex: URL
    let claude: URL
    let codexConfig: URL
    let claudeConfig: URL

    init() throws {
        root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        helper = root.appending(path: "installed/switchyard")
        codex = root.appending(path: "bin/codex")
        claude = root.appending(path: "bin/claude")
        codexConfig = root.appending(path: "codex/config.toml")
        claudeConfig = root.appending(path: "home/.claude.json")
        try writeExecutable(to: helper)
        try writeExecutable(to: codex)
        try writeExecutable(to: claude)
        try FileManager.default.createDirectory(at: codexConfig.deletingLastPathComponent(), withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: claudeConfig.deletingLastPathComponent(), withIntermediateDirectories: true)
    }

    deinit {
        try? FileManager.default.removeItem(at: root)
    }

    func paths(
        skillSourceURL: URL? = nil,
        codexSkillURL: URL? = nil,
        claudeSkillURL: URL? = nil
    ) -> AgentConnectionPaths {
        AgentConnectionPaths(
            switchyardExecutableURL: helper,
            codexExecutableURL: codex,
            codexConfigURL: codexConfig,
            claudeConfigURL: claudeConfig,
            claudeExecutableURL: claude,
            skillSourceURL: skillSourceURL,
            codexSkillURL: codexSkillURL,
            claudeSkillURL: claudeSkillURL
        )
    }
}

private final class ConcurrencyProbe: @unchecked Sendable {
    private let lock = NSLock()
    private var active = 0
    private var recordedMaximum = 0

    func enter() {
        lock.withLock {
            active += 1
            recordedMaximum = max(recordedMaximum, active)
        }
    }

    func leave() {
        lock.withLock { active -= 1 }
    }

    var maximumActive: Int {
        lock.withLock { recordedMaximum }
    }
}

private func codexServerList(helper: URL) throws -> Data {
    try JSONSerialization.data(withJSONObject: [[
        "name": "switchyard",
        "enabled": true,
        "transport": [
            "type": "stdio",
            "command": helper.path,
            "args": ["mcp"],
            "env_vars": [String](),
            "cwd": NSNull(),
        ] as [String: Any],
    ]])
}

private func writeExecutable(to url: URL) throws {
    try writePrivate(Data("executable\n".utf8), to: url)
    try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
}

private func writePrivate(_ data: Data, to url: URL) throws {
    try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    try data.write(to: url, options: .atomic)
    try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
}
