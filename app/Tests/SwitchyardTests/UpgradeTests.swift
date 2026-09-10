import Foundation
import Testing
@testable import SwitchyardApp

struct UpgradeTests {
    @Test func onlyUnstoppedLegacyEnvironmentsAreSelected() throws {
        let data = Data(#"{"schemaVersion":2,"environments":[{"id":"done","desiredState":"stopped","observedState":"stopped"},{"id":"active","desiredState":"running","observedState":"running"},{"id":"pending","desiredState":"stopped","observedState":"stopping"}]}"#.utf8)
        #expect(try AppUpgrade.activeEnvironmentIDs(data) == ["active", "pending"])
        #expect(throws: (any Error).self) { try AppUpgrade.activeEnvironmentIDs(Data("{}".utf8)) }
    }
    @Test func oldHelperStopsBeforeApplyingAndFailuresKeepInstallation() throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try Data("legacy".utf8).write(to: root.appending(path: "configuration.yaml"))
        let helper = root.appending(path: "old-helper")
        let bundled = root.appending(path: "new-helper")
        let oldScript = """
        #!/bin/sh
        cd "$(dirname "$0")"
        case "$1" in
        version) echo usage ;;
        stop) test ! -e fail || exit 1; touch stopped ;;
        status)
          state=running; test ! -e stopped || state=stopped
          printf '{"schemaVersion":2,"environments":[{"id":"fixture","desiredState":"%s","observedState":"%s"}]}' "$state" "$state"
          ;;
        esac
        """
        let newScript = """
        #!/bin/sh
        cd "$(dirname "$0")"
        if [ "$2" = apply ]; then test -e stopped || exit 1; touch applied; fi
        """
        for (file, script) in [(helper, oldScript), (bundled, newScript)] {
            try Data(script.utf8).write(to: file)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file.path)
        }
        let upgrade = AppUpgrade(root: root, helper: helper, bundled: bundled)
        try Data().write(to: root.appending(path: "fail"))
        #expect(throws: (any Error).self) { try upgrade.prepare() }
        #expect(!FileManager.default.fileExists(atPath: root.appending(path: "applied").path))
        #expect(try String(contentsOf: helper, encoding: .utf8) == oldScript)
        try FileManager.default.removeItem(at: root.appending(path: "fail"))
        try upgrade.prepare()
        #expect(FileManager.default.fileExists(atPath: root.appending(path: "applied").path))
        #expect(try String(contentsOf: root.appending(path: "upgrade/previous/configuration.yaml"), encoding: .utf8) == "legacy")
    }
    @Test func customizedSkillsRemainUntouched() throws {
        let home = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: home) }
        let skill = home.appending(path: ".codex/skills/switchyard/SKILL.md")
        try FileManager.default.createDirectory(at: skill.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("my custom instructions".utf8).write(to: skill)
        try AppUpgrade.repairBundledSkill(home: home, bundledSkill: home.appending(path: "missing"))
        #expect(try String(contentsOf: skill, encoding: .utf8) == "my custom instructions")
    }
}
