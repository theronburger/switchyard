import AppKit
import Foundation
import SwiftUI
import SwitchyardKit

enum ZedWorkspaceError: LocalizedError, Equatable {
    case notInstalled
    case invalidWorktree
    case cliUnavailable
    case launchFailed

    var errorDescription: String? {
        switch self {
        case .notInstalled: "Zed is not installed."
        case .invalidWorktree: "The worktree directory is unavailable."
        case .cliUnavailable: "The installed Zed app does not contain its CLI."
        case .launchFailed: "Zed could not open this worktree."
        }
    }
}

struct ZedLaunchPlan: Equatable {
    let executable: URL
    let arguments: [String]

    static func make(applicationURL: URL, worktreePath: String) throws -> ZedLaunchPlan {
        guard worktreePath.hasPrefix("/") else { throw ZedWorkspaceError.invalidWorktree }
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: worktreePath, isDirectory: &isDirectory), isDirectory.boolValue else {
            throw ZedWorkspaceError.invalidWorktree
        }
        let executable = applicationURL
            .appending(path: "Contents", directoryHint: .isDirectory)
            .appending(path: "MacOS", directoryHint: .isDirectory)
            .appending(path: "cli", directoryHint: .notDirectory)
        guard FileManager.default.isExecutableFile(atPath: executable.path) else {
            throw ZedWorkspaceError.cliUnavailable
        }
        return ZedLaunchPlan(executable: executable, arguments: ["-n", worktreePath])
    }
}

struct ZedWorkspaceLauncher {
    var applicationURL: () -> URL? = {
        NSWorkspace.shared.urlForApplication(withBundleIdentifier: "dev.zed.Zed")
    }
    var launch: (ZedLaunchPlan) throws -> Void = { plan in
        let process = Process()
        process.executableURL = plan.executable
        process.arguments = plan.arguments
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw ZedWorkspaceError.launchFailed
        }
    }

    func open(worktreePath: String) throws {
        guard let applicationURL = applicationURL() else { throw ZedWorkspaceError.notInstalled }
        try launch(ZedLaunchPlan.make(applicationURL: applicationURL, worktreePath: worktreePath))
    }
}

struct OpenInZedButton: View {
    let worktree: Worktree
    @State private var errorMessage: String?

    var body: some View {
        Button {
            do {
                try ZedWorkspaceLauncher().open(worktreePath: worktree.path)
            } catch {
                errorMessage = (error as? LocalizedError)?.errorDescription ?? "Zed could not open this worktree."
            }
        } label: {
            Label("Open in Zed", systemImage: "arrow.up.right.square")
        }
        .help("Open this existing Git worktree in a new Zed window")
        .alert("Could not open Zed", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK") { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "Unknown error")
        }
    }
}
