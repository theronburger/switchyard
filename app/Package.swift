// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "Switchyard",
    platforms: [.macOS(.v15)],
    products: [.executable(name: "SwitchyardApp", targets: ["SwitchyardApp"])],
    dependencies: [.package(url: "https://github.com/sparkle-project/Sparkle", exact: "2.9.5")],
    targets: [
        .executableTarget(name: "SwitchyardApp", dependencies: [.product(name: "Sparkle", package: "Sparkle")], resources: [.copy("Resources/SwitchyardTile.png")]),
        .testTarget(name: "SwitchyardTests", dependencies: ["SwitchyardApp"]),
    ]
)
