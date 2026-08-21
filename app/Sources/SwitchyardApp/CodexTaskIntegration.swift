import AppKit
import Darwin
import Foundation
import Security
import SwiftUI
import SwitchyardKit

enum CodexTaskIntegrationError: LocalizedError, Equatable {
    case notInstalled
    case invalidWorktree
    case invalidTask
    case bundleNotTrusted
    case queryFailed
    case responseTooLarge
    case timedOut
    case openFailed

    var errorDescription: String? {
        switch self {
        case .notInstalled:
            "The Codex desktop app is not installed."
        case .invalidWorktree:
            "The worktree directory is unavailable."
        case .invalidTask:
            "Codex returned an invalid task identifier."
        case .bundleNotTrusted:
            "The installed Codex app did not pass OpenAI's code-signing requirement."
        case .queryFailed:
            "Codex could not list tasks for this worktree."
        case .responseTooLarge:
            "Codex returned more task data than Switchyard accepts."
        case .timedOut:
            "Codex did not answer the task lookup in time."
        case .openFailed:
            "Codex could not open this task."
        }
    }
}

struct CodexTaskReference: Identifiable, Sendable, Equatable {
    let id: String
    let title: String
    let updatedAt: Date
}

/// Opens one existing Codex task. The app bundle is verified before the URL is
/// handed to it, and the task identifier is encoded as one path component.
struct CodexTaskOpenPlan: Equatable, Sendable {
    static let bundleIdentifier = "com.openai.codex"
    static let teamIdentifier = "2DC432GLL2"
    static let signingRequirement =
        "identifier \"\(bundleIdentifier)\" and anchor apple generic and certificate leaf[subject.OU] = \"\(teamIdentifier)\""

    let applicationURL: URL
    let url: URL

    static func make(applicationURL: URL, taskID: String) throws -> CodexTaskOpenPlan {
        guard validTaskID(taskID) else { throw CodexTaskIntegrationError.invalidTask }
        var components = URLComponents()
        components.scheme = "codex"
        components.host = "threads"
        components.path = "/\(taskID)"
        guard let url = components.url else { throw CodexTaskIntegrationError.invalidTask }
        return CodexTaskOpenPlan(applicationURL: applicationURL, url: url)
    }

    static func verifyBundle(at applicationURL: URL) throws {
        var staticCode: SecStaticCode?
        guard SecStaticCodeCreateWithPath(applicationURL as CFURL, [], &staticCode) == errSecSuccess,
              let staticCode else {
            throw CodexTaskIntegrationError.bundleNotTrusted
        }
        var requirement: SecRequirement?
        guard SecRequirementCreateWithString(signingRequirement as CFString, [], &requirement) == errSecSuccess,
              let requirement else {
            throw CodexTaskIntegrationError.bundleNotTrusted
        }
        let flags = SecCSFlags(rawValue: kSecCSCheckAllArchitectures | kSecCSCheckNestedCode | kSecCSStrictValidate)
        guard SecStaticCodeCheckValidity(staticCode, flags, requirement) == errSecSuccess else {
            throw CodexTaskIntegrationError.bundleNotTrusted
        }
    }

    static func validTaskID(_ value: String) -> Bool {
        guard !value.isEmpty, value.utf8.count <= 128 else { return false }
        return value.utf8.allSatisfy {
            (48 ... 57).contains($0) || (65 ... 90).contains($0) ||
                (97 ... 122).contains($0) || $0 == 45 || $0 == 95
        }
    }
}

struct CodexTaskOpener: Sendable {
    var applicationURL: @Sendable () -> URL? = {
        NSWorkspace.shared.urlForApplication(withBundleIdentifier: CodexTaskOpenPlan.bundleIdentifier)
    }
    var verify: @Sendable (URL) throws -> Void = CodexTaskOpenPlan.verifyBundle(at:)
    var launch: @Sendable (CodexTaskOpenPlan) async throws -> Void = { plan in
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = true
        do {
            _ = try await NSWorkspace.shared.open(
                [plan.url],
                withApplicationAt: plan.applicationURL,
                configuration: configuration
            )
        } catch {
            throw CodexTaskIntegrationError.openFailed
        }
    }

    func open(taskID: String) async throws {
        guard let applicationURL = applicationURL() else { throw CodexTaskIntegrationError.notInstalled }
        try verify(applicationURL)
        try await launch(try CodexTaskOpenPlan.make(applicationURL: applicationURL, taskID: taskID))
    }
}

protocol CodexTaskQuerying: Sendable {
    func tasks(executableURL: URL, worktreePath: String) async throws -> [CodexTaskReference]
}

