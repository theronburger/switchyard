// swift-tools-version: 6.2

import PackageDescription

let package = Package(
    name: "Switchyard",
    platforms: [.macOS(.v15)],
    products: [
        .library(name: "SwitchyardKit", targets: ["SwitchyardKit"]),
        .executable(name: "SwitchyardApp", targets: ["SwitchyardApp"]),
        .executable(name: "SwitchyardContractCheck", targets: ["SwitchyardContractCheck"]),
    ],
    dependencies: [
        .package(url: "https://github.com/sparkle-project/Sparkle", exact: "2.9.5"),
    ],
    targets: [
        .target(name: "SwitchyardKit"),
        .executableTarget(
            name: "SwitchyardApp",
            dependencies: [
                "SwitchyardKit",
                .product(name: "Sparkle", package: "Sparkle"),
            ],
            resources: [.copy("Resources/SwitchyardTile.png")]
        ),
        .executableTarget(
            name: "SwitchyardContractCheck",
            dependencies: ["SwitchyardKit"]
        ),
        .testTarget(
            name: "SwitchyardTests",
            dependencies: ["SwitchyardApp", "SwitchyardKit"],
            path: "Tests/SwitchyardTests",
            swiftSettings: [
                .enableExperimentalFeature("SwiftTesting"),
            ]
        ),
    ]
)
