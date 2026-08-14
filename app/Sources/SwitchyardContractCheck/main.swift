import Foundation
import SwitchyardKit

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: SwitchyardContractCheck STATUS_FIXTURE\n".utf8))
    exit(2)
}

do {
    let fixtureURL = URL(fileURLWithPath: CommandLine.arguments[1])
    let snapshot = try ContractDecoder().decode(
        StatusSnapshot.self,
        from: Data(contentsOf: fixtureURL)
    )
    guard snapshot.schemaVersion == contractSchemaVersion else {
        throw CheckError("unexpected schema version \(snapshot.schemaVersion)")
    }
    guard snapshot.environments.first?.displayName == "DEMO-830" else {
        throw CheckError("canonical environment is missing")
    }
    guard snapshot.environments.first?.health == .degraded else {
        throw CheckError("canonical environment health did not decode")
    }

    let futureHealth = try ContractDecoder().decode(
        Health.self,
        from: Data("\"future-health\"".utf8)
    )
    guard futureHealth == .unknown else {
        throw CheckError("unknown enum value did not map to unknown")
    }
} catch {
    FileHandle.standardError.write(Data("contract check failed: \(error)\n".utf8))
    exit(1)
}

private struct CheckError: Error, CustomStringConvertible {
    let description: String

    init(_ description: String) {
        self.description = description
    }
}