struct CodexAppServerTaskQuery: CodexTaskQuerying {
    func tasks(executableURL: URL, worktreePath: String) async throws -> [CodexTaskReference] {
        try await Task.detached(priority: .utility) {
            try Self.query(executableURL: executableURL, worktreePath: worktreePath)
        }.value
    }

    private static func query(executableURL: URL, worktreePath: String) throws -> [CodexTaskReference] {
        guard worktreePath.hasPrefix("/"), !worktreePath.contains("\0") else {
            throw CodexTaskIntegrationError.invalidWorktree
        }
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: worktreePath, isDirectory: &isDirectory), isDirectory.boolValue else {
            throw CodexTaskIntegrationError.invalidWorktree
        }

        let process = Process()
        let input = Pipe()
        let output = Pipe()
        let inbox = CodexJSONLineInbox(maximumBytes: 256 * 1024)
        let terminated = DispatchSemaphore(value: 0)
        process.executableURL = executableURL
        process.arguments = ["app-server", "--listen", "stdio://"]
        process.environment = sanitizedEnvironment(executableURL: executableURL)
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        output.fileHandleForReading.readabilityHandler = { handle in
            inbox.append(handle.availableData)
        }
        process.terminationHandler = { _ in
            inbox.terminate()
            terminated.signal()
        }

        do {
            try process.run()
        } catch {
            output.fileHandleForReading.readabilityHandler = nil
            throw CodexTaskIntegrationError.queryFailed
        }

        defer {
            try? input.fileHandleForWriting.close()
            if process.isRunning {
                process.terminate()
                if terminated.wait(timeout: .now() + 1) == .timedOut {
                    Darwin.kill(process.processIdentifier, SIGKILL)
                    _ = terminated.wait(timeout: .now() + 1)
                }
            }
            output.fileHandleForReading.readabilityHandler = nil
        }

        try writeJSONLine(
            [
                "method": "initialize",
                "id": 1,
                "params": [
                    "clientInfo": ["name": "switchyard", "title": "Switchyard", "version": "0.2"],
                    "capabilities": ["experimentalApi": false],
                ],
            ],
            to: input.fileHandleForWriting
        )
        let initialized = try inbox.wait(for: 1, timeout: 5)
        try requireSuccessfulResponse(initialized)

        try writeJSONLine(["method": "initialized", "params": [:]], to: input.fileHandleForWriting)
        try writeJSONLine(
            [
                "method": "thread/list",
                "id": 2,
                "params": [
                    "cwd": worktreePath,
                    "archived": false,
                    "limit": 10,
                    "sortKey": "recency_at",
                    "sortDirection": "desc",
                    "useStateDbOnly": true,
                ],
            ],
            to: input.fileHandleForWriting
        )
        let response = try inbox.wait(for: 2, timeout: 5)
        try requireSuccessfulResponse(response)
        let envelope: ThreadListEnvelope
        do {
            envelope = try JSONDecoder().decode(ThreadListEnvelope.self, from: response)
        } catch {
            throw CodexTaskIntegrationError.queryFailed
        }
        return envelope.result.data.compactMap { thread in
            guard thread.cwd == worktreePath, CodexTaskOpenPlan.validTaskID(thread.id) else { return nil }
            let title = thread.name?.trimmingCharacters(in: .whitespacesAndNewlines).nonEmpty
                ?? thread.preview.firstNonemptyLine
                ?? "Codex Task"
            return CodexTaskReference(
                id: thread.id,
                title: String(title.prefix(120)),
                updatedAt: Date(timeIntervalSince1970: TimeInterval(thread.recencyAt ?? thread.updatedAt))
            )
        }
    }

    private static func writeJSONLine(_ object: [String: Any], to handle: FileHandle) throws {
        do {
            var data = try JSONSerialization.data(withJSONObject: object)
            data.append(0x0A)
            try handle.write(contentsOf: data)
        } catch {
            throw CodexTaskIntegrationError.queryFailed
        }
    }

    private static func requireSuccessfulResponse(_ data: Data) throws {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              object["error"] == nil else {
            throw CodexTaskIntegrationError.queryFailed
        }
    }

    private static func sanitizedEnvironment(executableURL: URL) -> [String: String] {
        let base = ProcessInfo.processInfo.environment
        let allowed = ["HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"]
        var environment = Dictionary(uniqueKeysWithValues: allowed.compactMap { name in
            base[name].map { (name, $0) }
        })
        environment["PATH"] = [
            executableURL.deletingLastPathComponent().path,
            base["HOME"].map { "\($0)/.local/bin" },
            "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
        ].compactMap { $0 }.uniqued().joined(separator: ":")
        return environment
    }
}

private struct ThreadListEnvelope: Decodable {
    let result: ThreadListResult
}

