import Darwin
import Foundation
import MLX
import MLXLLM
import MLXLMCommon

struct Specification: Decodable {
    let prompt_tokens: [Int32]
    let generation_tokens: Int
    let trials: Int
    let ple: String
    let prefill_chunk: Int
}

func measure(_ model: Gemma4TextModel, _ spec: Specification) throws -> [String: Any] {
    let cache = try model.newCache(parameters: nil)
    Memory.peakMemory = 0
    var before = rusage()
    getrusage(RUSAGE_SELF, &before)
    let start = ProcessInfo.processInfo.systemUptime
    let prompt = spec.prompt_tokens
    var prefillStepMax = 0.0
    for offset in stride(from: 0, to: prompt.count - 1, by: spec.prefill_chunk) {
        let step = ProcessInfo.processInfo.systemUptime
        let chunk = Array(prompt[offset ..< min(offset + spec.prefill_chunk, prompt.count - 1)])
        _ = model(MLXArray(chunk, [1, chunk.count]), cache: cache)
        eval(cache.flatMap { $0.state })
        prefillStepMax = max(prefillStepMax, ProcessInfo.processInfo.systemUptime - step)
    }
    let lastStep = ProcessInfo.processInfo.systemUptime
    let logits = model(MLXArray([prompt.last!], [1, 1]), cache: cache)
    eval(logits, cache.flatMap { $0.state })
    prefillStepMax = max(prefillStepMax, ProcessInfo.processInfo.systemUptime - lastStep)
    let prefill = ProcessInfo.processInfo.systemUptime - start
    var token = argMax(logits[0, -1], axis: -1)
    eval(token)
    var tokens: [Int32] = []
    let decodingStart = ProcessInfo.processInfo.systemUptime
    var decodeStepMax = 0.0
    for _ in 0 ..< spec.generation_tokens {
        let step = ProcessInfo.processInfo.systemUptime
        tokens.append(token.item(Int32.self))
        let next = model(token.reshaped([1, 1]), cache: cache)
        token = argMax(next[0, -1], axis: -1)
        eval(token, cache.flatMap { $0.state })
        decodeStepMax = max(decodeStepMax, ProcessInfo.processInfo.systemUptime - step)
    }
    let decoding = ProcessInfo.processInfo.systemUptime - decodingStart
    var after = rusage()
    getrusage(RUSAGE_SELF, &after)
    return ["prefill_step_max_seconds": prefillStepMax, "decode_step_max_seconds": decodeStepMax,
            "prefill_seconds": prefill, "decode_seconds": decoding,
            "prompt_tps": Double(prompt.count) / prefill,
            "generation_tps": Double(spec.generation_tokens) / decoding,
            "peak_mlx_bytes": Memory.peakMemory, "live_mlx_bytes": Memory.activeMemory,
            "retained_mlx_bytes": Memory.cacheMemory, "process_peak_rss_bytes": after.ru_maxrss,
            "major_faults": after.ru_majflt - before.ru_majflt,
            "minor_faults": after.ru_minflt - before.ru_minflt,
            "block_inputs": after.ru_inblock - before.ru_inblock, "tokens": tokens]
}

do {
    if let path = ProcessInfo.processInfo.environment["PW_REFERENCE_METALLIB"] {
        GPU.metallib = URL(fileURLWithPath: path)
    }
    guard CommandLine.arguments.count == 3 else {
        throw ReferenceFailure("Expected model and benchmark input paths")
    }
    let directory = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
    let input = URL(fileURLWithPath: CommandLine.arguments[2])
    let spec = try JSONDecoder().decode(Specification.self, from: Data(contentsOf: input))
    guard (1 ... 256).contains(spec.prefill_chunk), !spec.prompt_tokens.isEmpty, (1 ... 4096).contains(spec.generation_tokens),
          spec.prompt_tokens.count + spec.generation_tokens <= 65536,
          (1 ... 20).contains(spec.trials), ["mapped", "resident"].contains(spec.ple),
          spec.prompt_tokens.allSatisfy({ $0 >= 0 && $0 < 262144 }) else {
        throw ReferenceFailure("Benchmark dimensions exceed their bounds")
    }
    Memory.cacheLimit = 128 << 20
    let loadingStart = ProcessInfo.processInfo.systemUptime
    let model = try loadReference(directory: directory, mappedPLE: spec.ple == "mapped")
    let loadingSeconds = ProcessInfo.processInfo.systemUptime - loadingStart
    let loadingPeak = Memory.peakMemory
    var loadingUsage = rusage()
    getrusage(RUSAGE_SELF, &loadingUsage)
    for trial in 0 ... spec.trials {
        var result = try measure(model, spec)
        result["load_seconds"] = loadingSeconds
        result["load_peak_mlx_bytes"] = loadingPeak
        result["load_peak_process_rss_bytes"] = loadingUsage.ru_maxrss
        result["trial"] = trial
        result["warmup"] = trial == 0
        result["ple"] = spec.ple
        let line = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
        FileHandle.standardOutput.write(line)
        FileHandle.standardOutput.write(Data([10]))
        Memory.clearCache()
    }
} catch {
    FileHandle.standardError.write(Data("\(error)\n".utf8))
    exit(1)
}
