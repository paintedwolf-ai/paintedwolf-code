import Foundation
import MLX
import MLXLLM
import MLXLMCommon
import MLXNN

func loadReference(directory: URL, mappedPLE: Bool) throws -> Gemma4TextModel {
    let raw = try Data(contentsOf: directory.appendingPathComponent("config.json"))
    guard let outer = try JSONSerialization.jsonObject(with: raw) as? [String: Any],
          let text = outer["text_config"] as? [String: Any],
          text["hidden_size"] as? Int == 2560, text["num_hidden_layers"] as? Int == 42,
          let quantization = outer["quantization"] as? [String: Any],
          quantization["bits"] as? Int == 4, quantization["group_size"] as? Int == 64 else {
        throw ReferenceFailure("Expected affine Q4 group-64 E4B checkpoint")
    }
    let config = try JSONDecoder().decode(Gemma4TextConfiguration.self,
        from: JSONSerialization.data(withJSONObject: text))
    let model = Gemma4TextModel(config)
    let file = directory.appendingPathComponent("model.safetensors")
    let ple = "model.embed_tokens_per_layer"
    var weights: [String: MLXArray] = [:]
    for (key, value) in try loadArrays(url: file) {
        guard key.hasPrefix("language_model.") else {
            throw ReferenceFailure("Unexpected checkpoint namespace")
        }
        let name = String(key.dropFirst("language_model.".count))
        if mappedPLE && name.hasPrefix(ple + ".") { continue }
        weights[name] = value
    }
    weights = model.sanitize(weights: weights)
    quantize(model: model) { path, _ in
        weights[path + ".scales"] == nil ? nil : (groupSize: 64, bits: 4, mode: .affine)
    }
    if mappedPLE {
        let embedding = try MappedPLE(url: file, prefix: "language_model." + ple)
        try model.update(modules: ModuleChildren.unflattened([(ple, embedding)]), verify: [.noUnusedKeys])
        weights[ple + ".weight"] = embedding.weight
    }
    try model.update(parameters: ModuleParameters.unflattened(weights), verify: [.all])
    try model.prepare()
    eval(model)
    return model
}
