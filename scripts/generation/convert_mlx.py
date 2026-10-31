"""Convert a verified merged E4B checkpoint with bounded tensor staging."""

import argparse
import hashlib
import json
import math
from pathlib import Path
import shutil
import struct

import mlx.core as mx
import numpy as np

TILE_BYTES = 16 << 20


def digest(path):
    result = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            result.update(block)
    return result.hexdigest()


def layout(source, config):
    text = config["text_config"]
    if (text["num_hidden_layers"], text["hidden_size"], text["hidden_size_per_layer_input"], text["num_kv_shared_layers"]) != (42, 2560, 256, 18):
        raise ValueError("conversion requires the E4B text architecture")
    with source.open("rb") as stream:
        raw = stream.read(8)
        if len(raw) != 8:
            raise ValueError("truncated source header")
        size, = struct.unpack("<Q", raw)
        if size > 16 << 20:
            raise ValueError("source header exceeds its bound")
        header = json.loads(stream.read(size))
    result = []
    for name, spec in header.items():
        if not name.startswith("model.language_model."):
            continue
        relative = name[len("model.language_model."):]
        parts = relative.split(".")
        if parts[0] == "layers" and int(parts[1]) >= 24 and parts[2:4] in [
            ["self_attn", "k_proj"], ["self_attn", "v_proj"], ["self_attn", "k_norm"]
        ]:
            continue
        if any(part in {"rotary_emb", "input_max", "input_min", "output_max", "output_min"} for part in parts):
            continue
        shape = spec["shape"]
        dtype = {"F32": "<f4", "F16": "<f2", "BF16": "<u2"}.get(spec["dtype"])
        start, end = spec["data_offsets"]
        if (dtype is None or not shape or any(type(n) is not int or n <= 0 for n in shape)
                or start < 0 or end - start != math.prod(shape) * np.dtype(dtype).itemsize
                or 8 + size + end > source.stat().st_size):
            raise ValueError("invalid source tensor " + name)
        quantized = len(shape) == 2 and name.endswith(".weight") and shape[-1] % 64 == 0
        result.append({"name": "language_model.model." + relative, "source": spec,
                       "offset": 8 + size + start, "dtype": dtype, "quantized": quantized})
    if not result:
        raise ValueError("checkpoint has no E4B text tensors")
    return result


def output_layout(tensors):
    header, cursor = {}, 0
    for tensor in tensors:
        shape = tensor["source"]["shape"]
        name = tensor["name"]
        entries = [(name, "F16", shape)]
        if tensor["quantized"]:
            base = name.removesuffix(".weight")
            entries = [(name, "U32", [shape[0], shape[1] // 8]),
                       (base + ".scales", "F16", [shape[0], shape[1] // 64]),
                       (base + ".biases", "F16", [shape[0], shape[1] // 64])]
        tensor["outputs"] = [entry[0] for entry in entries]
        for name, dtype, dims in entries:
            count = math.prod(dims) * (4 if dtype == "U32" else 2)
            header[name] = {"dtype": dtype, "shape": dims, "data_offsets": [cursor, cursor + count]}
            cursor += count
    raw = json.dumps(header, separators=(",", ":")).encode()
    raw += b" " * ((-len(raw)) % 8)
    return header, raw, cursor


def write_tensor(output, source, tensor, header, data_start):
    spec = tensor["source"]
    shape = spec["shape"]
    mapped = np.memmap(source, mode="r", dtype=tensor["dtype"], offset=tensor["offset"], shape=tuple(shape))
    rows = max(1, TILE_BYTES // (math.prod(shape[1:]) * 4))
    for start in range(0, shape[0], rows):
        block = np.array(mapped[start:start + rows], copy=True)
        if spec["dtype"] == "BF16":
            block = (block.astype(np.uint32) << 16).view(np.float32)
        if not np.isfinite(block).all():
            raise ValueError("non-finite source tensor " + tensor["name"])
        half = block.astype(np.float16)
        if not np.isfinite(half).all():
            raise ValueError("source tensor exceeds float16 range")
        if tensor["quantized"]:
            arrays = mx.quantize(mx.array(half), group_size=64, bits=4)
            mx.eval(arrays)
            chunks = [np.array(array) for array in arrays]
        else:
            chunks = [half]
        for name, chunk in zip(tensor["outputs"], chunks):
            target = header[name]
            row_bytes = math.prod(target["shape"][1:]) * (4 if target["dtype"] == "U32" else 2)
            output.seek(data_start + target["data_offsets"][0] + start * row_bytes)
            output.write(chunk.tobytes(order="C"))
    del mapped


def convert(source, destination, expected):
    if destination.exists():
        raise ValueError("conversion requires a new output directory")
    weights = source / "model.safetensors"
    if digest(weights) != expected:
        raise ValueError("source checkpoint digest mismatch")
    config = json.loads((source / "config.json").read_text())
    tensors = layout(weights, config)
    header, raw, data_bytes = output_layout(tensors)
    destination.parent.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(destination.parent).free < data_bytes + (1 << 30):
        raise ValueError("insufficient space for the converted checkpoint")
    destination.mkdir()
    try:
        mx.set_default_device(mx.cpu)
        mx.set_cache_limit(64 << 20)
        target = destination / "model.safetensors"
        with target.open("xb") as output:
            output.write(struct.pack("<Q", len(raw)))
            output.write(raw)
            output.truncate(8 + len(raw) + data_bytes)
            for tensor in tensors:
                write_tensor(output, weights, tensor, header, 8 + len(raw))
                print(tensor["name"], flush=True)
        config["quantization"] = {"group_size": 64, "bits": 4, "mode": "affine"}
        config["dtype"] = config["text_config"]["dtype"] = "float16"
        (destination / "config.json").write_text(json.dumps(config, indent=2) + "\n")
        for name in ["tokenizer.json", "tokenizer_config.json", "chat_template.jinja", "generation_config.json"]:
            shutil.copyfile(source / name, destination / name)
        manifest = {"schema": "paintedwolf-mlx-conversion/1", "source_sha256": expected,
                    "recipe": "affine-q4-group64-f16", "source_tile_bytes": TILE_BYTES,
                    "weights_sha256": digest(target), "weights_bytes": target.stat().st_size}
        (destination / "conversion.json").write_text(json.dumps(manifest, indent=2) + "\n")
    except BaseException:
        shutil.rmtree(destination)
        raise


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-sha256", required=True)
    args = parser.parse_args()
    convert(args.source, args.output, args.source_sha256)
