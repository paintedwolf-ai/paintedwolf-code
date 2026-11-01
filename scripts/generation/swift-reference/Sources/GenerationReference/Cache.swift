import MLXLLM
import MLXLMCommon

func referenceCache(_ model: Gemma4TextModel, bits: Int) throws -> [any KVCache] {
    let caches = try model.newCache(parameters: nil)
    return try configureReferenceCache(caches, bits: bits)
}

func configureReferenceCache(_ caches: [any KVCache], bits: Int) throws -> [any KVCache] {
    guard [8, 16].contains(bits), caches.count == 24,
          caches.enumerated().allSatisfy({ index, cache in
              cache.offset == 0 && (index % 6 == 5
                  ? cache is KVCacheSimple
                  : cache is RotatingKVCache && cache.maxSize == 512)
          }) else {
        throw ReferenceFailure("Expected E4B's twenty sliding and four global caches")
    }
    return try caches.map { cache in
        if bits == 8, let full = cache as? KVCacheSimple {
            return try full.toQuantized(groupSize: 64, bits: 8)
        }
        return cache
    }
}
