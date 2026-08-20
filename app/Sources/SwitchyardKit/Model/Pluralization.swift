import Foundation

/// Renders a count with the correctly inflected noun, for example `1 worktree`
/// or `3 worktrees`, so summary strings never read "1 worktrees".
public func pluralized(_ count: Int, _ singular: String, _ plural: String? = nil) -> String {
    "\(count) \(count == 1 ? singular : plural ?? singular + "s")"
}
