import Foundation
import Testing
@testable import SwitchyardKit

// MARK: - Release builds never honour SWITCHYARD_DAEMON_BINARY

struct DaemonBinaryOverrideTests {
    private static func emptyBundle() throws -> (Bundle, URL) {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let bundleURL = root.appending(path: "Empty.app", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(
            at: bundleURL.appending(path: "Contents/Resources"),
            withIntermediateDirectories: true
        )
        let bundle = try #require(Bundle(url: bundleURL))
        return (bundle, root)
    }

    @Test
    func `development channel honours the daemon override`() throws {
        let (bundle, root) = try Self.emptyBundle()
        defer { try? FileManager.default.removeItem(at: root) }
        let provider = BundleDaemonBinaryProvider(
            bundle: bundle,
            environment: ["SWITCHYARD_DAEMON_BINARY": "/tmp/dev-switchyard"],
            channel: .development
        )
        #expect(provider.honoursDevelopmentOverride)
        #expect(try provider.daemonBinary().sourceURL.path == "/tmp/dev-switchyard")
    }

    @Test
    func `release channel ignores the daemon override and fails closed`() throws {
        let (bundle, root) = try Self.emptyBundle()
        defer { try? FileManager.default.removeItem(at: root) }
        let provider = BundleDaemonBinaryProvider(
            bundle: bundle,
            environment: ["SWITCHYARD_DAEMON_BINARY": "/tmp/attacker-switchyard"],
            channel: .release
        )
        #expect(!provider.honoursDevelopmentOverride)
        #expect(throws: DaemonBinarySourceError.notPackaged(channel: .release)) {
            try provider.daemonBinary()
        }
        #expect(!DaemonBinarySourceError.notPackaged(channel: .release).description.contains("SWITCHYARD_DAEMON_BINARY"))
    }

    @Test
    func `release channel prefers the packaged daemon even when an override is set`() throws {
        let (bundle, root) = try Self.emptyBundle()
        defer { try? FileManager.default.removeItem(at: root) }
        let packaged = try #require(bundle.resourceURL).appending(path: "SwitchyardDaemon")
        try Data("daemon\n".utf8).write(to: packaged)
        let provider = BundleDaemonBinaryProvider(
            bundle: bundle,
            environment: ["SWITCHYARD_DAEMON_BINARY": "/tmp/attacker-switchyard"],
            channel: .release
        )
        #expect(try provider.daemonBinary().sourceURL.standardizedFileURL == packaged.standardizedFileURL)
    }

    @Test
    func `an embedded release channel cannot be downgraded by the environment`() {
        let channel = SwitchyardChannel.resolve(
            infoDictionary: ["SwitchyardChannel": "release"],
            environment: ["SWITCHYARD_CHANNEL": "development"]
        )
        #expect(channel == .release)
        #expect(!BundleDaemonBinaryProvider(environment: ["SWITCHYARD_DAEMON_BINARY": "/x"], channel: channel).honoursDevelopmentOverride)
    }
}

// MARK: - ManagedSkill ownership

