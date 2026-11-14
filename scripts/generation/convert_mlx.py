"""Convert a verified Gemma 4 student checkpoint to MLX with bounded tensor staging.

The output is mlx-lm's Gemma 4 layout (`language_model.model.*`), which the native MLX
runtime loads for every student tier: vision and audio towers are dropped, the K/V
projections of KV-shared layers are dropped, and a mixture-of-experts layer's fused
expert tensors are split into `switch_glu` gate, up and down projections. A tensor whose
last axis is a multiple of the group size is quantized affine, group 64: router
projections at router_bits, token embeddings at embedding_bits, per-layer embeddings at
ple_bits, every other matrix at weight_bits. The source may be one `model.safetensors`
or shards listed by `model.safetensors.index.json`.
"""

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
GROUP = 64
SINGLE = "model.safetensors"
INDEX = "model.safetensors.index.json"
COPIED = ["tokenizer.json", "tokenizer_config.json", "chat_template.jinja", "generation_config.json"]
# The pinned student architectures, by the text-config facts that tell them apart.
PROFILES = {
    "e4b": {"num_hidden_layers": 42, "hidden_size": 2560, "hidden_size_per_layer_input": 256, "num_kv_shared_layers": 18,
            "enable_moe_block": False},
    "12b": {"num_hidden_layers": 48, "hidden_size": 3840, "hidden_size_per_layer_input": 0, "num_kv_shared_layers": 0,
            "enable_moe_block": False},
    "26b-a4b": {"num_hidden_layers": 30, "hidden_size": 2816, "hidden_size_per_layer_input": 0, "num_kv_shared_layers": 0,
                "enable_moe_block": True, "num_experts": 128},
}
PRECISION = {"weight_bits": 4, "ple_bits": 4, "embedding_bits": 4, "router_bits": 8}
DROPPED_PARTS = {"rotary_emb", "input_max", "input_min", "output_max", "output_min"}
SHARED_KV_PARTS = (["self_attn", "k_proj"], ["self_attn", "v_proj"], ["self_attn", "k_norm"])


def digest(path):
    result = hashlib.sha256()
    with Path(path).open("rb") as source:
        for block in iter(lambda: source.read(1 << 20), b""):
            result.update(block)
    return result.hexdigest()


def weight_files(source):
    """Every source tensor's shard when an index names them, or None for one model.safetensors."""
    source = Path(source)
    if (source / INDEX).exists():
        weight_map = json.loads((source / INDEX).read_text())["weight_map"]
        return {name: source / shard for name, shard in weight_map.items()}
    if not (source / SINGLE).exists():
        raise ValueError("source has neither %s nor %s" % (SINGLE, INDEX))
    return None


def source_identity(source):
    """The SHA-256 a caller pins: of model.safetensors itself, or of the sorted
    `<name>  <sha256>` lines of the index and every shard."""
    source = Path(source)
    files = weight_files(source)
    if files is None:
        return digest(source / SINGLE)
    names = sorted({INDEX, *(path.name for path in files.values())})
    lines = "".join("%s  %s\n" % (name, digest(source / name)) for name in names)
    return hashlib.sha256(lines.encode()).hexdigest()


def text_config(config):
    return config.get("text_config", config)


def profile_of(config, profiles):
    text = text_config(config)
    for name, facts in profiles.items():
        if all(text.get(key, False if key == "enable_moe_block" else None) == value for key, value in facts.items()):
            return name
    raise ValueError("conversion requires a pinned student architecture: %s" % ", ".join(sorted(profiles)))


def read_header(path):
    with Path(path).open("rb") as stream:
        raw = stream.read(8)
        if len(raw) != 8:
            raise ValueError("truncated source header")
        size, = struct.unpack("<Q", raw)
        if size > 16 << 20:
            raise ValueError("source header exceeds its bound")
        header = json.loads(stream.read(size))
    header.pop("__metadata__", None)
    return size, header


def source_tensors(source):
    """(name, file, header size, spec) for every tensor of the source checkpoint."""
    files = weight_files(source)
    paths = sorted(set(files.values())) if files is not None else [Path(source) / SINGLE]
    out = []
    for path in paths:
        size, header = read_header(path)
        for name, spec in header.items():
            if files is not None and files.get(name) != path:
                raise ValueError("tensor %s is not in the shard the index names" % name)
            out.append((name, path, size, spec))
    return out


