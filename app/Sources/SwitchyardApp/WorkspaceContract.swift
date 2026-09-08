import Foundation

struct WorkspaceSnapshot: Decodable, Sendable {
    let version: Int
    let instanceId: String
    let revision: UInt64
    let repositories: [RepositorySummary]?
    let workspaces: [Workspace]?
}

struct RepositorySummary: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let path: String
    let defaultTarget: String
    let targets: [TargetSummary]?
    let services: [ServiceSummary]?
    let error: String?
}

struct TargetSummary: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let confirm: Bool?
}

struct ServiceSummary: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let kind: String?
    let dependencies: [String]?
}

struct Workspace: Decodable, Identifiable, Sendable {
    var id: String { path }
    var displayTitle: String { branch.isEmpty ? "Detached HEAD · \(head.prefix(8))" : branch }
    let path: String
    let repositoryId: String
    let branch: String
    let head: String
    let primary: Bool
    let locked: Bool
    let missing: Bool
    let dirty: Bool
    let unpushed: Bool
    let gitError: String?
    let choices: WorkspaceChoices
    let run: WorkspaceRun?
}

struct WorkspaceChoices: Codable, Equatable, Sendable {
    var target: String
    var services: [String]

    init(target: String, services: [String]) {
        self.target = target
        self.services = services
    }

    init(from decoder: any Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        target = try values.decodeIfPresent(String.self, forKey: .target) ?? ""
        services = try values.decodeIfPresent([String].self, forKey: .services) ?? []
    }
}

struct RunRequest: Encodable, Sendable {
    let path: String
    let requestId: String
    let target: String
    let services: [String]
    let confirmedTarget: String?
    let prepareOnly: Bool
}

struct WorkspaceRun: Decodable, Identifiable, Sendable {
    let id: String
    let path: String
    let target: String
    let requested: [String]?
    let state: String
    let step: String
    let error: String?
    let startedAt: Date
    let services: [ServiceRun]?
    let urls: [String: String]?

    var isBusy: Bool { ["preparing", "starting", "stopping"].contains(state) }
    var isActive: Bool { isBusy || state == "running" }

}

struct ServiceRun: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let state: String
    let pid: Int?
}

struct WorkspaceLog: Decodable, Sendable {
    let runId: String
    let text: String
}

struct PrunePlan: Decodable, Sendable {
    let path: String
    let branch: String
    let blockers: [String]?
}

struct CreateRequest: Encodable, Sendable {
    let repositoryId: String
    let branch: String
    let base: String?
}
