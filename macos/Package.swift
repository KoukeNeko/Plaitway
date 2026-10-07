// swift-tools-version: 6.1
import PackageDescription

let package = Package(
    name: "Plaitway",
    defaultLocalization: "en",
    // grpc-swift 2.x requires macOS 15.
    platforms: [.macOS("15.0")],
    products: [],
    dependencies: [
        .package(url: "https://github.com/grpc/grpc-swift-2.git", exact: "2.4.3"),
        .package(url: "https://github.com/grpc/grpc-swift-nio-transport.git", exact: "2.10.0"),
        .package(url: "https://github.com/grpc/grpc-swift-protobuf.git", exact: "2.4.1"),
        .package(url: "https://github.com/apple/swift-protobuf.git", exact: "1.38.1"),
    ],
    targets: [
        // Generated from proto/ by `make generate`; do not edit.
        .target(
            name: "PlaitwayAPI",
            dependencies: [
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCProtobuf", package: "grpc-swift-protobuf"),
                .product(name: "SwiftProtobuf", package: "swift-protobuf"),
            ]
        ),
        // Daemon connection, observable profile state, saved credentials, profile import and
        // editing (with the secrets kept off the screen), and the helper's installation; no
        // AppKit or SwiftUI.
        .target(
            name: "PlaitwayClient",
            dependencies: [
                "PlaitwayAPI",
                .product(name: "GRPCCore", package: "grpc-swift-2"),
                .product(name: "GRPCNIOTransportHTTP2Posix", package: "grpc-swift-nio-transport"),
            ]
        ),
        .testTarget(
            name: "PlaitwayTests",
            dependencies: ["PlaitwayClient", "PlaitwayAPI"]
        ),
    ],
    swiftLanguageModes: [.v6]
)
