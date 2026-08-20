import Foundation

/// Anything that can produce a full status snapshot for the app.
public protocol StatusProviding: Sendable {
    var sourceDescription: String { get }
    func loadStatus() async throws -> StatusSnapshot
}

/// Live provider backed by the authenticated loopback client.
public struct DaemonStatusProvider: StatusProviding {
    private let client: DaemonClient

    public init(client: DaemonClient) {
        self.client = client
    }

    public var sourceDescription: String { "daemon" }

    public func loadStatus() async throws -> StatusSnapshot {
        try await client.status()
    }
}

/// Development scenarios the fixture-driven app can render.
public enum FixtureScenario: String, CaseIterable, Sendable, Identifiable {
    case canonical
    case empty
    case failure

    public var id: String { rawValue }

    public var displayName: String {
        switch self {
        case .canonical: return "Canonical"
        case .empty: return "Empty"
        case .failure: return "Failure"
        }
    }

    public var blurb: String {
        switch self {
        case .canonical:
            return "The frozen contract fixture: one configured repository worktree with a degraded environment."
        case .empty:
            return "A healthy daemon that has not discovered any repositories yet."
        case .failure:
            return "The daemon endpoint is missing, exercising the error and repair surfaces."
        }
    }
}

public enum FixtureError: Error, Equatable, LocalizedError {
    case canonicalFixtureNotFound(searched: [String])
    case simulatedDaemonUnavailable

    public var errorDescription: String? {
        switch self {
        case .canonicalFixtureNotFound(let searched):
            return "The canonical contract fixture was not found. Searched: \(searched.joined(separator: ", "))"
        case .simulatedDaemonUnavailable:
            return "The daemon endpoint descriptor is missing, so no status snapshot is available. Run Connection Doctor to repair the daemon installation."
        }
    }
}

/// Loads contract fixtures for development builds (the app must render useful
/// state before the daemon exists — see ARCHITECTURE.md).
public struct FixtureStatusProvider: StatusProviding {
    public let scenario: FixtureScenario
    private let canonicalURLOverride: URL?

    public init(scenario: FixtureScenario, canonicalURL: URL? = nil) {
        self.scenario = scenario
        self.canonicalURLOverride = canonicalURL
    }

    public var sourceDescription: String { "fixture (\(scenario.displayName.lowercased()))" }

    public func loadStatus() async throws -> StatusSnapshot {
        switch scenario {
        case .canonical:
            let url = try canonicalURLOverride ?? Self.locateCanonicalFixture()
            let data: Data
            do {
                data = try Data(contentsOf: url)
            } catch {
                throw FixtureError.canonicalFixtureNotFound(searched: [url.path])
            }
            return try ContractDecoder().decode(StatusSnapshot.self, from: data)
        case .empty:
            return try ContractDecoder().decode(StatusSnapshot.self, from: Data(Self.emptyStatusJSON.utf8))
        case .failure:
            throw FixtureError.simulatedDaemonUnavailable
        }
    }

    /// Finds `contracts/v2/fixtures/status.json` for development builds:
    /// an explicit environment override first, then a walk up from the
    /// current directory, then a walk up from this source file.
    public static func locateCanonicalFixture(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        fileManager: FileManager = .default
    ) throws -> URL {
        let relativePath = "contracts/v2/fixtures/status.json"
        var searched: [String] = []

        if let override = environment["SWITCHYARD_STATUS_FIXTURE"] {
            if fileManager.fileExists(atPath: override) {
                return URL(fileURLWithPath: override)
            }
            searched.append(override)
        }

        let roots = [
            URL(fileURLWithPath: fileManager.currentDirectoryPath),
            URL(fileURLWithPath: #filePath).deletingLastPathComponent(),
        ]
        for root in roots {
            var directory = root
            while true {
                let candidate = directory.appending(path: relativePath)
                if fileManager.fileExists(atPath: candidate.path) {
                    return candidate
                }
                let parent = directory.deletingLastPathComponent()
                if parent.path == directory.path { break }
                directory = parent
            }
            searched.append(root.appending(path: relativePath).path)
        }

        throw FixtureError.canonicalFixtureNotFound(searched: searched)
    }

    /// A valid, deliberately empty contract snapshot: healthy daemon, nothing
    /// discovered yet.
    public static let emptyStatusJSON = """
    {
      "schemaVersion": 2,
      "snapshotRevision": 0,
      "generatedAt": "2026-08-14T08:00:00Z",
      "daemon": {
        "instanceId": "daemon_fixture_empty",
        "version": "0.1.0-dev",
        "state": "ready",
        "startedAt": "2026-08-14T07:59:00Z"
      },
      "repositories": [],
      "environments": [],
      "operations": [],
      "alerts": []
    }
    """
}
