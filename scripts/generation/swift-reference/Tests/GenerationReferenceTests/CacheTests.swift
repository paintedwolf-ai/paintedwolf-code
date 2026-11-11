import MLXLMCommon
import XCTest
@testable import GenerationReference

final class CacheTests: XCTestCase {
    private func caches() -> [any KVCache] {
        (0 ..< 24).map { index in
            if index % 6 == 5 { return KVCacheSimple() }
            return RotatingKVCache(maxSize: 512)
        }
    }

    func testOnlyGlobalCachesAreQuantized() throws {
        let original = caches()
        let result = try configureReferenceCache(original, bits: 8)
        XCTAssertEqual(result.filter { $0 is QuantizedKVCache }.count, 4)
        for index in 0 ..< 24 where index % 6 != 5 {
            XCTAssertTrue((result[index] as AnyObject) === (original[index] as AnyObject))
        }
        let unchanged = try configureReferenceCache(original, bits: 16)
        for index in 0 ..< 24 { XCTAssertTrue((unchanged[index] as AnyObject) === (original[index] as AnyObject)) }
    }

    func testUnsupportedCacheLayoutIsRejected() {
        XCTAssertThrowsError(try configureReferenceCache([], bits: 8))
        XCTAssertThrowsError(try configureReferenceCache(caches(), bits: 4))
        var reordered = caches()
        reordered.swapAt(0, 5)
        XCTAssertThrowsError(try configureReferenceCache(reordered, bits: 8))
        var oversized = caches()
        oversized[0] = RotatingKVCache(maxSize: 1024)
        XCTAssertThrowsError(try configureReferenceCache(oversized, bits: 16))
    }
}
