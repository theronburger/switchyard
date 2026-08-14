import Foundation

public enum AppLaunchConfiguration: Sendable, Equatable {
    case live
    case fixture(FixtureScenario)

    public static func resolve(
        arguments: [String] = CommandLine.arguments,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> AppLaunchConfiguration {
        for (index, argument) in arguments.enumerated() {
            if argument == "--fixture" {
                let value = arguments.indices.contains(index + 1) ? arguments[index + 1] : ""
                return .fixture(FixtureScenario(rawValue: value) ?? .canonical)
            }
            if argument.hasPrefix("--fixture=") {
                let value = String(argument.dropFirst("--fixture=".count))
                return .fixture(FixtureScenario(rawValue: value) ?? .canonical)
            }
        }
        if let value = environment["SWITCHYARD_FIXTURE"] {
            return .fixture(FixtureScenario(rawValue: value) ?? .canonical)
        }
        return .live
    }
}
