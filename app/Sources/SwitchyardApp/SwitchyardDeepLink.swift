import Foundation
import SwitchyardKit

enum SwitchyardDeepLink: Equatable, Sendable {
    case worktree(String)

    init?(url: URL) {
        guard url.scheme?.lowercased() == "switchyard",
              url.host?.lowercased() == "worktrees",
              url.user == nil,
              url.password == nil,
              url.port == nil,
              url.query == nil,
              url.fragment == nil else { return nil }
        var encodedPath = url.path(percentEncoded: true)
        guard encodedPath.first == "/" else { return nil }
        encodedPath.removeFirst()
        guard !encodedPath.isEmpty,
              !encodedPath.contains("/"),
              let id = encodedPath.removingPercentEncoding else { return nil }
        guard !id.isEmpty,
              id.utf8.count <= 256,
              id == id.trimmingCharacters(in: .whitespacesAndNewlines),
              !id.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) else { return nil }
        self = .worktree(id)
    }

    @MainActor
    func apply(to model: AppModel) {
        switch self {
        case .worktree(let id):
            model.selectWorktree(withId: id)
        }
    }
}
