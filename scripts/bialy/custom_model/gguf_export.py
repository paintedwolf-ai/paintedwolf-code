"""Convert a merged checkpoint with an explicitly pinned local llama.cpp build."""
import argparse
import hashlib
import json
import os
import socket
import time
import uuid
from pathlib import Path
import re
import subprocess
import sys

import serving_check
from behavior import compare
from common import file_digest, inventory
from checkpoint import atomic_json, exclusive_run
from rendering import template_sha256


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


class Server:
    """One llama.cpp server on a loopback port, alive for the checks of one artifact."""

    def __init__(self, binary, model, log, timeout=1800):
        self.binary, self.model, self.log, self.timeout = binary, model, log, timeout

    def __enter__(self):
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            port = probe.getsockname()[1]
        self.stream = self.log.open("a")
        self.process = subprocess.Popen(
            [str(self.binary), "--model", str(self.model), "--host", "127.0.0.1", "--port", str(port), "--ctx-size", "4096",
             "--parallel", "1", "--jinja", "--reasoning", "off", "--no-context-shift", "--no-warmup"],
            stdout=self.stream, stderr=subprocess.STDOUT)
        url, deadline = "http://127.0.0.1:%d" % port, time.monotonic() + self.timeout
        while True:
            if self.process.poll() is not None or time.monotonic() > deadline:
                self.__exit__(None, None, None)
                raise ValueError("llama.cpp server did not load the artifact; see " + self.log.name)
            try:
                if serving_check.call(url, "/health").get("status") == "ok":
                    return url
            except (ValueError, OSError):
                pass
            time.sleep(0.2)

    def __exit__(self, *_):
        self.process.terminate()
        self.process.wait()
        self.stream.close()


def observe(url, expected, call=serving_check.call):
    """The artifact's tokenization and greedy continuation of every reference prompt."""
    observed = []
    for prompt in expected["prompts"]:
        tokens = call(url, "/tokenize", {"content": prompt["prompt"], "add_special": False, "parse_special": True})["tokens"]
        result = call(url, "/completion", {"prompt": prompt["prompt_tokens"], "n_predict": len(prompt["tokens"]),
                                           "temperature": 0, "seed": 0, "cache_prompt": False, "return_tokens": True})
        observed.append({"prompt_tokens": tokens, "tokens": result["tokens"]})
    return observed


def qualify(server, artifact, precision, template, expected, log, revision):
    """Serve one artifact and check what it renders and, given a reference, what it generates."""
    with Server(server, artifact, log) as url:
        rendering = serving_check.check(url)
        if rendering["template_sha256"] != template:
            raise ValueError("artifact does not serve the template its rows were rendered with")
        if not serving_check.built_from(rendering["build"], revision):
            raise ValueError("llama.cpp server was not built from the converter revision")
        return {"rendering": rendering,
                "behavior": compare(expected, observe(url, expected), precision) if expected else None}


def convert(args):
    with exclusive_run(args.out, args.resume):
        return convert_owned(args)


def convert_owned(args):
    manifest = json.loads((args.merge / "manifest.json").read_text())
    if inventory(args.merge / "model") != manifest["files"]:
        raise ValueError("merged checkpoint changed")
    checkout_identity(args.llama_cpp, args.revision)
    binaries = {str(p):file_digest(p) for p in (args.quantizer, args.server)}
    expected = None
    if manifest.get("behavior_sha256"):
        if file_digest(args.merge / "behavior.json") != manifest["behavior_sha256"]:
            raise ValueError("behaviour reference changed")
        expected = json.loads((args.merge / "behavior.json").read_text())
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
    template = manifest["template_sha256"]
    if template_sha256(after["metadata"]["tokenizer.chat_template"]) != template:
        raise ValueError("GGUF does not embed the template its rows were rendered with")
    served = {name: qualify(args.server, path, precision, template, expected, args.out / "server.log", args.revision)
              for name, path, precision in (("unquantized", raw, "F16"), ("quantized", quantized, args.quantization))}
    checkout_identity(args.llama_cpp, args.revision)
    if binaries != {str(p):file_digest(p) for p in (args.quantizer, args.server)} or inventory(args.merge / "model") != manifest["files"]:
        raise ValueError("export inputs changed")
    atomic_json(args.out / "manifest.json", {"schema":"pw-custom-gguf/2", "llama_cpp_revision":args.revision,
               "binaries":binaries,"merged_manifest_sha256":file_digest(args.merge / "manifest.json"),
               "template_sha256":template,"behavior_sha256":manifest.get("behavior_sha256"),
               "unquantized":before,"quantized":after,"served":served})


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("merge","llama-cpp","quantizer","server","out"):
        p.add_argument("--"+name, type=Path, required=True)
    p.add_argument("--resume", action="store_true")
    p.add_argument("--revision", required=True)
    p.add_argument("--quantization", choices=("Q4_K_M","Q5_K_M","Q8_0"), default="Q4_K_M")
    convert(p.parse_args())
