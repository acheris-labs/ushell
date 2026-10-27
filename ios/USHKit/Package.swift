// swift-tools-version:6.2
import PackageDescription

let package = Package(
    name: "USHKit",
    platforms: [.iOS(.v26), .macOS(.v15)],
    products: [
        .library(name: "USHCore", targets: ["USHCore"]),
        .library(name: "USHClient", targets: ["USHClient"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.80.0"),
        .package(url: "https://github.com/apple/swift-nio-ssh.git", from: "0.3.7"),
    ],
    targets: [
        .target(name: "USHCore"),
        .target(name: "USHClient", dependencies: [
            "USHCore",
            .product(name: "NIOCore", package: "swift-nio"),
            .product(name: "NIOPosix", package: "swift-nio"),
            .product(name: "NIOSSH", package: "swift-nio-ssh"),
        ]),
        .testTarget(name: "USHCoreTests", dependencies: ["USHCore"]),
        .testTarget(name: "USHClientTests", dependencies: [
            "USHClient",
            .product(name: "NIOCore", package: "swift-nio"),
            .product(name: "NIOPosix", package: "swift-nio"),
        ]),
    ],
    swiftLanguageModes: [.v5]
)
