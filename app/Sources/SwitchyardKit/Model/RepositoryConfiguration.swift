import Foundation

/// Owner-supplied inputs for one new repository entry in the private
/// configuration file. This is generic profile data: the product never learns
/// which repository it describes, and nothing here is written into a checkout.
public struct RepositoryConfigurationDraft: Sendable, Equatable {
    public var key: String
    public var displayName: String
    public var rootPath: String
    public var remote: String
    public var defaultBase: String
    public var managedWorktreesRoot: String
    public var enabled: Bool

    public init(
        key: String = "",
        displayName: String = "",
        rootPath: String = "",
        remote: String = "origin",
        defaultBase: String = "origin/main",
        managedWorktreesRoot: String = "",
        enabled: Bool = true
    ) {
        self.key = key
        self.displayName = displayName
        self.rootPath = rootPath
        self.remote = remote
        self.defaultBase = defaultBase
        self.managedWorktreesRoot = managedWorktreesRoot
        self.enabled = enabled
    }

    /// A draft prefilled from the daemon's desired-file view, used by Edit.
    public init(entry: ConfigurationRepositoryEntry) {
        self.init(
            key: entry.key,
            displayName: entry.displayName,
            rootPath: entry.root,
            remote: entry.remote,
            defaultBase: entry.defaultBase,
            managedWorktreesRoot: entry.managedWorktreesRoot,
            enabled: entry.enabled
        )
    }

    /// The generic entry the daemon writes for this draft.
    public var entry: ConfigurationRepositoryEntry {
        let draft = normalized
        return ConfigurationRepositoryEntry(
            key: draft.key,
            enabled: draft.enabled,
            displayName: draft.displayName,
            root: draft.rootPath,
            remote: draft.remote,
            defaultBase: draft.defaultBase,
            managedWorktreesRoot: draft.managedWorktreesRoot
        )
    }

    public enum Problem: Sendable, Equatable, CustomStringConvertible {
        case keyInvalid
        case displayNameMissing
        case rootNotAbsolute
        case rootIsNotDirectory
        case remoteInvalid
        case defaultBaseInvalid
        case managedRootNotAbsolute
        case managedRootInsideRoot
        case keyAlreadyConfigured

        public var description: String {
            switch self {
            case .keyInvalid:
                return "Repository key must be 1–64 lowercase letters, digits, or hyphens and start with a letter."
            case .displayNameMissing:
                return "Display name is required."
            case .rootNotAbsolute:
                return "Repository root must be an absolute path."
            case .rootIsNotDirectory:
                return "Repository root must be an existing directory."
            case .remoteInvalid:
                return "Remote must be a Git remote name without whitespace."
            case .defaultBaseInvalid:
                return "Default base must be a Git ref such as origin/main."
            case .managedRootNotAbsolute:
                return "Managed worktrees root must be an absolute path."
            case .managedRootInsideRoot:
                return "Managed worktrees root must be outside the repository root."
            case .keyAlreadyConfigured:
                return "A repository with this key is already configured."
            }
        }
    }

    /// Trimmed, normalized copy used for validation and rendering.
    public var normalized: RepositoryConfigurationDraft {
        var copy = self
        copy.key = key.trimmingCharacters(in: .whitespacesAndNewlines)
        copy.displayName = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
        copy.rootPath = Self.normalizePath(rootPath)
        copy.remote = remote.trimmingCharacters(in: .whitespacesAndNewlines)
        copy.defaultBase = defaultBase.trimmingCharacters(in: .whitespacesAndNewlines)
        copy.managedWorktreesRoot = Self.normalizePath(managedWorktreesRoot)
        return copy
    }