def language_tensors(tensors):
    """The text model's tensors with their names below it. A multimodal checkpoint keeps
    them under model.language_model and its towers beside them; a text-only one keeps
    them directly under model."""
    multimodal = any(t[0].startswith("model.language_model.") for t in tensors)
    prefix = "model.language_model." if multimodal else "model."
    for tensor in tensors:
        name = tensor[0]
        if name.startswith(prefix):
            yield name[len(prefix):], tensor
        elif not name.startswith("model."):
            raise ValueError("unexpected tensor outside the model: " + name)


def family(relative):
    if relative.endswith("router.proj.weight"):
        return "router_bits"
    if relative == "embed_tokens_per_layer.weight":
        return "ple_bits"
    if relative == "embed_tokens.weight":
        return "embedding_bits"
    return "weight_bits"


def outputs(relative, shape):
    """The MLX tensors one source tensor becomes, as (name, slice of axis -2 or None)."""
    if relative.endswith(".experts.gate_up_proj"):
        if len(shape) != 3 or shape[-2] % 2:
            raise ValueError("fused expert projection has an unexpected shape: " + relative)
        base, half = relative.removesuffix(".gate_up_proj"), shape[-2] // 2
        return [(base + ".switch_glu.gate_proj.weight", (0, half)), (base + ".switch_glu.up_proj.weight", (half, shape[-2]))]
    if relative.endswith(".experts.down_proj"):
        return [(relative.removesuffix(".down_proj") + ".switch_glu.down_proj.weight", None)]
    return [(relative, None)]


def layout(source, config, precision, profiles=PROFILES):
    """The profile and the output tensors, each tied to the source bytes and slice it reads."""
    text = text_config(config)
    profile = profile_of(config, profiles)
    first_shared = text["num_hidden_layers"] - text.get("num_kv_shared_layers", 0)
    result = []
    for relative, (name, path, size, spec) in language_tensors(source_tensors(source)):
        parts = relative.split(".")
        if any(part in DROPPED_PARTS for part in parts):
            continue
        if parts[0] == "layers" and int(parts[1]) >= first_shared and parts[2:4] in SHARED_KV_PARTS:
            continue
        shape = spec["shape"]
        dtype = {"F32": "<f4", "F16": "<f2", "BF16": "<u2"}.get(spec["dtype"])
        start, end = spec["data_offsets"]
        if (dtype is None or not shape or any(type(n) is not int or n <= 0 for n in shape)
                or start < 0 or end - start != math.prod(shape) * np.dtype(dtype).itemsize
                or 8 + size + end > path.stat().st_size):
            raise ValueError("invalid source tensor " + name)
        for out_name, cut in outputs(relative, shape):
            out_shape = list(shape) if cut is None else [*shape[:-2], cut[1] - cut[0], shape[-1]]
            quantized = len(out_shape) >= 2 and out_name.endswith(".weight") and out_shape[-1] % GROUP == 0
            result.append({"name": "language_model.model." + out_name, "path": path, "source": spec, "shape": out_shape,
                           "cut": cut, "offset": 8 + size + start, "dtype": dtype, "quantized": quantized,
                           "bits": precision[family(relative)], "family": family(relative)})
    if not result:
        raise ValueError("checkpoint has no %s text tensors" % profile)
    return profile, result


