import AppKit
import SwiftUI

struct WorkspaceWindow: View {
    @Bindable var model: WorkspaceModel
    @State private var showsCreate = false

    var body: some View {
        NavigationSplitView {
            List(selection: $model.selection) {
                Label("Overview", systemImage: "rectangle.grid.1x2").tag("__overview")
                ForEach(model.repositories) { repository in
                    Section(repository.name) {
                        ForEach(model.workspaces.filter { $0.repositoryId == repository.id }) { workspace in
                            HStack(alignment: .top, spacing: 8) {
                                Circle().fill(stateColor(model.run(for: workspace)?.state ?? "stopped")).frame(width: 7, height: 7).padding(.top, 6)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(workspace.displayTitle).lineLimit(2)
                                    Text(workspace.primary ? "Primary checkout" : workspaceSubtitle(workspace, repository: repository))
                                        .font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                }
                            }
                            .padding(.vertical, 3).help(workspace.path).tag(workspace.path)
                        }
                        if let error = repository.error { Text(error).font(.caption).foregroundStyle(.orange) }
                    }
                }
                Section("Setup") {
                    Label("Repositories", systemImage: "folder.badge.gearshape").tag("__config")
                    Label("Connections & helper", systemImage: "stethoscope").tag("__setup")
                }
            }
            .listStyle(.sidebar)
            .navigationSplitViewColumnWidth(min: 210, ideal: 250, max: 320)
            .safeAreaInset(edge: .bottom) {
                HStack {
                    Circle().fill(model.connectionError == nil && model.snapshot != nil ? .green : .orange).frame(width: 7, height: 7)
                    Text(model.connectionError == nil ? "\(model.runningCount) running" : "Helper disconnected").font(.caption)
                    Spacer()
                }.padding(12).background(.bar)
            }
        } detail: {
            if model.selection == "__config" { ConfigurationView(model: model) }
            else if model.selection == "__setup" { SetupView(model: model) }
            else if let path = model.selection, let workspace = model.workspace(at: path), let repository = model.repository(for: workspace) {
                WorkspaceDetail(model: model, workspace: workspace, repository: repository).id(path)
            } else { overview }
        }
        .navigationTitle("Switchyard")
        .toolbar {
            Button { Task { await model.refresh() } } label: { Label("Refresh", systemImage: "arrow.clockwise") }
            Button { showsCreate = true } label: { Label("New worktree", systemImage: "plus") }.disabled(model.repositories.isEmpty)
        }
        .sheet(isPresented: $showsCreate) { CreateWorkspaceView(model: model) }
        .onOpenURL { url in
            guard ["switchyard", "switchyard-rebuild"].contains(url.scheme ?? ""), url.host == "workspace",
                  let path = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first(where: { $0.name == "path" })?.value,
                  path.hasPrefix("/") else { return }
            model.selection = path
            NSApp.activate(ignoringOtherApps: true)
        }
        .frame(minWidth: 760, minHeight: 560)
    }

    private func workspaceSubtitle(_ workspace: Workspace, repository: RepositorySummary) -> String {
        let path = URL(fileURLWithPath: workspace.path)
        if path.lastPathComponent == URL(fileURLWithPath: repository.path).lastPathComponent {
            return path.deletingLastPathComponent().lastPathComponent + "/" + path.lastPathComponent
        }
        return path.lastPathComponent
    }

    private var overview: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                HStack(spacing: 14) {
                    SwitchyardBrandMark(state: model.runningCount > 0 ? .running : .idle).frame(width: 42, height: 42)
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Your workspaces").font(.largeTitle.bold())
                        Text("\(model.workspaces.count) workspaces · \(model.runningCount) running").foregroundStyle(.secondary)
                    }
                }
                if model.isConnecting { ProgressView("Connecting to the helper…") }
                if let error = model.connectionError {
                    Notice(text: error)
                    Button("Open Setup") { model.selection = "__setup" }
                }
                if model.repositories.isEmpty && !model.isConnecting {
                    ContentUnavailableView {
                        Label("Add your repository", systemImage: "folder.badge.plus")
                    } description: { Text("Choose your repository and its commands. Switchyard discovers its worktrees automatically.") }
                    actions: { Button("Configure repository") { model.selection = "__config" }.buttonStyle(.borderedProminent) }
                } else {
                    ForEach(model.workspaces) { workspace in
                        Button { model.selection = workspace.path } label: {
                            HStack {
                                VStack(alignment: .leading, spacing: 5) {
                                    Text(workspace.displayTitle).font(.headline)
                                    Text(workspace.path).font(.caption.monospaced()).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                                }
                                Spacer()
                                StateBadge(state: model.run(for: workspace)?.state ?? "stopped")
                                Image(systemName: "chevron.right").foregroundStyle(.tertiary)
                            }.padding(16).background(.background.secondary, in: RoundedRectangle(cornerRadius: 10))
                        }.buttonStyle(.plain)
                    }
                }
            }.padding(28).frame(maxWidth: 1080, alignment: .leading)
        }
    }
}

