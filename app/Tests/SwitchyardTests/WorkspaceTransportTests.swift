import Foundation
import Testing
@testable import SwitchyardApp

struct WorkspaceTransportTests {
    @Test func canonicalGoSnapshotDecodesWithoutPreviousContractModels() throws {
        let source = URL(fileURLWithPath: #filePath)
        var repository = source.deletingLastPathComponent()
        while repository.path != "/" && !FileManager.default.fileExists(atPath: repository.appending(path: "contracts/v3/status.json").path) {
            repository.deleteLastPathComponent()
        }
        let snapshot = try WorkspaceClient.decode(WorkspaceSnapshot.self, data: Data(contentsOf: repository.appending(path: "contracts/v3/status.json")))
        #expect(snapshot.version == 3)
        let workspaces = try #require(snapshot.workspaces)
        #expect(workspaces.count == 2)
        #expect(Set(workspaces.map(\.path)).count == 2)
        let running = try #require(workspaces.first { $0.run?.state == "running" })
        let failed = try #require(workspaces.first { $0.run?.state == "failed" })
        #expect(running.choices.services == ["web"])
        #expect(failed.choices.services == ["api"])
        #expect(running.run?.error == nil)
        #expect(failed.run?.error?.isEmpty == false)
        #expect(failed.run?.urls?.isEmpty == true)
    }

    @Test(.enabled(if: ProcessInfo.processInfo.environment["SWITCHYARD_TEST_HELPER"] != nil))
    func actualGoHelperAcceptsNativeVersionAndAuthenticationHeaders() async throws {
        let helper = try #require(ProcessInfo.processInfo.environment["SWITCHYARD_TEST_HELPER"])
        let root = FileManager.default.temporaryDirectory.appending(path: "switchyard-native-transport-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: helper)
        process.arguments = ["daemon", "--root", root.path]
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        defer { if process.isRunning { process.terminate() }; process.waitUntilExit() }
        let client = WorkspaceClient(root: root)
        var loaded: WorkspaceSnapshot?
        for _ in 0..<100 {
            if let snapshot = try? await client.status() { loaded = snapshot; break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let status = try #require(loaded, "Native client could not complete the versioned, authenticated Go status request")
        #expect(status.version == 3)
        #expect((status.repositories ?? []).isEmpty)
        #expect((status.workspaces ?? []).isEmpty)
        let config = try await client.configuration()
        let decoded = try #require(JSONSerialization.jsonObject(with: Data(config.utf8)) as? [String: Any])
        #expect(decoded["schemaVersion"] as? Int == 1)
        try await client.saveConfiguration(config)
        let after = try await client.status()
        #expect(after.revision > status.revision)
        #expect(after.instanceId == status.instanceId)
        await #expect(throws: (any Error).self) { _ = try await client.prunePlan(path: root.path) }
        #expect(FileManager.default.fileExists(atPath: root.path))
    }

    @Test func developmentRootOverrideHasItsOwnLaunchRegistration() {
        let first = AppInstallation.resolve(arguments: ["Switchyard", "--root", "/tmp/one"], channel: .development)
        let second = AppInstallation.resolve(arguments: ["Switchyard", "--root", "/tmp/two"], channel: .development)
        #expect(first.root.path == "/tmp/one")
        #expect(first.label != second.label)
        #expect(first.label != "com.theronburger.switchyard.daemon")
        #expect(first.agentEntry == "switchyard-rebuild")
        #expect(!first.channel.permitsUpdates)
    }
}