    /// Derives a stable key and display name from a chosen root directory.
    public static func suggested(forRootPath rootPath: String) -> RepositoryConfigurationDraft {
        let normalizedRoot = normalizePath(rootPath)
        let basename = URL(fileURLWithPath: normalizedRoot).lastPathComponent
        var key = basename.lowercased().map { character -> Character in
            character.isLetter || character.isNumber ? character : "-"
        }
        while let first = key.first, !first.isLetter { key.removeFirst() }
        var collapsed = ""
        for character in key where !(character == "-" && collapsed.hasSuffix("-")) {
            collapsed.append(character)
        }
        while collapsed.hasSuffix("-") { collapsed.removeLast() }
        let parent = URL(fileURLWithPath: normalizedRoot).deletingLastPathComponent()
        return RepositoryConfigurationDraft(
            key: String(collapsed.prefix(64)),
            displayName: basename,
            rootPath: normalizedRoot,
            managedWorktreesRoot: basename.isEmpty ? "" : parent.appending(path: basename + "-worktrees").path
        )
    }

    /// Validation problems. `requiresExistingRoot` is false when editing an
    /// entry whose root is already bound: the daemon, not this machine's view
    /// of the filesystem, owns that binding.
    public func problems(
        existingKeys: Set<String> = [],
        requiresExistingRoot: Bool = true,
        fileManager: FileManager = .default
    ) -> [Problem] {
        let draft = normalized
        var problems: [Problem] = []
        if !Self.validKey(draft.key) { problems.append(.keyInvalid) }
        else if existingKeys.contains(draft.key) { problems.append(.keyAlreadyConfigured) }
        if draft.displayName.isEmpty { problems.append(.displayNameMissing) }
        if !draft.rootPath.hasPrefix("/") {
            problems.append(.rootNotAbsolute)
        } else if requiresExistingRoot {
            var isDirectory: ObjCBool = false
            if !fileManager.fileExists(atPath: draft.rootPath, isDirectory: &isDirectory) || !isDirectory.boolValue {
                problems.append(.rootIsNotDirectory)
            }
        }
        if !Self.validRefComponent(draft.remote) { problems.append(.remoteInvalid) }
        if !Self.validRefComponent(draft.defaultBase) { problems.append(.defaultBaseInvalid) }
        if !draft.managedWorktreesRoot.hasPrefix("/") {
            problems.append(.managedRootNotAbsolute)
        } else if draft.rootPath.hasPrefix("/"),
                  draft.managedWorktreesRoot == draft.rootPath ||
                    draft.managedWorktreesRoot.hasPrefix(draft.rootPath + "/") {
            problems.append(.managedRootInsideRoot)
        }
        return problems
    }

    /// A preview of the entry the daemon will add under `repositories:` in the
    /// private `configuration.yaml`. The daemon writes the file; this rendering
    /// is only for review. Every scalar is double-quoted so paths and names
    /// never change meaning under YAML's implicit typing.
    public var yamlSnippet: String {
        let draft = normalized
        func quote(_ value: String) -> String {
            var escaped = ""
            for scalar in value.unicodeScalars {
                switch scalar {
                case "\"": escaped += "\\\""
                case "\\": escaped += "\\\\"
                case "\n": escaped += "\\n"
                case "\t": escaped += "\\t"
                default: escaped.unicodeScalars.append(scalar)
                }
            }
            return "\"\(escaped)\""
        }
        return """
          \(draft.key):
            enabled: \(draft.enabled ? "true" : "false")
            displayName: \(quote(draft.displayName))
            root: \(quote(draft.rootPath))
            git:
              remote: \(quote(draft.remote))
              defaultBase: \(quote(draft.defaultBase))
              managedWorktreesRoot: \(quote(draft.managedWorktreesRoot))
            values: {}
            toolchains: {}
            caches: {}
            preparation: {}
            targets: {}
            defaultTarget: ""
            services: {}
            infrastructure: {}
            artifacts: {}
            actions: {}
            cleanup: {}
        """
    }

    static func validKey(_ value: String) -> Bool {
        guard let first = value.first, first.isLetter, first.isLowercase || !first.isCased,
              value.count <= 64 else { return false }
        return value.allSatisfy { ($0.isLetter && $0.isLowercase) || $0.isNumber || $0 == "-" }
    }

