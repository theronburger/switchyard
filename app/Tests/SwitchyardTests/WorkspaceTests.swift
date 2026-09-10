import Foundation
import Testing
@testable import SwitchyardApp

@MainActor struct WorkspaceTests {
    @Test func independentActionsKeepTheirPathWhenRepliesArriveBackwards() async throws {
        let api = FakeAPI(holdRuns: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        model.selection = pathA
        let first = Task { await model.runWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.requests.count == 1 }
        #expect(model.busy[pathA] != nil)
        #expect(model.busy[pathB] == nil)
        model.selection = pathB
        let second = Task { await model.runWorkspace(path: pathB, choices: choicesB) }
        try await eventually { await api.requests.count == 2 }
        await model.runWorkspace(path: pathA, choices: choicesA)
        #expect(await api.requests.count == 2)
        await api.release(pathB)
        await second.value
        #expect(model.run(for: try #require(model.workspace(at: pathB)))?.path == pathB)
        #expect(model.busy[pathA] != nil)
        await api.release(pathA, fails: true)
        await first.value
        #expect(model.errors[pathA] == "Setup failed in workspace A.")
        #expect(model.errors[pathB] == nil)
        #expect(model.selection == pathB)
    }

    @Test func lateStartReceiptCannotReplaceNewerRunningSnapshot() async throws {
        let api = FakeAPI(holdRuns: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let action = Task { await model.runWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.requests.count == 1 }
        let newer = makeSnapshot(revision: 20, runA: makeRun(pathA, state: "running"))
        await api.publish(newer)
        await model.refresh()
        await api.release(pathA)
        await action.value
        #expect(model.run(for: try #require(model.workspace(at: pathA)))?.state == "running")
    }

    @Test func stopWinsOverDelayedStartReceiptAndLeavesOtherWorkspaceRunning() async throws {
        let api = FakeAPI(holdRuns: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let start = Task { await model.runWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.requests.count == 1 }
        await model.stopWorkspace(path: pathA)
        await api.release(pathA)
        await start.value
        #expect(model.run(for: try #require(model.workspace(at: pathA)))?.state == "stopped")
        #expect(model.run(for: try #require(model.workspace(at: pathB)))?.state == "running")
        #expect(await api.stops == [pathA])
    }

    @Test func restartDoesNotStartAgainAfterAnotherStop() async throws {
        let api = FakeAPI(holdFirstStop: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let restart = Task { await model.restartWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.stops.count == 1 }
        await model.stopWorkspace(path: pathA)
        await api.releaseStop()
        await restart.value
        #expect(await api.requests.isEmpty)
    }

    @Test func statusReadStartedBeforeStopIsDiscardedAndRepeated() async throws {
        let api = FakeAPI()
        await api.publish(makeSnapshot(runA: makeRun(pathA, state: "running")))
        let model = WorkspaceModel(api: api)
        await model.refresh()
        await api.holdNextStatus()
        let read = Task { await model.refresh() }
        try await eventually { await api.hasStatusRead }
        await model.stopWorkspace(path: pathA)
        #expect(model.run(for: try #require(model.workspace(at: pathA)))?.state == "stopped")
        await api.releaseStatus()
        await read.value
        #expect(model.workspace(at: pathA)?.run?.state == "stopped")
        #expect(model.run(for: try #require(model.workspace(at: pathA)))?.state == "stopped")
    }

    @Test func interruptedRestartDoesNotRunOnReplacementDaemon() async throws {
        let api = FakeAPI(holdFirstStop: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let restart = Task { await model.restartWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.stops.count == 1 }
        #expect(model.accept(makeSnapshot(instance: "replacement")))
        await api.releaseStop()
        await restart.value
        #expect(await api.requests.isEmpty)
        #expect(model.busy[pathA] == nil)
    }

    @Test func daemonRestartRetiresPriorSnapshotsAndReceipts() async throws {
        let api = FakeAPI(holdRuns: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let start = Task { await model.runWorkspace(path: pathA, choices: choicesA) }
        try await eventually { await api.requests.count == 1 }
        let restarted = makeSnapshot(revision: 1, instance: "daemon-new")
        #expect(model.accept(restarted))
        await api.publish(restarted)
        #expect(!model.accept(makeSnapshot(revision: 99)))
        await api.release(pathA)
        await start.value
        #expect(model.snapshot?.instanceId == "daemon-new")
        #expect(model.run(for: try #require(model.workspace(at: pathA))) == nil)
        #expect(model.errors[pathA] == nil)
    }

    @Test func revisionsNeverMoveBackwardsWithinOneDaemon() {
        let model = WorkspaceModel(api: FakeAPI())
        #expect(model.accept(makeSnapshot(revision: 5)))
        #expect(!model.accept(makeSnapshot(revision: 4)))
        #expect(model.snapshot?.revision == 5)
    }

    @Test func daemonChoicesReplaceAcknowledgedEditsAndRemainAuthoritativeAfterRestart() async throws {
        let suite = "Switchyard.NewWorkspaceTests.\(UUID().uuidString)"
        let preferences = try #require(UserDefaults(suiteName: suite))
        defer { preferences.removePersistentDomain(forName: suite) }
        let api = FakeAPI()
        let model = WorkspaceModel(api: api, preferences: preferences)
        await model.refresh()
        model.selection = pathB
        model.remember(path: pathA, choices: choicesB)
        try await eventually { model.localChoices[pathA] == nil }
        let external = WorkspaceChoices(target: "local", services: [])
        try await api.choices(path: pathA, choices: external)
        await model.refresh()
        let workspace = try #require(model.workspace(at: pathA))
        let repository = try #require(model.repository(for: workspace))
        #expect(model.choices(for: workspace, repository: repository) == external)
        let reopened = WorkspaceModel(api: api, preferences: preferences)
        await reopened.refresh()
        #expect(reopened.selection == pathB)
        #expect(reopened.localChoices.isEmpty)
        #expect(reopened.choices(for: try #require(reopened.workspace(at: pathA)), repository: repository) == external)
        await reopened.runWorkspace(path: pathA, choices: external)
        #expect(await api.requests.isEmpty)
    }

    @Test func oldRunLogsCannotAppearUnderNewRun() async throws {
        let api = FakeAPI(holdLogs: true)
        let model = WorkspaceModel(api: api)
        await model.refresh()
        let load = Task { await model.loadLogs(path: pathB) }
        try await eventually { await api.hasLogRead }
        let newer = makeSnapshot(revision: 20, runB: makeRun(pathB, id: "run-new", state: "running"))
        #expect(model.accept(newer))
        await api.releaseLogs(WorkspaceLog(runId: "run-b", text: "old output"))
        await load.value
        #expect(model.logs[pathB] == nil)
        #expect(model.logs[pathA] == nil)
    }

    @Test func serverFailurePreservesLastKnownWorkspaceSelection() async throws {
        let api = FakeAPI()
        let model = WorkspaceModel(api: api)
        await model.refresh()
        model.selection = pathB
        await api.failStatus()
        await model.refresh()
        #expect(model.snapshot != nil)
        #expect(model.selection == pathB)
        #expect(model.connectionError != nil)
    }

    private func eventually(_ condition: () async -> Bool) async throws {
        for _ in 0..<400 {
            if await condition() { return }
            try await Task.sleep(for: .milliseconds(5))
        }
        throw WorkspaceAPIError(message: "Test condition timed out.")
    }
}

private let pathA = "/synthetic/workspace-a"
private let pathB = "/synthetic/workspace-b"
private let choicesA = WorkspaceChoices(target: "local", services: ["web"])
private let choicesB = WorkspaceChoices(target: "preview", services: ["api"])

private func makeWorkspace(_ path: String, choices: WorkspaceChoices, run: WorkspaceRun?) -> Workspace {
    Workspace(path: path, repositoryId: "repository", branch: "feature/\(path.suffix(1))", head: "abcdef12345",
              primary: false, locked: false, missing: false, dirty: false, unpushed: false, gitError: nil, choices: choices, run: run)
}
private func makeRun(_ path: String, id: String? = nil, state: String = "starting") -> WorkspaceRun {
    WorkspaceRun(id: id ?? "run-\(path.suffix(1))", path: path, target: "local", requested: ["web"], state: state,
                 step: "Preparing workspace", error: nil, startedAt: Date(), services: [], urls: [:])
}
private func makeSnapshot(revision: UInt64 = 1, instance: String = "daemon-old", runA: WorkspaceRun? = nil,
                          runB: WorkspaceRun? = makeRun(pathB, state: "running")) -> WorkspaceSnapshot {
    WorkspaceSnapshot(version: 3, instanceId: instance, revision: revision, repositories: [
        RepositorySummary(id: "repository", name: "Sample", path: "/synthetic/repository", defaultTarget: "local",
                          targets: [TargetSummary(id: "local", name: "Local", confirm: nil), TargetSummary(id: "preview", name: "Preview", confirm: true)],
                          services: [ServiceSummary(id: "web", name: "Web", kind: "web", dependencies: ["api"]), ServiceSummary(id: "api", name: "API", kind: nil, dependencies: nil)], error: nil),
    ], workspaces: [makeWorkspace(pathA, choices: choicesA, run: runA), makeWorkspace(pathB, choices: choicesB, run: runB)])
}

private actor FakeAPI: WorkspaceAPI {
    private var snapshot = makeSnapshot()
    private let holdRuns: Bool
    private let holdFirstStop: Bool
    private let holdLogs: Bool
    private var runContinuations: [String: CheckedContinuation<Bool, Never>] = [:]
    private var stopContinuation: CheckedContinuation<Void, Never>?
    private var logContinuation: CheckedContinuation<WorkspaceLog, Never>?
    private var statusFails = false
    private var holdStatus = false
    private var statusContinuation: CheckedContinuation<Void, Never>?
    var hasStatusRead: Bool { statusContinuation != nil }
    private(set) var requests: [RunRequest] = []
    private(set) var stops: [String] = []
    var hasLogRead: Bool { logContinuation != nil }
    init(holdRuns: Bool = false, holdFirstStop: Bool = false, holdLogs: Bool = false) {
        self.holdRuns = holdRuns; self.holdFirstStop = holdFirstStop; self.holdLogs = holdLogs
    }
    func status() async throws -> WorkspaceSnapshot {
        if statusFails { throw WorkspaceAPIError(message: "Connection interrupted.") }
        let captured = snapshot
        if holdStatus {
            holdStatus = false
            await withCheckedContinuation { statusContinuation = $0 }
        }
        return captured
    }
    func holdNextStatus() { holdStatus = true }
    func releaseStatus() { statusContinuation?.resume(); statusContinuation = nil }
    func failStatus() { statusFails = true }
    func publish(_ snapshot: WorkspaceSnapshot) { self.snapshot = snapshot }
    func run(_ request: RunRequest) async throws -> WorkspaceRun {
        requests.append(request)
        let receipt = makeRun(request.path)
        updateRun(path: request.path, run: receipt)
        if holdRuns {
            let fails = await withCheckedContinuation { runContinuations[request.path] = $0 }
            if fails { throw WorkspaceAPIError(message: "Setup failed in workspace A.") }
        }
        return receipt
    }
    func release(_ path: String, fails: Bool = false) { runContinuations.removeValue(forKey: path)?.resume(returning: fails) }
    func stop(path: String) async throws -> WorkspaceRun {
        stops.append(path)
        if holdFirstStop && stops.count == 1 { await withCheckedContinuation { stopContinuation = $0 } }
        let run = makeRun(path, state: "stopped")
        updateRun(path: path, run: run)
        return run
    }
    private func updateRun(path: String, run: WorkspaceRun) {
        let workspaces = (snapshot.workspaces ?? []).map { workspace in
            workspace.path == path ? makeWorkspace(path, choices: workspace.choices, run: run) : workspace
        }
        snapshot = WorkspaceSnapshot(version: snapshot.version, instanceId: snapshot.instanceId, revision: snapshot.revision + 1,
                                     repositories: snapshot.repositories, workspaces: workspaces)
    }
    func releaseStop() { stopContinuation?.resume(); stopContinuation = nil }
    func choices(path: String, choices: WorkspaceChoices) async throws {
        let workspaces = (snapshot.workspaces ?? []).map { workspace in
            workspace.path == path ? makeWorkspace(path, choices: choices, run: workspace.run) : workspace
        }
        snapshot = WorkspaceSnapshot(version: snapshot.version, instanceId: snapshot.instanceId, revision: snapshot.revision + 1,
                                     repositories: snapshot.repositories, workspaces: workspaces)
    }
    func logs(path: String) async throws -> WorkspaceLog {
        if holdLogs { return await withCheckedContinuation { logContinuation = $0 } }
        return WorkspaceLog(runId: "run-\(path.suffix(1))", text: "output")
    }
    func releaseLogs(_ logs: WorkspaceLog) { logContinuation?.resume(returning: logs); logContinuation = nil }
    func configuration() async throws -> String { "{}" }
    func saveConfiguration(_ json: String) async throws {}
    func prunePlan(path: String) async throws -> PrunePlan { PrunePlan(path: path, branch: "test", blockers: []) }
    func prune(path: String) async throws {}
    func create(_ request: CreateRequest) async throws {}
}
