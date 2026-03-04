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
            resources: [.copy("Resources/AppIcon.icns")]
        )
    ]
)