    static func validRefComponent(_ value: String) -> Bool {
        !value.isEmpty && value.utf8.count <= 256 &&
            !value.hasPrefix("-") &&
            value.unicodeScalars.allSatisfy {
                !CharacterSet.whitespacesAndNewlines.contains($0) && !CharacterSet.controlCharacters.contains($0)
            }
    }

    static func normalizePath(_ value: String) -> String {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return "" }
        var path = (trimmed as NSString).expandingTildeInPath
        while path.count > 1, path.hasSuffix("/") { path.removeLast() }
        return path
    }
}

/// Where the private configuration lives for the resolved channel. The app
/// reveals and describes this file; only the daemon writes it.
public struct PrivateConfigurationLocation: Sendable, Equatable {
    public let directory: URL
    public let file: URL

    public static func standard(channel: SwitchyardChannel = .resolve()) -> PrivateConfigurationLocation {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSHomeDirectory()).appending(path: "Library/Application Support")
        let directory = base.appending(path: channel.applicationSupportDirectoryName, directoryHint: .isDirectory)
        return PrivateConfigurationLocation(
            directory: directory,
            file: directory.appending(path: "configuration.yaml", directoryHint: .notDirectory)
        )
    }
}

/// Per-repository acceptance derived from the global configuration status.
public enum RepositoryAcceptanceState: Sendable, Equatable {
    /// The repository is in the accepted revision and unchanged by the candidate.
    case accepted
    /// A validated candidate changes this repository; re-acceptance is required.
    case pendingChange
    /// A validated candidate introduces this repository for the first time.
    case pendingAddition
    /// The daemon publishes the repository but the accepted revision is unknown.
    case unknown

    public var label: String {
        switch self {
        case .accepted: return "Accepted"
        case .pendingChange: return "Pending re-acceptance"
        case .pendingAddition: return "Pending acceptance"
        case .unknown: return "Unknown"
        }
    }

    public var requiresAcceptance: Bool {
        self == .pendingChange || self == .pendingAddition
    }
}

public struct ConfigurationAcceptancePresentation: Sendable, Equatable {
    public let status: ConfigurationStatus

    public init(status: ConfigurationStatus) {
        self.status = status
    }

    public var stateLabel: String {
        switch status.state {
        case .missing: return "No accepted configuration"
        case .accepted: return "Accepted"
        case .pending: return "Pending acceptance"
        case .unknown: return "Unknown"
        }
    }

    public var summary: String {
        switch status.state {
        case .missing:
            return "No configuration revision has been accepted. Add a repository, then validate and accept the private configuration."
        case .accepted:
            return "Revision \(status.acceptedRevision) is accepted and authorizes every compiled command."
        case .pending:
            if let candidate = status.candidate {
                let changed = changedRepositoryKeys(candidate).count
                return "A validated candidate staged at \(candidate.stagedAt.formatted(date: .abbreviated, time: .shortened)) is waiting for acceptance (\(pluralized(changed, "repository entry", "repository entries")), \(pluralized(candidate.executableDigests.count, "executable fingerprint")))."
            }
            return "A validated candidate is waiting for acceptance."
        case .unknown:
            return "The daemon reported a configuration state this app does not understand."
        }
    }

    /// The exact revision the next accept request must name. Validation and
    /// acceptance both compare-and-swap against it.
    public var expectedRevision: Int64 { status.acceptedRevision }

    public var canAccept: Bool {
        status.state == .pending && status.candidate != nil
    }

    /// Repository keys whose digest differs from the accepted revision. The
    /// contract publishes only the candidate's per-repository digests, so a
    /// pending candidate marks every listed repository as changed.
    public func changedRepositoryKeys(_ candidate: ConfigurationCandidate) -> [String] {
        candidate.repositoryDigests.keys.sorted()
    }