private struct ThreadListResult: Decodable {
    let data: [ThreadListItem]
}

private struct ThreadListItem: Decodable {
    let id: String
    let cwd: String
    let name: String?
    let preview: String
    let updatedAt: Int64
    let recencyAt: Int64?
}

private final class CodexJSONLineInbox: @unchecked Sendable {
    private let condition = NSCondition()
    private let maximumBytes: Int
    private var buffer = Data()
    private var responses: [Int: Data] = [:]
    private var failure: CodexTaskIntegrationError?
    private var ended = false

    init(maximumBytes: Int) {
        self.maximumBytes = maximumBytes
    }

    func append(_ data: Data) {
        condition.lock()
        defer {
            condition.broadcast()
            condition.unlock()
        }
        guard failure == nil, !ended else { return }
        if data.isEmpty {
            ended = true
            return
        }
        guard buffer.count + data.count <= maximumBytes else {
            failure = .responseTooLarge
            return
        }
        buffer.append(data)
        while let newline = buffer.firstIndex(of: 0x0A) {
            let line = Data(buffer[..<newline])
            buffer.removeSubrange(...newline)
            guard !line.isEmpty,
                  let object = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
                  let id = (object["id"] as? NSNumber)?.intValue else { continue }
            responses[id] = line
        }
    }

    func terminate() {
        condition.lock()
        ended = true
        condition.broadcast()
        condition.unlock()
    }

    func wait(for id: Int, timeout: TimeInterval) throws -> Data {
        let deadline = Date().addingTimeInterval(timeout)
        condition.lock()
        defer { condition.unlock() }
        while responses[id] == nil, failure == nil, !ended {
            guard condition.wait(until: deadline) else { throw CodexTaskIntegrationError.timedOut }
        }
        if let response = responses[id] { return response }
        if let failure { throw failure }
        throw CodexTaskIntegrationError.queryFailed
    }
}

struct CodexTaskLocator: Sendable {
    var executableURL: @Sendable () -> URL? = {
        AgentConnectionPaths.standard().codexExecutableURL
    }
    var query: any CodexTaskQuerying = CodexAppServerTaskQuery()

    func tasks(worktreePath: String) async throws -> [CodexTaskReference] {
        guard let executableURL = executableURL() else { throw CodexTaskIntegrationError.notInstalled }
        return try await query.tasks(executableURL: executableURL, worktreePath: worktreePath)
    }
}

/// Read-only bridge from one Switchyard worktree to the Codex tasks whose
/// recorded cwd exactly matches it. Switchyard never creates or mutates the
/// task; it only asks Codex for identifiers and opens the owner's selection.
struct OpenCodexTaskButton: View {
    let worktree: Worktree
    var locator = CodexTaskLocator()
    var opener = CodexTaskOpener()
    @State private var tasks: [CodexTaskReference]?
    @State private var errorMessage: String?

    var body: some View {
        Group {
            if let tasks, tasks.count == 1, let task = tasks.first {
                Button { open(task) } label: {
                    Label("Open Codex Task", systemImage: "sparkles.rectangle.stack")
                }
                .help(task.title)
            } else if let tasks, tasks.count > 1 {
                Menu {
                    ForEach(tasks) { task in
                        Button(task.title) { open(task) }
                    }
                } label: {
                    Label("Open Codex Task", systemImage: "sparkles.rectangle.stack")
                }
                .help("Choose a Codex task recorded for exactly this worktree")
            } else if tasks == nil {
                Label {
                    Text("Finding Codex Task…")
                } icon: {
                    ProgressView().controlSize(.mini)
                }
                .foregroundStyle(.secondary)
            }
        }
        .task(id: worktree.path) { await locate() }
        .alert("Could not open the Codex task", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK") { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "Unknown error")
        }
    }

    private func locate() async {
        tasks = nil
        do {
            let found = try await locator.tasks(worktreePath: worktree.path)
            guard !Task.isCancelled else { return }
            tasks = found
        } catch is CancellationError {
            return
        } catch {
            guard !Task.isCancelled else { return }
            tasks = []
        }
    }

    private func open(_ task: CodexTaskReference) {
        Task {
            do {
                try await opener.open(taskID: task.id)
            } catch {
                errorMessage = (error as? LocalizedError)?.errorDescription ?? "Codex could not open this task."
            }
        }
    }
}

private extension String {
    var nonEmpty: String? { isEmpty ? nil : self }

    var firstNonemptyLine: String? {
        split(whereSeparator: \Character.isNewline)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .first(where: { !$0.isEmpty })
    }
}

private extension Array where Element == String {
    func uniqued() -> [String] {
        var seen = Set<String>()
        return filter { seen.insert($0).inserted }
    }
}
