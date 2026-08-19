import AppKit
import Foundation
import SwiftUI

enum FinderWorkspaceError: LocalizedError, Equatable {
    case invalidWorktree

    var errorDescription: String? {
        switch self {
        case .invalidWorktree: "The worktree directory is unavailable."
        }
    }
}

struct FinderRevealTarget: Equatable {
    let url: URL

    static func make(worktreePath: String) throws -> FinderRevealTarget {
        guard worktreePath.hasPrefix("/") else { throw FinderWorkspaceError.invalidWorktree }
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: worktreePath, isDirectory: &isDirectory),
              isDirectory.boolValue else {
            throw FinderWorkspaceError.invalidWorktree
        }
        return FinderRevealTarget(url: URL(fileURLWithPath: worktreePath, isDirectory: true))
    }
}

struct FinderWorkspaceLauncher {
    var reveal: (FinderRevealTarget) -> Void = { target in
        NSWorkspace.shared.activateFileViewerSelecting([target.url])
    }

    func open(worktreePath: String) throws {
        reveal(try FinderRevealTarget.make(worktreePath: worktreePath))
    }
}

struct OpenInFinderButton: View {
    let path: String
    var labelStyle: OpenInFinderLabelStyle = .iconOnly
    @State private var errorMessage: String?

    var body: some View {
        Button {
            do {
                try FinderWorkspaceLauncher().open(worktreePath: path)
            } catch {
                errorMessage = (error as? LocalizedError)?.errorDescription
                    ?? "Finder could not reveal this worktree."
            }
        } label: {
            switch labelStyle {
            case .iconOnly:
                Image(systemName: "folder")
            case .labelled:
                Label("Reveal in Finder", systemImage: "folder")
            }
        }
        .buttonStyle(.borderless)
        .help("Reveal this worktree in Finder")
        .accessibilityLabel("Reveal this worktree in Finder")
        .alert("Could not open Finder", isPresented: Binding(
            get: { errorMessage != nil },
            set: { if !$0 { errorMessage = nil } }
        )) {
            Button("OK") { errorMessage = nil }
        } message: {
            Text(errorMessage ?? "Unknown error")
        }
    }
}

enum OpenInFinderLabelStyle {
    case iconOnly
    case labelled
}
