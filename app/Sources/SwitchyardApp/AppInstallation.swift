import Foundation
import CryptoKit

// Update delivery keeps the released bundle's established channel boundary.
enum SwitchyardChannel: String, Sendable {
    case development, release
    static func resolve() -> Self {
        Self(rawValue: Bundle.main.object(forInfoDictionaryKey: "SwitchyardChannel") as? String ?? "development") ?? .development
    }
    var permitsUpdates: Bool { self == .release }
    var bundleIdentifier: String { self == .release ? "com.theronburger.switchyard" : "com.theronburger.switchyard.rebuild" }
}

struct AppInstallation: Sendable {
    let root: URL
    let label: String
    let channel: SwitchyardChannel
    var helper: URL { root.appending(path: "bin/switchyard") }
    var agentEntry: String { channel == .release ? "switchyard" : "switchyard-rebuild" }

    static func resolve(arguments: [String] = CommandLine.arguments, channel: SwitchyardChannel = .resolve()) -> Self {
        let support = FileManager.default.homeDirectoryForCurrentUser.appending(path: "Library/Application Support")
        var root = support.appending(path: channel == .release ? "Switchyard" : "Switchyard Rebuild")
        var label = channel == .release ? "com.theronburger.switchyard.daemon" : "com.theronburger.switchyard.rebuild.daemon"
        if let index = arguments.firstIndex(of: "--root"), arguments.indices.contains(index + 1) {
            root = URL(fileURLWithPath: arguments[index + 1]).standardizedFileURL
            let suffix = SHA256.hash(data: Data(root.path.utf8)).prefix(6).map { String(format: "%02x", $0) }.joined()
            label += ".\(suffix)"
        }
        return Self(root: root, label: label, channel: channel)
    }

    func ensureDaemon(repair: Bool) throws {
        let manager = FileManager.default
        let bundled = Bundle.main.bundleURL.appending(path: "Contents/Resources/SwitchyardDaemon")
        guard manager.isExecutableFile(atPath: bundled.path) else {
            throw WorkspaceAPIError(message: "This build is missing its bundled helper. Open the packaged Switchyard app.")
        }
        try manager.createDirectory(at: helper.deletingLastPathComponent(), withIntermediateDirectories: true)
        try manager.createDirectory(at: root.appending(path: "daemon"), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let bytes = try Data(contentsOf: bundled, options: .mappedIfSafe)
        let changed = (try? Data(contentsOf: helper, options: .mappedIfSafe)) != bytes
        if changed {
            try bytes.write(to: helper, options: .atomic)
            try manager.setAttributes([.posixPermissions: 0o755], ofItemAtPath: helper.path)
        }
        let agents = manager.homeDirectoryForCurrentUser.appending(path: "Library/LaunchAgents")
        try manager.createDirectory(at: agents, withIntermediateDirectories: true)
        let plistURL = agents.appending(path: label + ".plist")
        let plist: [String: Any] = [
            "Label": label, "ProgramArguments": [helper.path, "daemon", "--root", root.path],
            "RunAtLoad": true, "KeepAlive": true, "ProcessType": "Interactive",
            "AssociatedBundleIdentifiers": [Bundle.main.bundleIdentifier ?? channel.bundleIdentifier],
            "StandardOutPath": root.appending(path: "daemon/launchd.stdout.log").path,
            "StandardErrorPath": root.appending(path: "daemon/launchd.stderr.log").path,
        ]
        let data = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        let registrationChanged = (try? Data(contentsOf: plistURL)) != data
        let domain = "gui/\(getuid())"
        let service = domain + "/" + label
        if registrationChanged {
            _ = try? AgentTools.execute(URL(fileURLWithPath: "/bin/launchctl"), ["bootout", service])
            try data.write(to: plistURL, options: .atomic)
        }
        if (try? AgentTools.execute(URL(fileURLWithPath: "/bin/launchctl"), ["print", service])) != 0 {
            guard try AgentTools.execute(URL(fileURLWithPath: "/bin/launchctl"), ["bootstrap", domain, plistURL.path]) == 0 else {
                throw WorkspaceAPIError(message: "macOS could not start the helper. Allow Switchyard in Login Items & Extensions, then Repair.")
            }
        } else if changed || repair {
            guard try AgentTools.execute(URL(fileURLWithPath: "/bin/launchctl"), ["kickstart", "-k", service]) == 0 else {
                throw WorkspaceAPIError(message: "macOS could not restart the helper. Check Login Items & Extensions.")
            }
        }
    }
}

enum AgentTools {
    static func executable(_ name: String) -> URL? {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let candidates = name == "codex" ? [
            URL(fileURLWithPath: "/Applications/Codex.app/Contents/Resources/codex"),
            home.appending(path: "Applications/Codex.app/Contents/Resources/codex"),
            home.appending(path: ".local/bin/codex"), URL(fileURLWithPath: "/opt/homebrew/bin/codex"), URL(fileURLWithPath: "/usr/local/bin/codex"),
        ] : [home.appending(path: ".local/bin/claude"), URL(fileURLWithPath: "/opt/homebrew/bin/claude"), URL(fileURLWithPath: "/usr/local/bin/claude")]
        return candidates.first { FileManager.default.isExecutableFile(atPath: $0.path) }
    }

    static func connect(_ name: String, installation: AppInstallation) throws {
        guard let tool = executable(name) else { throw WorkspaceAPIError(message: "\(name.capitalized) is not installed.") }
        let entry = installation.agentEntry
        let arguments = name == "codex"
            ? ["mcp", "add", entry, "--", installation.helper.path, "mcp", "--root", installation.root.path]
            : ["mcp", "add", "--scope", "user", "--transport", "stdio", entry, "--", installation.helper.path, "mcp", "--root", installation.root.path]
        let removal = name == "codex" ? ["mcp", "remove", entry] : ["mcp", "remove", "--scope", "user", entry]
        _ = try execute(tool, removal)
        guard try execute(tool, arguments) == 0 else { throw WorkspaceAPIError(message: "\(name.capitalized) could not register Switchyard. Check its CLI login and retry.") }
    }

    static func execute(_ executable: URL, _ arguments: [String]) throws -> Int32 {
        let process = Process()
        process.executableURL = executable
        process.arguments = arguments
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        let deadline = Date().addingTimeInterval(20)
        while process.isRunning && Date() < deadline { Thread.sleep(forTimeInterval: 0.05) }
        if process.isRunning {
            process.terminate()
            throw WorkspaceAPIError(message: "The setup command timed out.")
        }
        return process.terminationStatus
    }
}