def output_layout(tensors):
    header, cursor = {}, 0
    for tensor in tensors:
        shape, name = tensor["shape"], tensor["name"]
        entries = [(name, "F16", shape)]
        if tensor["quantized"]:
            base = name.removesuffix(".weight")
            entries = [(name, "U32", [*shape[:-1], shape[-1] * tensor["bits"] // 32]),
                       (base + ".scales", "F16", [*shape[:-1], shape[-1] // GROUP]),
                       (base + ".biases", "F16", [*shape[:-1], shape[-1] // GROUP])]
        tensor["outputs"] = [entry[0] for entry in entries]
        for out, dtype, dims in entries:
            count = math.prod(dims) * (4 if dtype == "U32" else 2)
            header[out] = {"dtype": dtype, "shape": dims, "data_offsets": [cursor, cursor + count]}
            cursor += count
    raw = json.dumps(header, separators=(",", ":")).encode()
    raw += b" " * ((-len(raw)) % 8)
    return header, raw, cursor


def write_tensor(output, tensor, header, data_start):
    spec = tensor["source"]
    shape = spec["shape"]
    mapped = np.memmap(tensor["path"], mode="r", dtype=tensor["dtype"], offset=tensor["offset"], shape=tuple(shape))
    rows = max(1, TILE_BYTES // (math.prod(shape[1:]) * 4))
    for start in range(0, shape[0], rows):
        block = np.array(mapped[start:start + rows], copy=True)
        if tensor["cut"] is not None:
            block = np.ascontiguousarray(block[..., tensor["cut"][0]:tensor["cut"][1], :])
        if spec["dtype"] == "BF16":
            block = (block.astype(np.uint32) << 16).view(np.float32)
        if not np.isfinite(block).all():
            raise ValueError("non-finite source tensor " + tensor["name"])
        half = block.astype(np.float16)
        if not np.isfinite(half).all():
            raise ValueError("source tensor exceeds float16 range")
        if tensor["quantized"]:
            arrays = mx.quantize(mx.array(half), group_size=GROUP, bits=tensor["bits"])
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


def check_precision(precision):
    if set(precision) != set(PRECISION) or any(type(bits) is not int or bits not in {4, 6, 8} for bits in precision.values()):
        raise ValueError("precision requires %s in 4, 6, 8" % ", ".join(sorted(PRECISION)))


def quantization(tensors, precision):
    """mlx-lm's quantization record: weight_bits by default, every other width per module."""
    out = {"group_size": GROUP, "bits": precision["weight_bits"], "mode": "affine"}
    for tensor in tensors:
        if tensor["quantized"] and tensor["family"] != "weight_bits":
            out[tensor["name"].removesuffix(".weight")] = {"group_size": GROUP, "bits": tensor["bits"], "mode": "affine"}
    return out


def convert(source, destination, expected, precision=None, profiles=PROFILES):
    source, destination = Path(source), Path(destination)
    precision = dict(PRECISION, **(precision or {}))
    check_precision(precision)
    if destination.exists():
        raise ValueError("conversion requires a new output directory")
    if source_identity(source) != expected:
        raise ValueError("source checkpoint digest mismatch")
    config = json.loads((source / "config.json").read_text())
    profile, tensors = layout(source, config, precision, profiles)
    header, raw, data_bytes = output_layout(tensors)
    destination.parent.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(destination.parent).free < data_bytes + (1 << 30):
        raise ValueError("insufficient space for the converted checkpoint")
    destination.mkdir()
    try:
        mx.set_default_device(mx.cpu)
        mx.set_cache_limit(64 << 20)
        target = destination / SINGLE
        with target.open("xb") as output:
            output.write(struct.pack("<Q", len(raw)))
            output.write(raw)
            output.truncate(8 + len(raw) + data_bytes)
            for tensor in tensors:
                write_tensor(output, tensor, header, 8 + len(raw))
                print(tensor["name"], flush=True)
        config["quantization"] = quantization(tensors, precision)
        config["dtype"] = "float16"
        if "text_config" in config:
            config["text_config"]["dtype"] = "float16"
        (destination / "config.json").write_text(json.dumps(config, indent=2) + "\n")
        for name in COPIED:
            shutil.copyfile(source / name, destination / name)
        manifest = {"schema": "paintedwolf-mlx-conversion/2", "profile": profile, "source_sha256": expected,
                    "recipe": "affine-group64-f16", "precision": precision, "source_tile_bytes": TILE_BYTES,
                    "weights_sha256": digest(target), "weights_bytes": target.stat().st_size,
                    "chat_template_sha256": digest(destination / "chat_template.jinja")}
        (destination / "conversion.json").write_text(json.dumps(manifest, indent=2) + "\n")
    except BaseException:
        shutil.rmtree(destination)
        raise
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--source-sha256", required=True, help="source_identity of the checkpoint")
    parser.add_argument("--precision", type=Path)
    args = parser.parse_args()
    precision = json.loads(args.precision.read_text()) if args.precision else None
    convert(args.source, args.output, args.source_sha256, precision)
