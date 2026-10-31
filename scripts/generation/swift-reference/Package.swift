// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "GenerationReference",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "generation-swift-reference", targets: ["GenerationReference"])],
    dependencies: [
        .package(url: "https://github.com/ml-explore/mlx-swift-lm", revision: "128f49f1db38ea7b949f2ebb60967def8b76f58e"),
        .package(url: "https://github.com/ml-explore/mlx-swift", revision: "7ede4fd6d05f63305c1c0ebf4f8c1909a637c574"),
    ],
    targets: [.executableTarget(name: "GenerationReference", dependencies: [
        .product(name: "MLXLLM", package: "mlx-swift-lm"),
        .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
        .product(name: "MLX", package: "mlx-swift"),
        .product(name: "MLXNN", package: "mlx-swift"),
    ]), .testTarget(name: "GenerationReferenceTests", dependencies: [
        "GenerationReference", .product(name: "MLX", package: "mlx-swift"),
    ])]
)