    /// Acceptance state for one published repository, matched by profile key.
    public func repositoryState(profileKey: String, isPublished: Bool) -> RepositoryAcceptanceState {
        switch status.state {
        case .accepted:
            return .accepted
        case .pending:
            guard let candidate = status.candidate, candidate.repositoryDigests[profileKey] != nil else {
                return isPublished ? .accepted : .unknown
            }
            return isPublished ? .pendingChange : .pendingAddition
        case .missing:
            return isPublished ? .unknown : .pendingAddition
        case .unknown:
            return .unknown
        }
    }

    /// How every repository edit reaches disk: the daemon, never the app or
    /// the checkout, writes the private desired file, and the result is a
    /// staged candidate the owner still has to accept.
    public static let editFlow =
        "Switchyard's daemon writes this entry into its private configuration.yaml under Application Support, compares it against the exact revision and file digest shown here so a concurrent edit is refused, and stages a candidate. Nothing runs until you accept that candidate's digest. The repository itself is never modified."

    /// What the desired-file view tells the owner about editability.
    public var desiredFileSummary: String {
        guard let desired = status.desired else {
            return "The daemon has not published the desired file state, so repository edits are unavailable."
        }
        if !desired.present {
            return "No configuration.yaml exists yet. Adding the first repository creates it as an owner-only file."
        }
        if let problem = desired.problem {
            return "configuration.yaml cannot be edited until it is fixed: \(problem)"
        }
        let count = desired.repositories.count
        return "configuration.yaml lists \(pluralized(count, "repository entry", "repository entries"))."
    }
}

/// Scenario-scoped configuration status for fixture builds and tests. The
/// fixture keeps an in-memory desired file per scenario so the add, edit,
/// disable, and remove flows can be exercised without a daemon, applying the
/// same compare-and-swap rules the daemon enforces.
public final class FixtureConfigurationActionClient: ConfigurationActionSubmitting, @unchecked Sendable {
    public let scenario: FixtureScenario
    private let lock = NSLock()
    private var status: ConfigurationStatus?

    public init(scenario: FixtureScenario) {
        self.scenario = scenario
    }

    public func configuration() async throws -> ConfigurationStatus {
        try currentStatus()
    }

