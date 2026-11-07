import Foundation
import XCTest
@testable import GenerationReference

final class FileMappingTests: XCTestCase {
    func testMappingRetainsTheOpenedFileAfterPathReplacement() throws {
        let path = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: path) }
        let original = Data(repeating: 37, count: 8192)
        try original.write(to: path)
        let file = try FileHandle(forReadingFrom: path)
        let mapped = try mapReadOnly(file)
        try file.close()
        try Data(repeating: 92, count: 8192).write(to: path, options: .atomic)
        XCTAssertEqual(mapped, original)
    }

    func testEmptyFileIsRejected() throws {
        let path = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: path) }
        try Data().write(to: path)
        let file = try FileHandle(forReadingFrom: path)
        defer { try? file.close() }
        XCTAssertThrowsError(try mapReadOnly(file))
    }
}
