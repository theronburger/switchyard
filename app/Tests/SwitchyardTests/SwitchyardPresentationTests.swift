import AppKit
import SwiftUI
import Testing
@testable import SwitchyardApp
@testable import SwitchyardKit

@MainActor
struct SwitchyardPresentationTests {
    @Test
    func `repository runtime drives targets and the complete service picker`() async throws {
        let model = try await fixtureModel()
        let repository = try #require(model.snapshot?.repositories.first)
        let options = StartEnvironmentOptions(repository: repository)

        #expect(options.defaultTargetId == "testing")
        #expect(options.targets.map(\.id) == ["development", "testing", "demo", "production"])
        #expect(options.targets.filter(\.warnOnStart).map(\.id) == ["demo", "production"])
        #expect(options.services.count == 18)
        #expect(options.availableServiceIDs == Set(options.services.map(\.id)))
        #expect(options.services.allSatisfy { $0.available })
        #expect(options.normalizedTarget(current: "", preferred: nil) == "testing")
        #expect(options.normalizedTarget(current: "testing", preferred: "production") == "testing")
        #expect(options.normalizedTarget(current: "", preferred: "production") == "production")
        #expect(options.normalizedServices(current: []) == Set(["organizer", "nonprofit-service"]))

        let unavailable = RuntimeService(
            id: "future-service",
            displayName: "Future Service",
            kind: "api",
            available: false,
            unavailableReason: "This fixture intentionally exercises the unavailable-service explanation."
        )
        let worktree = try #require(repository.worktrees.first)
        let demo = try #require(options.targets.first { $0.id == "demo" })
        let prompt = ServiceIsolationPrompt.make(
            repository: repository,
            worktree: worktree,
            target: demo,
            service: unavailable
        )
        #expect(prompt.contains("`future-service`"))
        #expect(prompt.contains(worktree.path))
        #expect(prompt.contains("exact argv"))
        #expect(prompt.contains("Do not mark the service available"))
    }

    @Test
    func `operation row resolves repository worktree target and detailed hover context`() async throws {
        let model = try await fixtureModel()
        let snapshot = try #require(model.snapshot)
        let operation = try #require(snapshot.operations.first)
        let row = OperationRowPresentation(operation: operation, snapshot: snapshot)

        #expect(row.repositoryName == "sample")
        #expect(row.worktreeName == "feature/demo-environment")
        #expect(row.hoverDetail.contains("Target: testing"))
        #expect(row.hoverDetail.contains("Operation: \(operation.id)"))
        #expect(row.hoverDetail.contains("Environment revision:"))
        #expect(newestFirstOperations(snapshot.operations).map(\.updatedAt) == snapshot.operations.map(\.updatedAt).sorted(by: >))
    }

    @Test
    func `command center minimum remains practical on a half screen`() {
        #expect(CommandCenterLayout.minimumWidth == 680)
        #expect(CommandCenterLayout.minimumWidth < 720)
        #expect(CommandCenterLayout.minimumHeight <= 540)
        #expect(CommandCenterLayout.defaultWidth > CommandCenterLayout.minimumWidth)
    }

    @Test
    func `Zed launch plan opens the exact worktree in a new window`() throws {
        let root = URL(fileURLWithPath: NSTemporaryDirectory())
            .appending(path: UUID().uuidString, directoryHint: .isDirectory)
        let application = root.appending(path: "Zed.app", directoryHint: .isDirectory)
        let executable = application.appending(path: "Contents/MacOS/cli", directoryHint: .notDirectory)
        let worktree = root.appending(path: "work tree", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(
            at: executable.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try FileManager.default.createDirectory(at: worktree, withIntermediateDirectories: true)
        try Data("#!/bin/sh\n".utf8).write(to: executable)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: executable.path)
        defer { try? FileManager.default.removeItem(at: root) }

        let plan = try ZedLaunchPlan.make(applicationURL: application, worktreePath: worktree.path)
        #expect(plan.executable == executable)
        #expect(plan.arguments == ["-n", worktree.path])
    }

    @Test
    func `Finder reveal target preserves the exact worktree directory`() throws {
        let root = URL(fileURLWithPath: NSTemporaryDirectory())
            .appending(path: UUID().uuidString, directoryHint: .isDirectory)
        let worktree = root.appending(path: "work tree", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: worktree, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }

        let target = try FinderRevealTarget.make(worktreePath: worktree.path)
        #expect(target.url == worktree)
        #expect(throws: FinderWorkspaceError.invalidWorktree) {
            try FinderRevealTarget.make(worktreePath: "relative/worktree")
        }
    }

