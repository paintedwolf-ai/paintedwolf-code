import Foundation
import MLX
import XCTest
@testable import GenerationReference

final class MappedPLETests: XCTestCase {
    override func setUp() {
        super.setUp()
        if let path = ProcessInfo.processInfo.environment["PW_REFERENCE_METALLIB"] {
            GPU.metallib = URL(fileURLWithPath: path)
        }
    }

    func testSelectedRowsMatchResidentDequantization() throws {
        for bits in [4, 6, 8] { try checkSelectedRows(bits: bits) }
    }

    private func checkSelectedRows(bits: Int) throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("model.safetensors")
        let values = (MLXArray((0 ..< 512).map(Float.init), [4, 128]) / 101).asType(.float16)
        let packed = quantized(values, groupSize: 64, bits: bits)
        let prefix = "model.embed_tokens_per_layer"
        try save(arrays: [prefix + ".weight": packed.wq, prefix + ".scales": packed.scales,
                         prefix + ".biases": try XCTUnwrap(packed.biases)], url: file)
        let embedding = try MappedPLE(url: file, prefix: prefix, bits: bits)
        let indices = MLXArray([Int32(3), 1, 3, 0], [1, 4])
        let actual = embedding(indices)
        let resident = dequantized(packed.wq, scales: packed.scales, biases: packed.biases,
                                   groupSize: 64, bits: bits)
        let expected = resident[indices]
        eval(actual, expected)
        XCTAssertEqual(actual.asType(.float32).asArray(Float.self), expected.asType(.float32).asArray(Float.self))
        XCTAssertEqual(embedding.parameters().flattened().map { $0.1.size }.reduce(0, +), 1)
    }

    func testOversizedHeaderIsRejectedBeforeMapping() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: file) }
        try Data(repeating: 255, count: 8).write(to: file)
        XCTAssertThrowsError(try MappedPLE(url: file, prefix: "ple"))
    }
}