    private func currentStatus() throws -> ConfigurationStatus {
        try lock.withLock {
            if let status { return status }
            let initial: ConfigurationStatus
            switch scenario {
            case .canonical:
                initial = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(Self.canonicalPendingJSON.utf8))
            case .empty:
                initial = try ContractDecoder().decode(ConfigurationStatus.self, from: Data(Self.missingJSON.utf8))
            case .failure:
                throw FixtureError.simulatedDaemonUnavailable
            }
            status = initial
            return initial
        }
    }

    private func store(_ next: ConfigurationStatus) {
        lock.withLock { status = next }
    }

    public func validateConfiguration(_ request: ConfigurationValidationRequest) async throws -> ConfigurationStatus {
        try await configuration()
    }

    public func acceptConfiguration(_ request: ConfigurationAcceptanceRequest) async throws -> ConfigurationStatus {
        let current = try await configuration()
        guard request.expectedRevision == current.acceptedRevision,
              request.digest == current.candidate?.digest else {
            throw FixtureError.simulatedDaemonUnavailable
        }
        let accepted = ConfigurationStatus(
            state: .accepted, acceptedRevision: current.acceptedRevision + 1,
            acceptedDigest: current.candidate?.digest, candidate: nil, desired: current.desired
        )
        store(accepted)
        return accepted
    }

    public func mutateRepositoryConfiguration(_ request: ConfigurationRepositoryMutationRequest) async throws -> ConfigurationStatus {
        let current = try await configuration()
        let desired = current.desired ?? ConfigurationDesiredFile(present: false, sourceDigest: nil, problem: nil, repositories: [])
        guard request.expectedRevision == current.acceptedRevision,
              request.expectedSourceDigest == desired.sourceDigest,
              desired.problem == nil else {
            throw FixtureError.simulatedDaemonUnavailable
        }
        var repositories = desired.repositories
        switch request.operation {
        case .upsert:
            guard let entry = request.entry else { throw FixtureError.simulatedDaemonUnavailable }
            if let index = repositories.firstIndex(where: { $0.key == entry.key }) {
                guard repositories[index].root == entry.root else { throw FixtureError.simulatedDaemonUnavailable }
                repositories[index] = entry
            } else {
                repositories.append(entry)
            }
        case .remove:
            guard let index = repositories.firstIndex(where: { $0.key == request.key }),
                  !repositories[index].enabled else {
                throw FixtureError.simulatedDaemonUnavailable
            }
            repositories.remove(at: index)
        }
        repositories.sort { $0.key < $1.key }
        let sourceDigest = "sha256:" + String(repeating: String(UInt8(repositories.count % 10)), count: 64)
        let candidate = ConfigurationCandidate(
            schemaVersion: contractSchemaVersion,
            digest: "sha256:" + String(repeating: "7", count: 64),
            sourceDigest: sourceDigest,
            compilerVersion: "profile-compiler/1",
            repositoryDigests: Dictionary(uniqueKeysWithValues: repositories.map { ($0.key, "sha256:" + String(repeating: "8", count: 64)) }),
            executableDigests: [:],
            stagedAt: Date(timeIntervalSince1970: 1_786_000_000)
        )
        let staged = ConfigurationStatus(
            state: .pending, acceptedRevision: current.acceptedRevision, acceptedDigest: current.acceptedDigest,
            candidate: candidate,
            desired: ConfigurationDesiredFile(present: true, sourceDigest: sourceDigest, problem: nil, repositories: repositories)
        )
        store(staged)
        return staged
    }

    public static let canonicalPendingJSON = """
    {
      "schemaVersion": 2,
      "state": "pending",
      "acceptedRevision": 4,
      "acceptedDigest": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "candidate": {
        "schemaVersion": 2,
        "digest": "sha256:2222222222222222222222222222222222222222222222222222222222222222",
        "sourceDigest": "sha256:3333333333333333333333333333333333333333333333333333333333333333",
        "compilerVersion": "profile-compiler/1",
        "repositoryDigests": {
          "sample": "sha256:4444444444444444444444444444444444444444444444444444444444444444",
          "second-sample": "sha256:5555555555555555555555555555555555555555555555555555555555555555"
        },
        "executableDigests": {
          "/usr/bin/env": "sha256:6666666666666666666666666666666666666666666666666666666666666666"
        },
        "stagedAt": "2026-08-14T08:01:00Z"
      },
      "desired": {
        "present": true,
        "sourceDigest": "sha256:3333333333333333333333333333333333333333333333333333333333333333",
        "repositories": [
          {
            "key": "sample",
            "enabled": true,
            "displayName": "Sample",
            "root": "/Users/example/Developer/sample",
            "remote": "origin",
            "defaultBase": "origin/main",
            "managedWorktreesRoot": "/Users/example/Developer/sample-worktrees"
          },
          {
            "key": "second-sample",
            "enabled": false,
            "displayName": "Second Sample",
            "root": "/Users/example/Developer/second-sample",
            "remote": "origin",
            "defaultBase": "origin/main",
            "managedWorktreesRoot": "/Users/example/Developer/second-sample-worktrees"
          }
        ]
      }
    }
    """

    public static let canonicalAcceptedJSON = """
    {
      "schemaVersion": 2,
      "state": "accepted",
      "acceptedRevision": 5,
      "acceptedDigest": "sha256:2222222222222222222222222222222222222222222222222222222222222222"
    }
    """

    public static let missingJSON = """
    {
      "schemaVersion": 2,
      "state": "missing",
      "acceptedRevision": 0,
      "desired": { "present": false, "repositories": [] }
    }
    """
}
