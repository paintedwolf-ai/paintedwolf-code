import Foundation
import MLX
import MLXNN

struct ReferenceFailure: Error {
    let message: String
    init(_ message: String) { self.message = message }
}

final class MappedPLE: Embedding {
    private let mapped: Data
    private let tensors: [Tensor]
    private let rows: Int
    private let width: Int
    private let bits: Int

    private struct Tensor {
        let offset: Int
        let columns: Int
        let dtype: DType
        var rowBytes: Int { columns * dtype.size }
    }

    init(url: URL, prefix: String, bits: Int = 4) throws {
        guard [4, 6, 8].contains(bits) else { throw ReferenceFailure("Unsupported PLE precision") }
        let file = try FileHandle(forReadingFrom: url)
        defer { try? file.close() }
        guard let first = try file.read(upToCount: 8), first.count == 8 else {
            throw ReferenceFailure("Truncated tensor header")
        }
        let length = first.withUnsafeBytes { UInt64(littleEndian: $0.loadUnaligned(as: UInt64.self)) }
        guard length <= 16 << 20 else { throw ReferenceFailure("Tensor header exceeds its bound") }
        let size = Int(length)
        guard let raw = try file.read(upToCount: size), raw.count == size,
              let header = try JSONSerialization.jsonObject(with: raw) as? [String: Any] else {
            throw ReferenceFailure("Invalid tensor header")
        }
        let data = try mapReadOnly(file)
        var entries: [Tensor] = []
        var rowCount = 0
        for (name, kind) in [("weight", "U32"), ("scales", "F16"), ("biases", "F16")] {
            guard let spec = header[prefix + "." + name] as? [String: Any],
                  let shape = spec["shape"] as? [Int], shape.count == 2,
                  shape[0] > 0, shape[1] > 0,
                  let offsets = spec["data_offsets"] as? [Int], offsets.count == 2,
                  offsets[0] >= 0, offsets[1] >= offsets[0],
                  offsets[1] <= data.count - 8 - size, spec["dtype"] as? String == kind else {
                throw ReferenceFailure("Invalid mapped PLE layout")
            }
            let dtype: DType = kind == "U32" ? .uint32 : .float16
            let bytes = shape[0].multipliedReportingOverflow(by: shape[1])
            let length = bytes.partialValue.multipliedReportingOverflow(by: dtype.size)
            guard !bytes.overflow, !length.overflow, length.partialValue == offsets[1] - offsets[0],
                  rowCount == 0 || rowCount == shape[0] else {
                throw ReferenceFailure("Invalid mapped PLE shape")
            }
            rowCount = shape[0]
            entries.append(Tensor(offset: 8 + size + offsets[0], columns: shape[1], dtype: dtype))
        }
        guard entries[0].columns * 32 % bits == 0,
              entries[0].columns * 32 / bits == entries[1].columns * 64,
              entries[1].columns == entries[2].columns else {
            throw ReferenceFailure("Inconsistent PLE quantization")
        }
        self.mapped = data
        self.tensors = entries
        self.rows = rowCount
        self.width = entries[0].columns * 32 / bits
        self.bits = bits
        super.init(weight: MLXArray.zeros([1, 1]))
    }

    override var shape: (Int, Int) { (rows, width) }

    override func callAsFunction(_ ids: MLXArray) -> MLXArray {
        let indices = ids.asArray(Int32.self)
        precondition(!indices.isEmpty && indices.count <= 256)
        precondition(indices.allSatisfy { $0 >= 0 && $0 < rows })
        let arrays = tensors.map { tensor in
            var selected = Data(count: indices.count * tensor.rowBytes)
            selected.withUnsafeMutableBytes { destination in
                mapped.withUnsafeBytes { source in
                    for (index, token) in indices.enumerated() {
                        destination.baseAddress!.advanced(by: index * tensor.rowBytes).copyMemory(
                            from: source.baseAddress!.advanced(by: tensor.offset + Int(token) * tensor.rowBytes),
                            byteCount: tensor.rowBytes)
                    }
                }
            }
            return MLXArray(selected, [indices.count, tensor.columns], dtype: tensor.dtype)
        }
        return dequantized(arrays[0], scales: arrays[1], biases: arrays[2], groupSize: 64, bits: bits)
            .reshaped(ids.shape + [width])
    }
}