struct ManagedSkillOwnershipTests {
    @Test
    func `install writes an ownership marker that the fingerprint ignores`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "bundle/skills/switchyard")
        try writePrivateFile(Data("---\nname: switchyard\n---\n".utf8), to: source.appending(path: "SKILL.md"))
        let destination = root.appending(path: "claude/skills/switchyard")

        try ManagedSkill.install(source: source, destination: destination)

        #expect(ManagedSkill.isOwned(destination))
        #expect(!ManagedSkill.isOwned(source))
        #expect(try ManagedSkill.fingerprintOwned(destination) == ManagedSkill.fingerprint(source))
        let marker = destination.appending(path: ManagedSkill.ownershipMarkerName)
        #expect(try Data(contentsOf: marker) == ManagedSkill.ownershipMarkerContents)
    }

    @Test
    func `repair never replaces a user-authored skill directory`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "bundle/skills/switchyard")
        try writePrivateFile(Data("---\nname: switchyard\n---\nbundled\n".utf8), to: source.appending(path: "SKILL.md"))
        let destination = root.appending(path: "claude/skills/switchyard")
        let userManifest = Data("---\nname: switchyard\n---\nmy own notes\n".utf8)
        try writePrivateFile(userManifest, to: destination.appending(path: "SKILL.md"))
        try writePrivateFile(Data("extra\n".utf8), to: destination.appending(path: "notes/extra.md"))

        var reportedPath: String?
        do {
            try ManagedSkill.install(source: source, destination: destination)
        } catch ManagedSkillError.foreignSkill(let path) {
            reportedPath = path
        }
        #expect(reportedPath == destination.path)

        // Every user byte survives, and no staging or backup directory remains.
        #expect(try Data(contentsOf: destination.appending(path: "SKILL.md")) == userManifest)
        #expect(FileManager.default.fileExists(atPath: destination.appending(path: "notes/extra.md").path))
        #expect(!ManagedSkill.isOwned(destination))
        let siblings = try FileManager.default.contentsOfDirectory(atPath: destination.deletingLastPathComponent().path)
        #expect(siblings == ["switchyard"])
    }

    @Test
    func `a user directory without a manifest is also left alone`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "bundle/skills/switchyard")
        try writePrivateFile(Data("---\nname: switchyard\n---\n".utf8), to: source.appending(path: "SKILL.md"))
        let destination = root.appending(path: "codex/skills/switchyard")
        try writePrivateFile(Data("draft\n".utf8), to: destination.appending(path: "README.md"))

        var reportedPath: String?
        do {
            try ManagedSkill.install(source: source, destination: destination)
        } catch ManagedSkillError.foreignSkill(let path) {
            reportedPath = path
        }
        #expect(reportedPath == destination.path)
        #expect(FileManager.default.fileExists(atPath: destination.appending(path: "README.md").path))
        #expect(!FileManager.default.fileExists(atPath: destination.appending(path: "SKILL.md").path))
    }

    @Test
    func `repair adopts an unmarked tree identical to the bundled release and replaces an owned one`() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = root.appending(path: "bundle/skills/switchyard")
        let manifest = Data("---\nname: switchyard\n---\nv1\n".utf8)
        try writePrivateFile(manifest, to: source.appending(path: "SKILL.md"))
        let destination = root.appending(path: "codex/skills/switchyard")
        try writePrivateFile(manifest, to: destination.appending(path: "SKILL.md"))
        #expect(!ManagedSkill.isOwned(destination))

        try ManagedSkill.install(source: source, destination: destination)
        #expect(ManagedSkill.isOwned(destination))

        // An owned tree with local edits is replaced by an explicit repair.
        try writePrivateFile(Data("edited\n".utf8), to: destination.appending(path: "SKILL.md"))
        let updated = Data("---\nname: switchyard\n---\nv2\n".utf8)
        try writePrivateFile(updated, to: source.appending(path: "SKILL.md"))
        try ManagedSkill.install(source: source, destination: destination)
        #expect(try Data(contentsOf: destination.appending(path: "SKILL.md")) == updated)
        #expect(ManagedSkill.isOwned(destination))
    }

    @Test
    func `Connection Doctor reports a foreign skill directory as refused and never repairs it`() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let helper = root.appending(path: "installed/switchyard")
        let codex = root.appending(path: "bin/codex")
        let claude = root.appending(path: "bin/claude")
        for executable in [helper, codex, claude] {
            try writePrivateFile(Data("executable\n".utf8), to: executable)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: executable.path)
        }
        let claudeConfig = root.appending(path: "home/.claude.json")
        let connected = """
        {"mcpServers":{"switchyard":{"type":"stdio","command":"\(helper.path)","args":["mcp"],"env":{}}}}
        """
        try writePrivateFile(Data(connected.utf8), to: claudeConfig)
        let source = root.appending(path: "bundle/skills/switchyard")
        try writePrivateFile(Data("---\nname: switchyard\n---\nbundled\n".utf8), to: source.appending(path: "SKILL.md"))
        let claudeSkill = root.appending(path: "claude/skills/switchyard")
        let userManifest = Data("---\nname: switchyard\n---\nmine\n".utf8)
        try writePrivateFile(userManifest, to: claudeSkill.appending(path: "SKILL.md"))

        let runner = RecordingExactRunner { command in
            if command.executableURL == codex {
                return ExactCommandResult(exitCode: 0, standardOutput: Data("[]".utf8))
            }
            Issue.record("no Claude command should run for a refused skill")
            return ExactCommandResult(exitCode: 1)
        }
        let manager = AgentConnectionManager(
            paths: AgentConnectionPaths(
                switchyardExecutableURL: helper,
                codexExecutableURL: codex,
                codexConfigURL: root.appending(path: "codex/config.toml"),
                claudeConfigURL: claudeConfig,
                claudeExecutableURL: claude,
                skillSourceURL: source,
                codexSkillURL: nil,
                claudeSkillURL: claudeSkill
            ),
            commandRunner: runner
        )

        let status = try #require(await manager.inspect().status(for: .claude))
        #expect(status.skillState == .refused)
        #expect(!status.canRepair)
        #expect(status.detail.contains(claudeSkill.path))

        _ = await manager.repair(.claude)
        #expect(try Data(contentsOf: claudeSkill.appending(path: "SKILL.md")) == userManifest)
        #expect(!ManagedSkill.isOwned(claudeSkill))
    }
}

// MARK: - Operation waits poll status only

private final class RecordingExactRunner: ExactArgvRunning, @unchecked Sendable {
    private let handler: @Sendable (ExactCommand) throws -> ExactCommandResult
    private let lock = NSLock()
    private var recorded: [ExactCommand] = []

    init(_ handler: @escaping @Sendable (ExactCommand) throws -> ExactCommandResult) {
        self.handler = handler
    }

    var commands: [ExactCommand] { lock.withLock { recorded } }

    func run(_ command: ExactCommand) throws -> ExactCommandResult {
        lock.withLock { recorded.append(command) }
        return try handler(command)
    }
}

private func writePrivateFile(_ data: Data, to url: URL) throws {
    try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
    try data.write(to: url, options: .atomic)
    try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
}
