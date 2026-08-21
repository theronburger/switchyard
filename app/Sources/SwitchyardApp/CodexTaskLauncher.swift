import AppKit
import Foundation
import Security
import SwiftUI
import SwitchyardKit

enum CodexTaskError: LocalizedError, Equatable {
    case notInstalled
    case invalidWorktree
    case bundleNotTrusted
    case launchFailed

    var errorDescription: String? {
        switch self {
        case .notInstalled:
            "The Codex desktop app is not installed."
        case .invalidWorktree:
            "The worktree directory is unavailable."
        case .bundleNotTrusted:
            "The installed app registered for Codex did not pass OpenAI's code-signing requirement."
        case .launchFailed:
            "Codex could not open a task in this worktree."
        }
    }
}

/// The exact launch the installed Codex CLI performs for `codex app PATH`:
/// open the verified `com.openai.codex` bundle with the documented
/// `codex://threads/new?path=<absolute path>` deep link. Switchyard mirrors
/// that contract instead of guessing a branch, and passes only the path.
struct CodexLaunchPlan: Equatable, Sendable {
    static let bundleIdentifier = "com.openai.codex"
    static let teamIdentifier = "2DC432GLL2"
    static let signingRequirement =
        "identifier \"\(bundleIdentifier)\" and anchor apple generic and certificate leaf[subject.OU] = \"\(teamIdentifier)\""

    let applicationURL: URL
    let url: URL

    static func make(applicationURL: URL, worktreePath: String) throws -> CodexLaunchPlan {
        guard worktreePath.hasPrefix("/"), !worktreePath.contains("\0") else { throw CodexTaskError.invalidWorktree }
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: worktreePath, isDirectory: &isDirectory), isDirectory.boolValue else {
            throw CodexTaskError.invalidWorktree
        }
        var components = URLComponents()
        components.scheme = "codex"
        components.host = "threads"
        components.path = "/new"
        // Match the Codex CLI's `form_urlencoded` query exactly: it also
        // escapes `+`, `&`, and `=`, which URLComponents would leave literal.
        components.percentEncodedQuery = Self.formEncoded("path", worktreePath)
        guard let url = components.url else { throw CodexTaskError.invalidWorktree }
        return CodexLaunchPlan(applicationURL: applicationURL, url: url)
    }

    static func formEncoded(_ name: String, _ value: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._*")
        func encode(_ string: String) -> String {
            (string.addingPercentEncoding(withAllowedCharacters: allowed) ?? "")
                .replacingOccurrences(of: "%20", with: "+")
        }
        return "\(encode(name))=\(encode(value))"
    }

    /// Evaluates Codex's own signing requirement against the bundle before
    /// anything is handed to it.
    static func verifyBundle(at applicationURL: URL) throws {
        var staticCode: SecStaticCode?
        guard SecStaticCodeCreateWithPath(applicationURL as CFURL, [], &staticCode) == errSecSuccess,
              let staticCode else {
            throw CodexTaskError.bundleNotTrusted
        }
        var requirement: SecRequirement?
        guard SecRequirementCreateWithString(signingRequirement as CFString, [], &requirement) == errSecSuccess,
              let requirement else {
            throw CodexTaskError.bundleNotTrusted
        }
        let flags = SecCSFlags(rawValue: kSecCSCheckAllArchitectures | kSecCSCheckNestedCode | kSecCSStrictValidate)
        guard SecStaticCodeCheckValidity(staticCode, flags, requirement) == errSecSuccess else {
            throw CodexTaskError.bundleNotTrusted
        }
    }
}

struct CodexTaskLauncher: Sendable {
    var applicationURL: @Sendable () -> URL? = {
        NSWorkspace.shared.urlForApplication(withBundleIdentifier: CodexLaunchPlan.bundleIdentifier)
    }
    var verify: @Sendable (URL) throws -> Void = CodexLaunchPlan.verifyBundle(at:)
    var launch: @Sendable (CodexLaunchPlan) async throws -> Void = { plan in
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        do {
            _ = try await NSWorkspace.shared.open(
                [plan.url],
                withApplicationAt: plan.applicationURL,
                configuration: configuration
            )
        } catch {
            throw CodexTaskError.launchFailed
        }
    }

    var isInstalled: Bool { applicationURL() != nil }

    func plan(worktreePath: String) throws -> CodexLaunchPlan {
        guard let applicationURL = applicationURL() else { throw CodexTaskError.notInstalled }
        try verify(applicationURL)
        return try CodexLaunchPlan.make(applicationURL: applicationURL, worktreePath: worktreePath)
    }

    func open(worktreePath: String) async throws {
        try await launch(try plan(worktreePath: worktreePath))
    }
}

/// Prepares the exact worktree through the daemon when needed, then opens a
/// new Codex task there. Switchyard infers nothing from the deep link itself:
/// the app records one explicit, conservative handoff lease before opening
/// the task, releases it again if the launch fails, and only the owner ends a
/// lease that protects a launched task.
struct StartCodexTaskButton: View {
    @Bindable var model: AppModel
    let worktree: Worktree
    var launcher = CodexTaskLauncher()
    @State private var errorMessage: String?

    private var isPreparingThisWorktree: Bool {
        if case .preparing(let worktreeId, _) = model.agentHandoffState { return worktreeId == worktree.id }
        return false
    }

    var body: some View {
        Button {
            Task { await start() }
        } label: {
            if isPreparingThisWorktree {
                Label {
                    Text("Preparing…")
                } icon: {
                    ProgressView().controlSize(.mini)
                }
            } else {
                Label("Start Codex Task", systemImage: "sparkles.rectangle.stack")
            }
        }
        .disabled(!canStart)
        .help(helpText)
        .alert("Could not start a Codex task", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK") { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "Unknown error")
        }
    }

    private var canStart: Bool {
        guard launcher.isInstalled, !model.agentHandoffState.isActive else { return false }
        return model.worktreeIsPrepared(worktree) || model.isFixtureMode || model.canPrepareWorktree
    }

    private var helpText: String {
        guard launcher.isInstalled else { return "Install the Codex desktop app to start tasks from Switchyard." }
        if model.worktreeIsPrepared(worktree) || model.isFixtureMode {
            return "Open a new Codex task in exactly this worktree."
        }
        return "Prepare this worktree through the daemon, then open a new Codex task in it."
    }

    private func start() async {
        guard await model.prepareWorktreeForHandoff(worktree) else {
            if case .failed(_, let message) = model.agentHandoffState {
                errorMessage = message
                model.dismissAgentHandoff()
            }
            return
        }
        // The launch is fire-and-forget, so the lease is the only durable
        // evidence that this worktree was handed to a task. The model records
        // it before the launch and rolls it back if the launch fails.
        let launcher = self.launcher
        let path = worktree.path
        let outcome = await model.handOffWorktree(worktree, holderLabel: "Codex task") {
            try await launcher.open(worktreePath: path)
        }
        errorMessage = outcome.failureMessage
    }
}
