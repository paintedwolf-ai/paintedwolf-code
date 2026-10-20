"""Convert a merged checkpoint with an explicitly pinned local llama.cpp build."""
import argparse
import hashlib
import json
import os
import uuid
from pathlib import Path
import re
import subprocess
import sys

from common import file_digest, inventory
from checkpoint import atomic_json, exclusive_run


def checkout_identity(root, revision):
    if not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise ValueError("llama.cpp revision must be a full commit")
    head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
    dirty = subprocess.check_output(["git", "-C", str(root), "status", "--porcelain", "--untracked-files=normal"], text=True)
    if head != revision or dirty:
        raise ValueError("llama.cpp must be clean at the requested revision")
    return head


def inspect_gguf(path):
    import gguf
    reader = gguf.GGUFReader(path)
    tensors, ranges = {}, []
    for tensor in reader.tensors:
        start, size = int(tensor.data_offset), int(tensor.n_bytes)
        if tensor.name in tensors or start < reader.data_offset or start + size > path.stat().st_size:
            raise ValueError("invalid GGUF tensor range or duplicate name")
        ranges.append((start, start + size))
        tensors[tensor.name] = {"shape":tensor.shape.tolist(), "type":str(tensor.tensor_type),
                                "offset":start, "bytes":size, "sha256":hashlib.sha256(tensor.data).hexdigest()}
    ranges.sort()
    if not tensors or any(a[1] > b[0] for a,b in zip(ranges,ranges[1:])):
        raise ValueError("empty or overlapping tensor inventory")
    fields = {k:v.contents() for k,v in reader.fields.items() if k.startswith("tokenizer.") or k == "general.architecture"}
    if not fields.get("tokenizer.chat_template"):
        raise ValueError("GGUF lost chat template")
    return {"sha256":file_digest(path),"tensors":tensors,"metadata":fields}


def convert(args):
    with exclusive_run(args.out, args.resume):
        return convert_owned(args)


def convert_owned(args):
    manifest = json.loads((args.merge / "manifest.json").read_text())
    if inventory(args.merge / "model") != manifest["files"]:
        raise ValueError("merged checkpoint changed")
    checkout_identity(args.llama_cpp, args.revision)
    binaries = {str(p):file_digest(p) for p in (args.quantizer, args.loader)}
    inputs = {"merged_manifest_sha256":file_digest(args.merge / "manifest.json"),
              "revision":args.revision,"binaries":binaries,"quantization":args.quantization}
    if (args.out / "inputs.json").exists():
        if json.loads((args.out / "inputs.json").read_text()) != inputs:
            raise ValueError("export resume inputs changed")
    else:
        atomic_json(args.out / "inputs.json", inputs)
    raw, quantized = args.out / "model-f16.gguf", args.out / ("model-" + args.quantization + ".gguf")
    if (args.out / "manifest.json").exists():
        completed = json.loads((args.out / "manifest.json").read_text())
        if file_digest(raw) != completed["unquantized"]["sha256"] or file_digest(quantized) != completed["quantized"]["sha256"]:
            raise ValueError("completed immutable export changed; use a new output directory")
        return completed
    sys.path.insert(0, str(args.llama_cpp / "gguf-py"))
    def stage(path, receipt, command):
        receipt = args.out / receipt
        if receipt.exists():
            saved = json.loads(receipt.read_text())
            if path.exists() and file_digest(path) == saved["sha256"]:
                return saved
        temporary = path.with_name(".pending-" + uuid.uuid4().hex + ".gguf")
        subprocess.run(command(temporary), check=True)
        checked = inspect_gguf(temporary)
        with temporary.open("rb") as stream:
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        atomic_json(receipt, checked)
        return checked
    before = stage(raw, "conversion.json", lambda dest: [sys.executable,
        str(args.llama_cpp / "convert_hf_to_gguf.py"), str(args.merge / "model"), "--outfile", str(dest), "--outtype", "f16"])
    after = stage(quantized, "quantization.json", lambda dest: [str(args.quantizer), str(raw), str(dest), args.quantization])
    if before["metadata"] != after["metadata"] or {k:v["shape"] for k,v in before["tensors"].items()} != {k:v["shape"] for k,v in after["tensors"].items()}:
        raise ValueError("quantization changed tensor names, shapes, or tokenizer metadata")
    for name, tensor in before["tensors"].items():
        other = after["tensors"][name]
        if tensor["type"] == other["type"] and tensor["sha256"] != other["sha256"]:
            raise ValueError("quantizer changed an unquantized tensor")
    # Load-only probe: no completion is generated and no service is installed.
    with (args.out / ("load-" + uuid.uuid4().hex + ".log")).open("x") as log:
        subprocess.run([str(args.loader), "-m", str(quantized), "-n", "0", "-p", "", "--no-conversation"],
                       stdout=log, stderr=subprocess.STDOUT, check=True)
    checkout_identity(args.llama_cpp, args.revision)
    if binaries != {str(p):file_digest(p) for p in (args.quantizer, args.loader)} or inventory(args.merge / "model") != manifest["files"]:
        raise ValueError("export inputs changed")
    atomic_json(args.out / "manifest.json", {"schema":"pw-custom-gguf/1", "llama_cpp_revision":args.revision,
               "binaries":binaries,"merged_manifest_sha256":file_digest(args.merge / "manifest.json"),
               "unquantized":before,"quantized":after,"load_probe":"passed","behavior_qualified":False})


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("merge","llama-cpp","quantizer","loader","out"):
        p.add_argument("--"+name, type=Path, required=True)
    p.add_argument("--resume", action="store_true")
    p.add_argument("--revision", required=True)
    p.add_argument("--quantization", choices=("Q4_K_M","Q5_K_M","Q8_0"), default="Q4_K_M")
    convert(p.parse_args())
