"""Bounded token-row PLE lookup for the MLX performance reference."""

import json
from pathlib import Path
import struct

import mlx.core as mx
import mlx.nn as nn
import numpy as np


class MappedPLE(nn.Module):
    def __init__(self, directory, prefix, group_size, bits):
        super().__init__()
        if group_size != 64 or bits != 4:
            raise ValueError("the comparison PLE reader requires affine q4 group-64")
        self._group_size, self._bits = group_size, bits
        tensors = {}
        wanted = {prefix + "." + name for name in ("weight", "scales", "biases")}
        for path in sorted(Path(directory).glob("model*.safetensors")):
            with path.open("rb") as source:
                raw = source.read(8)
                if len(raw) != 8:
                    raise ValueError("truncated tensor header")
                length, = struct.unpack("<Q", raw)
                if length > 16 << 20:
                    raise ValueError("tensor header exceeds its bound")
                header = json.loads(source.read(length))
            for name in wanted & header.keys():
                if name in tensors:
                    raise ValueError("duplicate PLE tensor")
                entry = header[name]
                dtype = {"U32": np.dtype("<u4"), "F16": np.dtype("<f2")}.get(entry["dtype"])
                shape = entry["shape"]
                start, end = entry["data_offsets"]
                if (dtype is None or len(shape) != 2 or any(n <= 0 for n in shape)
                        or start < 0 or end - start != shape[0] * shape[1] * dtype.itemsize
                        or 8 + length + end > path.stat().st_size):
                    raise ValueError("invalid PLE tensor layout")
                tensors[name] = np.memmap(path, dtype=dtype, mode="r", offset=8 + length + start, shape=tuple(shape))
        if tensors.keys() != wanted:
            raise ValueError("missing PLE tensors")
        self._packed = tensors[prefix + ".weight"]
        self._scales = tensors[prefix + ".scales"]
        self._biases = tensors[prefix + ".biases"]
        width = self._packed.shape[1] * 8
        if self._scales.shape != (self._packed.shape[0], width // 64) or self._biases.shape != self._scales.shape:
            raise ValueError("inconsistent PLE quantization shapes")
        self._width = width

    def __call__(self, ids):
        indices = np.asarray(ids.tolist(), dtype=np.int64)
        if indices.size > 256 or np.any(indices < 0) or np.any(indices >= self._packed.shape[0]):
            raise ValueError("PLE lookup exceeds token or vocabulary bounds")
        flat = indices.reshape(-1)
        packed = mx.array(np.array(self._packed[flat], copy=True))
        scales = mx.array(np.array(self._scales[flat], copy=True))
        biases = mx.array(np.array(self._biases[flat], copy=True))
        result = mx.dequantize(packed, scales, biases, group_size=self._group_size, bits=self._bits)
        return result.reshape(*indices.shape, self._width)


def replace_ple(model, directory, config):
    text = model.language_model if hasattr(model, "language_model") else model
    prefix = "language_model.model.embed_tokens_per_layer" if hasattr(model, "language_model") else "model.embed_tokens_per_layer"
    quantization = config["quantization"]
    parameters = quantization.get(prefix, quantization)
    text.model.embed_tokens_per_layer = MappedPLE(directory, prefix, parameters["group_size"], parameters["bits"])
