import Foundation
import CryptoKit

struct AppUpgrade {
    let root: URL
    let helper: URL
    let bundled: URL

    func prepare() throws {
        guard try AgentTools.execute(bundled, ["upgrade", "check", "--root", root.path]) == 0 else {
            throw WorkspaceAPIError(message: "The upgrade configuration needs repair. Your existing setup is unchanged; check upgrade/config.json in the configuration folder.")
        }
        let manager = FileManager.default
        let legacy = root.appending(path: "configuration.yaml")
        let backup = root.appending(path: "upgrade/previous")
        if manager.fileExists(atPath: legacy.path) {
            try manager.createDirectory(at: backup, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            if !manager.fileExists(atPath: backup.appending(path: "configuration.yaml").path) {
                try manager.copyItem(at: legacy, to: backup.appending(path: "configuration.yaml"))
            }
            if manager.isExecutableFile(atPath: helper.path) {
                let version = try? AgentTools.capture(helper, ["version", "--json"])
                let info = version.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]
                if (info?["schemaVersion"] as? Int) != 3 {
                    try stopPreviousRuns()
                    if !manager.fileExists(atPath: backup.appending(path: "switchyard").path) {
                        try manager.copyItem(at: helper, to: backup.appending(path: "switchyard"))
                    }
                }
            }
        }
        guard try AgentTools.execute(bundled, ["upgrade", "apply", "--root", root.path]) == 0 else {
            throw WorkspaceAPIError(message: "The upgrade configuration could not be installed. Your previous configuration is preserved.")
        }
    }

    func stopPreviousRuns() throws {
        let data = try AgentTools.capture(helper, ["status", "--all", "--json"])
        let ids = try Self.activeEnvironmentIDs(data)
        for id in ids {
            guard try AgentTools.execute(helper, ["stop", id, "--if-running"]) == 0 else {
                throw WorkspaceAPIError(message: "A previous run could not stop. The helper has not been replaced; retry the upgrade after stopping that run.")
            }
        }
        if ids.isEmpty { return }
        let deadline = Date().addingTimeInterval(90)
        repeat {
            if try Self.activeEnvironmentIDs(AgentTools.capture(helper, ["status", "--all", "--json"])).isEmpty { return }
            Thread.sleep(forTimeInterval: 0.3)
        } while Date() < deadline
        throw WorkspaceAPIError(message: "Previous runs are still stopping. Reopen Switchyard to finish the upgrade.")
    }

    static func activeEnvironmentIDs(_ data: Data) throws -> [String] {
        struct Snapshot: Decodable {
            struct Environment: Decodable { let id: String; let desiredState: String; let observedState: String }
            let schemaVersion: Int
            let environments: [Environment]
        }
        let snapshot = try JSONDecoder().decode(Snapshot.self, from: data)
        guard snapshot.schemaVersion == 2 else { throw WorkspaceAPIError(message: "The previous helper returned an incompatible status.") }
        return snapshot.environments.filter { $0.desiredState != "stopped" || $0.observedState != "stopped" }.map(\.id)
    }

    static func repairBundledSkill(home: URL, bundledSkill: URL) throws {
        let destination = home.appending(path: ".codex/skills/switchyard/SKILL.md")
        guard let bytes = try? Data(contentsOf: destination) else { return }
        let digest = SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
        guard digest == "7cff95a0080a27864623ec9cfdf02607cb2e2380136a13a47843d36642ed34f9" else { return }
        let backup = destination.deletingLastPathComponent().appending(path: "SKILL.before-0.3.md")
        if !FileManager.default.fileExists(atPath: backup.path) { try bytes.write(to: backup, options: .atomic) }
        try Data(contentsOf: bundledSkill).write(to: destination, options: .atomic)
    }
}

extension AgentTools {
    static func capture(_ executable: URL, _ arguments: [String]) throws -> Data {
        let file = FileManager.default.temporaryDirectory.appending(path: "switchyard-upgrade-\(UUID().uuidString)")
        FileManager.default.createFile(atPath: file.path, contents: nil, attributes: [.posixPermissions: 0o600])
        defer { try? FileManager.default.removeItem(at: file) }
        let output = try FileHandle(forWritingTo: file)
        defer { try? output.close() }
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        try process.run()
        let deadline = Date().addingTimeInterval(20)
        while process.isRunning && Date() < deadline { Thread.sleep(forTimeInterval: 0.05) }
        if process.isRunning { process.terminate(); throw WorkspaceAPIError(message: "The previous helper is not responding. Reopen the previous app and retry the upgrade.") }
        guard process.terminationStatus == 0,
              let size = try file.resourceValues(forKeys: [.fileSizeKey]).fileSize, size <= 4 * 1024 * 1024 else {
            throw WorkspaceAPIError(message: "Could not inspect the previous helper. Its installation is unchanged.")
        }
        return try Data(contentsOf: file)
    }
}
