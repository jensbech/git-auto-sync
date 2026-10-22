// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "GitAutoSyncMenuBar",
    platforms: [
        .macOS(.v14)
    ],
    targets: [
        .executableTarget(
            name: "GitAutoSyncMenuBar",
            path: "Sources",
            exclude: ["Resources/AppIcon.icns"]
        ),
        .testTarget(
            name: "GitAutoSyncMenuBarTests",
            dependencies: ["GitAutoSyncMenuBar"],
            path: "Tests/GitAutoSyncMenuBarTests",
            resources: [.copy("Fixtures")]
        ),
    ]
)