struct WorkspaceDetail: View {
    @Bindable var model: WorkspaceModel
    let workspace: Workspace
    let repository: RepositorySummary
    @State private var showsLogs = false
    @State private var showsFacts = false
    @State private var confirmRun = false
    @State private var pendingChoices: WorkspaceChoices?
    @State private var prunePlan: PrunePlan?
    @State private var localError: String?
    @State private var planning = false
    private var run: WorkspaceRun? { model.run(for: workspace) }
    private var choices: WorkspaceChoices { model.choices(for: workspace, repository: repository) }
    private var transitioning: Bool { run?.isBusy == true || model.busy[workspace.path] != nil }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                header
                if let error = model.connectionError { Notice(text: "Connection interrupted. " + error) }
                runControls
                if let error = model.errors[workspace.path] ?? localError { Notice(text: error) }
                if let error = run?.error, !error.isEmpty { Notice(text: error) }
                if let urls = run?.urls, !urls.isEmpty {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("Open your app").font(.headline)
                        ForEach(urls.sorted { $0.key < $1.key }, id: \.key) { name, address in
                            if let url = URL(string: address), ["http", "https"].contains(url.scheme ?? "") {
                                HStack {
                                    Link(destination: url) { Label(name, systemImage: "arrow.up.right.square").font(.body.weight(.medium)) }
                                    Spacer()
                                    Text(address).font(.caption.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                                    CopyButton(value: address)
                                }
                            }
                        }
                    }.card()
                }
                if run != nil {
                    DisclosureGroup("Run output", isExpanded: $showsLogs) {
                        VStack(alignment: .leading, spacing: 8) {
                            if let error = model.logErrors[workspace.path] { Notice(text: error) }
                            ScrollView([.horizontal, .vertical]) {
                                Text(cleanLog(model.logs[workspace.path]?.text ?? "Waiting for output…"))
                                    .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                                    .frame(maxWidth: .infinity, alignment: .topLeading).padding(12)
                            }.frame(minHeight: 140, maxHeight: 300).background(.black.opacity(0.06), in: RoundedRectangle(cornerRadius: 7))
                            HStack {
                                Text(run?.step ?? "").font(.caption).foregroundStyle(.secondary)
                                Spacer()
                                Button("Refresh output") { Task { await model.loadLogs(path: workspace.path) } }
                            }
                        }.padding(.top, 10)
                    }.card()
                }
                DisclosureGroup("Workspace details", isExpanded: $showsFacts) {
                    Grid(alignment: .leading, horizontalSpacing: 22, verticalSpacing: 10) {
                        fact("Repository", repository.path)
                        fact("Revision", workspace.head)
                        fact("Checkout", workspace.primary ? "Primary" : "Linked worktree")
                        fact("Git state", workspace.dirty ? "Uncommitted changes" : "Clean")
                        fact("Upstream", workspace.unpushed ? "Unpushed commits" : "No unpushed commits")
                        if let error = workspace.gitError { fact("Git observation", error) }
                        if let run { fact("Run", run.id); fact("Started", run.startedAt.formatted()) }
                    }.padding(.top, 12)
                    if !workspace.primary {
                        HStack {
                            Spacer()
                            Button(planning ? "Checking worktree…" : "Delete worktree…", role: .destructive) { Task { await previewPrune() } }
                                .disabled(transitioning || planning)
                        }.padding(.top, 16)
                    }
                }.card()
            }.padding(28).frame(maxWidth: 1080, alignment: .leading)
        }
        .confirmationDialog("Run on \(pendingChoices?.target ?? choices.target)?", isPresented: $confirmRun, titleVisibility: .visible) {
            Button("Run on \(pendingChoices?.target ?? choices.target)") {
                if let pendingChoices { launch(pendingChoices, confirmed: pendingChoices.target) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { Text("This target requires confirmation in your repository configuration.") }
        .sheet(isPresented: Binding(get: { prunePlan != nil }, set: { if !$0 { prunePlan = nil } })) {
            if let prunePlan { PruneView(model: model, plan: prunePlan) }
        }
        .onChange(of: run?.state, initial: true) { _, state in if state == "failed" { showsLogs = true } }
        .task(id: showsLogs) {
            guard showsLogs else { return }
            while !Task.isCancelled {
                await model.loadLogs(path: workspace.path)
                do { try await Task.sleep(for: .seconds(2)) } catch { return }
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "arrow.triangle.branch").font(.title2).foregroundStyle(.secondary).padding(.top, 8)
                VStack(alignment: .leading, spacing: 5) {
                    Text(workspace.displayTitle).font(.largeTitle.bold()).textSelection(.enabled)
                    Text(repository.name).foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                StateBadge(state: run?.state ?? "stopped")
            }
            HStack {
                Text(workspace.path).font(.callout.monospaced()).textSelection(.enabled).lineLimit(1).truncationMode(.middle)
                Spacer()
                CopyButton(value: workspace.path)
                OpenInFinderButton(path: workspace.path)
            }.padding(12).background(.background.secondary, in: RoundedRectangle(cornerRadius: 10))
            HStack(spacing: 16) {
                OpenCodexTaskButton(worktree: workspace)
                Button { openZed() } label: { Label("Open in Zed", systemImage: "arrow.up.right.square") }
                Spacer()
                Label(workspace.dirty ? "Changes" : "Clean", systemImage: workspace.dirty ? "pencil" : "checkmark")
                    .foregroundStyle(workspace.dirty ? .orange : .secondary)
                Text(String(workspace.head.prefix(8))).monospaced()
            }.font(.caption)
        }
    }

    private var runControls: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Run configuration").font(.headline)
                Spacer()
                Picker("Target", selection: Binding(get: { choices.target }, set: { value in
                    var updated = choices; updated.target = value; model.remember(path: workspace.path, choices: updated)
                })) {
                    ForEach(repository.targets ?? []) { Text($0.name).tag($0.id) }
                }.frame(maxWidth: 270).disabled(transitioning)
            }
            ForEach(repository.services ?? []) { service in
                HStack(spacing: 12) {
                    Toggle(isOn: Binding(get: { choices.services.contains(service.id) }, set: { selected in
                        var updated = choices
                        updated.services.removeAll { $0 == service.id }
                        if selected { updated.services.append(service.id) }
                        model.remember(path: workspace.path, choices: updated)
                    })) {
                        VStack(alignment: .leading, spacing: 3) {
                            Label(service.name, systemImage: service.kind == "web" ? "macwindow" : "network")
                            if let dependencies = service.dependencies, !dependencies.isEmpty {
                                Text("Includes \(dependencies.joined(separator: ", "))").font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }.toggleStyle(.checkbox).disabled(transitioning)
                    Spacer()
                    if let active = run?.services?.first(where: { $0.id == service.id }) { StateBadge(state: active.state) }
                }.padding(.vertical, 3)
            }
            Divider()
            HStack(spacing: 12) {
                if transitioning {
                    ProgressView().controlSize(.small)
                    Text(model.busy[workspace.path] ?? run?.step ?? "Starting…").font(.callout).lineLimit(2)
                    Spacer()
                    if run?.isActive == true && run?.state != "stopping" {
                        Button(run?.state == "running" ? "Stop" : "Cancel start") { Task { await model.stopWorkspace(path: workspace.path) } }
                            .disabled(model.busy[workspace.path] == "Stopping…")
                    }
                } else {
                    Text(choices.services.isEmpty ? "Select at least one service." : "Setup is included when you run.").font(.caption).foregroundStyle(.secondary)
                    Spacer()
                    if run?.state == "running" {
                        Button("Stop") { Task { await model.stopWorkspace(path: workspace.path) } }
                    }
                    Button(primaryTitle) {
                        let selected = choices
                        if repository.targets?.first(where: { $0.id == selected.target })?.confirm == true {
                            pendingChoices = selected; confirmRun = true
                        } else { launch(selected) }
                    }.buttonStyle(.borderedProminent)
                        .disabled(choices.services.isEmpty || choices.target.isEmpty || workspace.missing || model.connectionError != nil)
                }
            }
        }.card()
    }

    private var primaryTitle: String {
        if run?.state == "running" {
            return run?.target != choices.target || Set(run?.requested ?? []) != Set(choices.services) ? "Apply & restart" : "Restart"
        }
        return run?.state == "failed" ? "Retry" : "Run"
    }
    private func launch(_ choices: WorkspaceChoices, confirmed: String? = nil) {
        let path = workspace.path
        let restarting = run?.state == "running"
        Task {
            if restarting { await model.restartWorkspace(path: path, choices: choices, confirmedTarget: confirmed) }
            else { await model.runWorkspace(path: path, choices: choices, confirmedTarget: confirmed) }
        }
    }
    private func fact(_ label: String, _ value: String) -> some View {
        GridRow { Text(label).foregroundStyle(.secondary); Text(value).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }.font(.caption)
    }
    private func previewPrune() async {
        planning = true
        defer { planning = false }
        do {
            let plan = try await model.api.prunePlan(path: workspace.path)
            guard plan.path == workspace.path else { throw WorkspaceAPIError(message: "The helper answered for a different workspace.") }
            prunePlan = plan
        } catch { localError = error.localizedDescription }
    }
    private func openZed() {
        guard let app = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "dev.zed.Zed") else { localError = "Zed is not installed."; return }
        let executable = app.appending(path: "Contents/MacOS/cli")
        let process = Process(); process.executableURL = executable; process.arguments = ["-n", workspace.path]
        process.standardOutput = FileHandle.nullDevice; process.standardError = FileHandle.nullDevice
        do { try process.run() } catch { localError = "Zed could not open this workspace." }
    }
}

struct Notice: View {
    let text: String
    var body: some View { Label(text, systemImage: "exclamationmark.circle").font(.callout).foregroundStyle(.orange).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading) }
}
struct StateBadge: View {
    let state: String
    var body: some View { Text(state.capitalized).font(.caption.weight(.semibold)).foregroundStyle(stateColor(state)).padding(.horizontal, 8).padding(.vertical, 4).background(stateColor(state).opacity(0.1), in: Capsule()) }
}
func stateColor(_ state: String) -> Color {
    switch state { case "running", "ready": .green; case "failed", "exited": .red; case "preparing", "starting", "stopping": .orange; default: .secondary }
}
struct CopyButton: View {
    let value: String
    var body: some View {
        Button { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(value, forType: .string) } label: { Image(systemName: "doc.on.doc") }.buttonStyle(.borderless).help("Copy")
    }
}
extension View {
    func card() -> some View { padding(16).background(.background.secondary, in: RoundedRectangle(cornerRadius: 12)) }
}
func cleanLog(_ text: String) -> String {
    text.replacingOccurrences(of: "\u{001B}\\[[0-?]*[ -/]*[@-~]", with: "", options: .regularExpression)
}