    @Test
    func `Jira reference is derived from configured repository branch without inventing metadata`() throws {
        let reference = try #require(JiraIssueReference.detect(in: "feature/PROJ-830/example-change"))
        #expect(reference.key == "PROJ-830")
        #expect(JiraIssueReference.detect(in: "maintenance/no-ticket") == nil)
        #expect(JiraIssueReference.normalizedKey(" proj-915 ") == "PROJ-915")
        #expect(JiraIssueReference.normalizedKey("PROJ-nope") == nil)
        let overridden = try #require(
            JiraIssueReference.resolve(branch: "feature/PROJ-830/example-change", override: "PROJ-826")
        )
        #expect(overridden.key == "PROJ-826")
        #expect(JiraIssueReference.overrideStorageKey(worktreeId: "worktree_1").contains("worktree_1"))
    }

    @Test
    func `Jira relay command and summary stay exact and bounded`() throws {
        let root = URL(fileURLWithPath: NSTemporaryDirectory())
            .appending(path: UUID().uuidString, directoryHint: .isDirectory)
        let relayRoot = root.appending(path: "jira-mcp-relay", directoryHint: .isDirectory)
        let script = relayRoot.appending(path: "dist/src/issue-summary.js", directoryHint: .notDirectory)
        let node = root.appending(path: "node", directoryHint: .notDirectory)
        try FileManager.default.createDirectory(
            at: script.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try Data("export {};\n".utf8).write(to: script)
        try Data("#!/bin/sh\n".utf8).write(to: node)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: node.path)
        defer { try? FileManager.default.removeItem(at: root) }

        let resolver = JiraRelayCommandResolver(
            homeDirectory: root,
            environment: [
                "SWITCHYARD_JIRA_RELAY_ROOT": relayRoot.path,
                "SWITCHYARD_NODE_BINARY": node.path,
            ]
        )
        let command = try resolver.command(issueKey: "PROJ-830")
        #expect(command.executableURL == node)
        #expect(command.arguments == [script.path, "PROJ-830"])

        let data = Data(#"{"schemaVersion":1,"key":"PROJ-830","summary":"Example issue using fictional data","status":"In Development","assignee":"Example User","priority":"Medium","updated":"2026-08-12T13:21:31.616Z","url":"https://example.atlassian.net/browse/PROJ-830"}"#.utf8)
        let summary = try JSONDecoder().decode(JiraIssueSummary.self, from: data)
        try summary.validate(expectedKey: "PROJ-830")
        #expect(summary.summary == "Example issue using fictional data")
        #expect(summary.assignee == "Example User")
        #expect(throws: JiraRelayClientError.invalidResponse) {
            try summary.validate(expectedKey: "PROJ-831")
        }
    }

    @Test
    func `official GitHub pull request Octicons render as four distinct template vectors`() throws {
        let states: [(PullRequestState, Bool)] = [
            (.open, false),
            (.open, true),
            (.closed, false),
            (.merged, false),
        ]
        let renders = try states.map { state, draft in
            try render(
                AnyView(
                    GitHubPullRequestStateIcon(state: state, draft: draft)
                        .foregroundStyle(.primary)
                        .padding(8)
                ),
                size: CGSize(width: 32, height: 32),
                appearance: .dark
            )
        }
        #expect(renders.allSatisfy { $0.count > 250 })
        #expect(Set(renders).count == 4)
    }

    @Test
    func `menu bar mark replaces the running count with distinct idle and running geometry`() throws {
        #expect(SwitchyardBrandIcon.idle.isTemplate)
        #expect(SwitchyardBrandIcon.running.isTemplate)

        let idle = try render(
            AnyView(
                SwitchyardBrandMark(state: .idle)
                    .frame(width: 18, height: 18)
            ),
            size: CGSize(width: 18, height: 18),
            appearance: .dark
        )
        let running = try render(
            AnyView(
                SwitchyardBrandMark(state: .running)
                    .frame(width: 18, height: 18)
            ),
            size: CGSize(width: 18, height: 18),
            appearance: .dark
        )

        #expect(idle.count > 500)
        #expect(running.count > 500)
        #expect(idle != running)
    }

    @Test
    func `menu bar mark follows running state changes on the live model instance`() async throws {
        let model = try await fixtureModel()

        #expect(MenuBarStatusLabel(model: model).markState == .running)
        await model.select(scenario: .empty)
        #expect(MenuBarStatusLabel(model: model).markState == .idle)
        await model.select(scenario: .canonical)
        #expect(MenuBarStatusLabel(model: model).markState == .running)
    }

    @Test
    func `dock runtime icon has distinct masked idle and running states`() throws {
        let idle = SwitchyardDockIcon.idle
        let running = SwitchyardDockIcon.running
        let idleData = try #require(idle.tiffRepresentation)
        let runningData = try #require(running.tiffRepresentation)
        let idleBitmap = try #require(NSBitmapImageRep(data: idleData))
        let runningBitmap = try #require(NSBitmapImageRep(data: runningData))

        #expect(idle.size == NSSize(width: 1024, height: 1024))
        #expect(running.size == idle.size)
        #expect(idleData != runningData)
        #expect(idleBitmap.colorAt(x: 0, y: 0)?.alphaComponent == 0)
        #expect(idleBitmap.colorAt(x: 1023, y: 1023)?.alphaComponent == 0)
        #expect(idleBitmap.colorAt(x: 512, y: 512)?.alphaComponent == 1)
        #expect(try #require(runningBitmap.colorAt(x: 342, y: 682)).brightnessComponent > 0.5)
        #expect(try #require(runningBitmap.colorAt(x: 682, y: 342)).brightnessComponent > 0.5)
    }

    @Test
    func `command center and menu render offscreen in light dark wide and compact states`() async throws {
        let model = try await fixtureModel()
        let snapshot = try #require(model.snapshot)
        let environment = try #require(snapshot.environments.first)
        let recoveryFixtureURL = try makeRecoveryFixture()
        defer { try? FileManager.default.removeItem(at: recoveryFixtureURL.deletingLastPathComponent()) }
        let recoveryModel = AppModel(scenario: .canonical, canonicalFixtureURL: recoveryFixtureURL)
        await recoveryModel.refresh()
        let recoverySnapshot = try #require(recoveryModel.snapshot)
        let recoveryEnvironment = try #require(recoverySnapshot.environments.first)
        let outputDirectory = ProcessInfo.processInfo.environment["SWITCHYARD_SCREENSHOT_DIR"]
            .map { URL(fileURLWithPath: $0, isDirectory: true) }

        var renders: [(String, Data)] = []
        renders.append((
            "github-pull-request-octicons-dark",
            try render(
                AnyView(
                    HStack(spacing: 14) {
                        GitHubPullRequestStateIcon(state: .open, draft: false)
                        GitHubPullRequestStateIcon(state: .open, draft: true)
                        GitHubPullRequestStateIcon(state: .closed, draft: false)
                        GitHubPullRequestStateIcon(state: .merged, draft: false)
                    }
                    .foregroundStyle(.primary)
                    .padding(10)
                ),
                size: CGSize(width: 104, height: 36),
                appearance: .dark
            )
        ))
        model.selection = .overview
        for appearance in [RenderAppearance.light, .dark] {
            renders.append((
                "overview-wide-\(appearance.name)",
                try render(
                    AnyView(CommandCenterView(model: model)),
                    size: CGSize(width: 1_180, height: 760),
                    appearance: appearance,
                    roundedWindow: appearance == .light)
            ))
            renders.append((
                "overview-compact-\(appearance.name)",
                try render(
                    AnyView(CommandCenterView(model: model)),
                    size: CGSize(width: CommandCenterLayout.minimumWidth, height: 680),
                    appearance: appearance)
            ))
            renders.append((
                "menu-\(appearance.name)",
                try renderFitting(
                    AnyView(MenuBarSummaryView(model: model)),
                    appearance: appearance)
            ))
        }
        renders.append((
            "menu-bar-mark-idle-dark",
            try render(
                AnyView(
                    SwitchyardBrandMark(state: .idle)
                        .frame(width: 18, height: 18)
                ),
                size: CGSize(width: 18, height: 18),
                appearance: .dark
            )
        ))
        renders.append((
            "menu-bar-mark-running-dark",
            try render(
                AnyView(
                    SwitchyardBrandMark(state: .running)
                        .frame(width: 18, height: 18)
                ),
                size: CGSize(width: 18, height: 18),
                appearance: .dark
            )
        ))
        renders.append((
            "dock-icon-idle",
            try render(
                AnyView(
                    Image(nsImage: SwitchyardDockIcon.idle)
                        .resizable()
                        .scaledToFit()
                ),
                size: CGSize(width: 256, height: 256),
                appearance: .dark
            )
        ))
        renders.append((
            "dock-icon-running",
            try render(
                AnyView(
                    Image(nsImage: SwitchyardDockIcon.running)
                        .resizable()
                        .scaledToFit()
                ),
                size: CGSize(width: 256, height: 256),
                appearance: .dark
            )
        ))

        model.selection = .environment(environment.id)
        renders.append((
            "environment-wide-dark",
            try render(
                AnyView(CommandCenterView(model: model)),
                size: CGSize(width: 1_180, height: 760),
                appearance: .dark)
        ))
        #expect(recoveryEnvironment.desiredState == .stopped)
        #expect(recoveryEnvironment.observedState == .failed)
        #expect(recoveryEnvironment.allowsStopRequest)
        #expect(recoveryEnvironment.allowsRebuildRequest)
        #expect(recoveryEnvironment.services.first?.observedState == .unverifiable)
        recoveryModel.selection = .environment(recoveryEnvironment.id)
        renders.append((
            "environment-recovery-wide-dark",
            try render(
                AnyView(CommandCenterView(model: recoveryModel)),
                size: CGSize(width: 1_180, height: 760),
                appearance: .dark)
        ))
        model.selection = .worktree(repositoryId: snapshot.repositories[0].id, worktreeId: snapshot.repositories[0].worktrees[0].id)
        renders.append((
            "worktree-wide-dark",
            try render(
                AnyView(CommandCenterView(model: model)),
                size: CGSize(width: 1_180, height: 760),
                appearance: .dark)
        ))
        model.selection = .repository(snapshot.repositories[0].id)
        renders.append((
            "repository-settings-wide-dark",
            try render(
                AnyView(CommandCenterView(model: model)),
                size: CGSize(width: 1_180, height: 760),
                appearance: .dark)
        ))
        renders.append((
            "add-repository-sheet-dark",
            try render(
                AnyView(AddRepositorySheet(model: model, isPresented: .constant(true))),
                size: CGSize(width: 640, height: 640),
                appearance: .dark)
        ))
        renders.append((
            "configuration-status-light",
            try render(
                AnyView(ConfigurationStatusCard(model: model).padding(20)),
                size: CGSize(width: 900, height: 420),
                appearance: .light)
        ))
        renders.append((
            "settings-dark",
            try render(
                AnyView(SettingsView(model: model, updates: AppUpdateController())),
                size: CGSize(width: 500, height: 680),
                appearance: .dark)
        ))
        renders.append((
            "start-demo-warning-dark",
            try render(
                AnyView(StartEnvironmentView(
                    model: model,
                    snapshot: snapshot,
                    initialWorktreeId: snapshot.repositories.first?.worktrees.first?.id,
                    initialTargetId: "demo"
                )),
                size: CGSize(width: 900, height: 260),
                appearance: .dark)
        ))
        let repository = try #require(snapshot.repositories.first)
        let worktree = try #require(repository.worktrees.first)
        let target = try #require(repository.runtime?.targets.first { $0.id == "testing" })
        let unavailableService = RuntimeService(
            id: "future-service",
            displayName: "Future Service",
            kind: "api",
            available: false,
            unavailableReason: "This fixture intentionally exercises the unavailable-service explanation."
        )
        renders.append((
            "service-catalog-dark",
            try render(
                AnyView(ServicePickerPopover(
                    repository: repository,
                    worktree: worktree,
                    target: target,
                    services: repository.runtime?.services ?? [],
                    selectedServiceIDs: .constant(Set(["organizer", "nonprofit-service"]))
                )),
                size: CGSize(width: 460, height: 560),
                appearance: .dark)
        ))
        renders.append((
            "service-unavailable-info-dark",
            try render(
                AnyView(UnavailableServiceInfo(
                    service: unavailableService,
                    prompt: ServiceIsolationPrompt.make(
                        repository: repository,
                        worktree: worktree,
                        target: target,
                        service: unavailableService
                    )
                )),
                size: CGSize(width: 390, height: 270),
                appearance: .dark)
        ))

        for (name, data) in renders {
            let minimumByteCount = name.hasPrefix("menu-bar-mark-") ? 500 :
                (name == "github-pull-request-octicons-dark" ? 2_000 : 10_000)
            #expect(data.count > minimumByteCount)
        }
        let lightOverview = try #require(renders.first { $0.0 == "overview-wide-light" })
        let darkOverview = try #require(renders.first { $0.0 == "overview-wide-dark" })
        #expect(lightOverview.1 != darkOverview.1)

        if let outputDirectory {
            try FileManager.default.createDirectory(at: outputDirectory, withIntermediateDirectories: true)
            for (name, data) in renders {
                try data.write(to: outputDirectory.appending(path: "\(name).png"), options: .atomic)
            }
            for (name, image) in [
                ("dock-icon-idle", SwitchyardDockIcon.idle),
                ("dock-icon-running", SwitchyardDockIcon.running),
            ] {
                let tiffData = try #require(image.tiffRepresentation)
                let bitmap = try #require(NSBitmapImageRep(data: tiffData))
                let pngData = try #require(bitmap.representation(using: .png, properties: [:]))
                try pngData.write(
                    to: outputDirectory.appending(path: "\(name).png"),
                    options: .atomic
                )
            }
        }
    }

    private func fixtureModel() async throws -> AppModel {
        let model = AppModel(scenario: .canonical, canonicalFixtureURL: Self.fixtureURL)
        await model.refresh()
        guard model.phase == .loaded else {
            throw PresentationTestError.fixtureDidNotLoad
        }
        return model
    }

    private static var fixtureURL: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appending(path: "contracts/v1/fixtures/status.json")
    }

    private func makeRecoveryFixture() throws -> URL {
        let source = try Data(contentsOf: Self.fixtureURL)
        var root = try #require(JSONSerialization.jsonObject(with: source) as? [String: Any])
        var environments = try #require(root["environments"] as? [[String: Any]])
        var environment = try #require(environments.first)
        environment["desiredState"] = "stopped"
        environment["observedState"] = "failed"
        environment["health"] = "degraded"

        var services = try #require(environment["services"] as? [[String: Any]])
        var service = try #require(services.first)
        service["desiredState"] = "stopped"
        service["observedState"] = "unverifiable"
        service["health"] = "degraded"
        service["observationCode"] = "PROCESS_OWNERSHIP_UNVERIFIED"
        services[0] = service
        environment["services"] = services
        environments[0] = environment
        root["environments"] = environments

        let directory = FileManager.default.temporaryDirectory
            .appending(path: UUID().uuidString, directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let fixture = directory.appending(path: "status.json", directoryHint: .notDirectory)
        try JSONSerialization.data(withJSONObject: root, options: [.sortedKeys])
            .write(to: fixture, options: .atomic)
        return fixture
    }

    private func render(
        _ view: AnyView,
        size: CGSize,
        appearance: RenderAppearance,
        roundedWindow: Bool = false
    ) throws -> Data {
        let root = AnyView(
            view
                .environment(\.colorScheme, appearance.colorScheme)
                .frame(width: size.width, height: size.height)
                .background(Color(nsColor: .windowBackgroundColor))
        )
        let hosting = NSHostingView(rootView: root)
        hosting.appearance = NSAppearance(named: appearance.nameValue)
        hosting.frame = CGRect(origin: .zero, size: size)
        hosting.layoutSubtreeIfNeeded()

        return try pngData(for: hosting, size: size, roundedWindow: roundedWindow)
    }

    private func renderFitting(
        _ view: AnyView,
        appearance: RenderAppearance
    ) throws -> Data {
        let root = AnyView(
            view
                .environment(\.colorScheme, appearance.colorScheme)
                .background(Color(nsColor: .windowBackgroundColor))
        )
        let hosting = NSHostingView(rootView: root)
        hosting.appearance = NSAppearance(named: appearance.nameValue)
        let size = hosting.fittingSize
        #expect(size.width == 420)
        #expect(size.height > 200)
        #expect(size.height < 500)
        hosting.frame = CGRect(origin: .zero, size: size)
        hosting.layoutSubtreeIfNeeded()

        return try pngData(for: hosting, size: size)
    }

    private func pngData(
        for hosting: NSHostingView<AnyView>,
        size: CGSize,
        roundedWindow: Bool = false
    ) throws -> Data {

        let scale: CGFloat = 2
        let representation = try #require(NSBitmapImageRep(
            bitmapDataPlanes: nil,
            pixelsWide: Int(size.width * scale),
            pixelsHigh: Int(size.height * scale),
            bitsPerSample: 8,
            samplesPerPixel: 4,
            hasAlpha: true,
            isPlanar: false,
            colorSpaceName: .deviceRGB,
            bytesPerRow: 0,
            bitsPerPixel: 0
        ))
        representation.size = size
        let context = try #require(NSGraphicsContext(bitmapImageRep: representation))
        context.cgContext.clear(CGRect(origin: .zero, size: size))
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = context
        if roundedWindow {
            NSBezierPath(
                roundedRect: CGRect(origin: .zero, size: size),
                xRadius: 13,
                yRadius: 13
            ).addClip()
        }
        hosting.displayIgnoringOpacity(hosting.bounds, in: context)
        NSGraphicsContext.restoreGraphicsState()
        return try #require(representation.representation(using: .png, properties: [:]))
    }
}

private enum RenderAppearance: CaseIterable, Equatable {
    case light
    case dark

    var name: String {
        switch self {
        case .light: "light"
        case .dark: "dark"
        }
    }

    var nameValue: NSAppearance.Name {
        switch self {
        case .light: .aqua
        case .dark: .darkAqua
        }
    }

    var colorScheme: ColorScheme {
        switch self {
        case .light: .light
        case .dark: .dark
        }
    }
}

private enum PresentationTestError: Error {
    case fixtureDidNotLoad
}
