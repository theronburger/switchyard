import AppKit
import SwiftUI

struct ConfigurationView: View {
    @Bindable var model: WorkspaceModel
    @State private var json = ""
    @State private var savedJSON = ""
    @State private var error: String?
    @State private var busy = false
    @State private var showsAdd = false
    @State private var advanced = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                HStack {
                    VStack(alignment: .leading, spacing: 5) {
                        Text("Repositories").font(.largeTitle.bold())
                        Text("Private commands and settings for your workspaces.").foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("Add repository…") { showsAdd = true }.buttonStyle(.borderedProminent).disabled(busy || json.isEmpty)
                }
                ForEach(model.repositories) { repository in
                    VStack(alignment: .leading, spacing: 10) {
                        HStack { Text(repository.name).font(.headline); Spacer(); Text("\((repository.services ?? []).count) services").foregroundStyle(.secondary) }
                        Text(repository.path).font(.caption.monospaced()).textSelection(.enabled)
                        Text((repository.services ?? []).map(\.name).joined(separator: " · ")).font(.caption).foregroundStyle(.secondary)
                        if let error = repository.error { Notice(text: error) }
                    }.card()
                }
                if model.repositories.isEmpty {
                    ContentUnavailableView("No repositories configured", systemImage: "folder.badge.plus", description: Text("Add a repository and the commands you already use to run it."))
                }
                if let error { Notice(text: error) }
                DisclosureGroup("Edit configuration JSON", isExpanded: $advanced) {
                    VStack(alignment: .leading, spacing: 12) {
                        Text("Saving accepts these commands for execution. The configuration stays in Switchyard's private storage.").font(.caption).foregroundStyle(.secondary)
                        TextEditor(text: $json).font(.system(.caption, design: .monospaced)).frame(minHeight: 400).disabled(busy)
                            .overlay(RoundedRectangle(cornerRadius: 6).stroke(.separator))
                        HStack {
                            Button("Reload") { Task { await load() } }.disabled(busy)
                            Spacer()
                            if busy { ProgressView().controlSize(.small) }
                            Button("Save configuration") { Task { await save(json) } }.buttonStyle(.borderedProminent).disabled(busy || json == savedJSON)
                        }
                    }.padding(.top, 12)
                }.card()
            }.padding(28).frame(maxWidth: 1080, alignment: .leading)
        }
        .task { await load() }
        .onChange(of: model.snapshot?.instanceId) { _, _ in
            if json.isEmpty { Task { await load() } }
        }
        .sheet(isPresented: $showsAdd) {
            AddRepositoryView(existingJSON: json) { updated in
                await save(updated) ? nil : (error ?? "The configuration could not be saved.")
            }
        }
    }
    private func load() async {
        busy = true; defer { busy = false }
        do { json = try await model.api.configuration(); savedJSON = json; error = nil }
        catch { self.error = error.localizedDescription }
    }
    @discardableResult private func save(_ value: String) async -> Bool {
        busy = true; defer { busy = false }
        do {
            try await model.api.saveConfiguration(value)
            json = value; savedJSON = value; error = nil
            await model.refreshAfterMutation()
            return true
        } catch { self.error = error.localizedDescription; return false }
    }
}

struct AddRepositoryView: View {
    let existingJSON: String
    let save: (String) async -> String?
    @Environment(\.dismiss) private var dismiss
    @State private var path = ""
    @State private var name = ""
    @State private var setup = ""
    @State private var command = ""
    @State private var port = "3000"
    @State private var portVariable = "PORT"
    @State private var readinessPath = "/"
    @State private var target = "local"
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("Add repository").font(.title2.bold())
            Form {
                HStack {
                    TextField("Repository path", text: $path)
                    Button("Choose…") {
                        let panel = NSOpenPanel(); panel.canChooseDirectories = true; panel.canChooseFiles = false
                        if panel.runModal() == .OK, let url = panel.url { path = url.path; if name.isEmpty { name = url.lastPathComponent } }
                    }
                }
                TextField("Name", text: $name)
                TextField("Target", text: $target)
                TextField("Setup command (optional)", text: $setup, prompt: Text("npm install"))
                TextField("App command", text: $command, prompt: Text("npm run dev"))
                TextField("Preferred port", text: $port)
                TextField("Port environment variable", text: $portVariable)
                TextField("Readiness URL path", text: $readinessPath)
            }.textFieldStyle(.roundedBorder)
            Text("Commands run in each worktree. Saving authorizes these commands. Add more services, targets, dependencies and variables in the JSON editor.").font(.caption).foregroundStyle(.secondary)
            if let error { Notice(text: error) }
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("Save repository") { Task { await add() } }.buttonStyle(.borderedProminent)
                    .disabled(busy || !path.hasPrefix("/") || name.isEmpty || command.isEmpty || target.isEmpty || Int(port) == nil)
            }
        }.padding(24).frame(width: 580)
    }

    private func add() async {
        busy = true; defer { busy = false }
        do {
            guard var config = try JSONSerialization.jsonObject(with: Data(existingJSON.utf8)) as? [String: Any],
                  let port = Int(port), (1...65535).contains(port) else { throw WorkspaceAPIError(message: "Enter a valid port and configuration.") }
            var repositories = config["repositories"] as? [[String: Any]] ?? []
            let id = UUID().uuidString.lowercased()
            let recipe: [String: Any] = [
                "id": id, "name": name, "path": path, "defaultTarget": target,
                "setup": setup.isEmpty ? [] : [["script": setup, "timeoutSeconds": 600]],
                "targets": [["id": target, "name": target]],
                "services": [["id": "app", "name": "App", "kind": "web", "command": ["script": command],
                              "ports": [["name": "http", "environment": portVariable, "preferred": port, "url": true]],
                              "readiness": ["port": "http", "path": readinessPath, "timeoutSeconds": 120]]],
            ]
            repositories.append(recipe); config["repositories"] = repositories
            let updated = String(decoding: try JSONSerialization.data(withJSONObject: config, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]), as: UTF8.self)
            if let failure = await save(updated) { error = failure } else { dismiss() }
        } catch { self.error = error.localizedDescription }
    }
}

