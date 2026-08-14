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
    targets: [
        .target(name: "SwitchyardKit"),
        .executableTarget(
            name: "SwitchyardApp",
            dependencies: ["SwitchyardKit"]
        ),
        .executableTarget(
            name: "SwitchyardContractCheck",
            dependencies: ["SwitchyardKit"]
        ),
    ]
)