struct SetupView: View {
    @Bindable var model: WorkspaceModel
    @State private var connecting = Set<String>()
    @State private var results: [String: String] = [:]

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                Text("Connections & helper").font(.largeTitle.bold())
                VStack(alignment: .leading, spacing: 14) {
                    HStack {
                        Label("Local helper", systemImage: "server.rack").font(.headline)
                        Spacer()
                        StateBadge(state: model.connectionError == nil && model.snapshot != nil ? "ready" : "disconnected")
                    }
                    Text("Switchyard starts its helper automatically. Workspaces keep running when this window closes.").foregroundStyle(.secondary)
                    if let error = model.connectionError { Notice(text: error) }
                    HStack {
                        Button("Check connection") { Task { await model.refresh() } }
                        Button("Repair helper") { Task { await model.connect(repair: true) } }.disabled(model.isConnecting)
                        if model.isConnecting { ProgressView().controlSize(.small) }
                    }
                }.card()
                VStack(alignment: .leading, spacing: 16) {
                    Text("Agent connections").font(.headline)
                    Text("Connect your installed agent to the same workspaces, Run and Stop actions.").foregroundStyle(.secondary)
                    ForEach(["codex", "claude"], id: \.self) { name in
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(name == "codex" ? "Codex" : "Claude Code").font(.body.weight(.medium))
                                Text(results[name] ?? (AgentTools.executable(name) == nil ? "Not installed" : "Installed · ready to connect")).font(.caption).foregroundStyle(.secondary)
                            }
                            Spacer()
                            if connecting.contains(name) { ProgressView().controlSize(.small) }
                            Button("Connect / repair") { Task { await connect(name) } }
                                .disabled(connecting.contains(name) || AgentTools.executable(name) == nil || model.connectionError != nil)
                        }
                    }
                    if model.installation.channel != .release {
                        Text("Development uses the separate switchyard-rebuild connection.").font(.caption).foregroundStyle(.secondary)
                    }
                }.card()
                DisclosureGroup("Installation details") {
                    Text(model.installation.root.path).font(.caption.monospaced()).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
                }.card()
            }.padding(28).frame(maxWidth: 1000, alignment: .leading)
        }
    }
    private func connect(_ name: String) async {
        connecting.insert(name); defer { connecting.remove(name) }
        let installation = model.installation
        do {
            try await Task.detached { try AgentTools.connect(name, installation: installation) }.value
            results[name] = "Connected. Start a new agent session to load the connection."
        } catch { results[name] = error.localizedDescription }
    }
}

struct PruneView: View {
    @Bindable var model: WorkspaceModel
    let plan: PrunePlan
    @Environment(\.dismiss) private var dismiss
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("Delete worktree").font(.title2.bold())
            Text(plan.branch).font(.headline)
            Text(plan.path).font(.callout.monospaced()).textSelection(.enabled)
            if let blockers = plan.blockers, !blockers.isEmpty {
                Text("This worktree cannot be deleted:").font(.headline)
                ForEach(blockers, id: \.self) { Notice(text: $0) }
            } else { Text("Git will remove this linked checkout. Switchyard checks it again before deletion.").foregroundStyle(.secondary) }
            if let error = model.errors[plan.path] { Notice(text: error) }
            HStack {
                Button(model.busy[plan.path] == nil ? "Cancel" : "Close", role: .cancel) { dismiss() }
                Spacer()
                Button(model.busy[plan.path] == nil ? "Delete worktree" : "Deleting…", role: .destructive) { Task { if await model.prune(path: plan.path) { dismiss() } } }
                    .disabled(!(plan.blockers ?? []).isEmpty || model.busy[plan.path] != nil)
            }
        }.padding(24).frame(width: 550)
    }
}

struct CreateWorkspaceView: View {
    @Bindable var model: WorkspaceModel
    @Environment(\.dismiss) private var dismiss
    @State private var repositoryId = ""
    @State private var branch = ""
    @State private var base = ""
    @State private var busy = false
    @State private var error: String?
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Text("New worktree").font(.title2.bold())
            Form {
                Picker("Repository", selection: $repositoryId) { ForEach(model.repositories) { Text($0.name).tag($0.id) } }
                TextField("Branch", text: $branch)
                TextField("Start from (optional)", text: $base)
            }.textFieldStyle(.roundedBorder).disabled(busy)
            if let error { Notice(text: error) }
            HStack {
                Button("Cancel", role: .cancel) { dismiss() }.disabled(busy)
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("Create") { Task { await create() } }.buttonStyle(.borderedProminent).disabled(busy || branch.isEmpty || repositoryId.isEmpty)
            }
        }.padding(24).frame(width: 480)
            .onAppear { repositoryId = model.repositories.first?.id ?? "" }
    }
    private func create() async {
        let request = CreateRequest(repositoryId: repositoryId, branch: branch, base: base.isEmpty ? nil : base)
        busy = true; defer { busy = false }
        do { try await model.api.create(request); await model.refreshAfterMutation(); dismiss() }
        catch { self.error = error.localizedDescription }
    }
}
