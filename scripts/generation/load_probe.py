"""Load a signed student bundle in the native runtime on this device and generate from it.

Writes a `pw-load-probe/1` receipt bound to the bundle manifest, the export it packages,
this checkout's commit and the device the runtime reported. The probe renders one served
conversation fixture with the bundle's own renderer, which must produce the tokens
training rendered, then generates greedily and holds the result to the export's
behaviour reference. A bundle the runtime cannot load yields a failed receipt that says
at which step; a bundle that is not the named export yields no receipt.

`native_tiers.json` states which student tiers each backend of the native runtime
loads. A probe of any other tier fails as `native_runtime_unsupported` without
starting the runtime, and the factory reads the same file when it plans a campaign.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

from native_bench import Client

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/bialy/custom_model"))
from behavior import compare, served_cases  # noqa: E402

SCHEMA = "pw-load-probe/1"
MODELS = {"e4b": "gemma4-e4b-paintedwolf", "12b": "gemma4-12b-paintedwolf", "26b-a4b": "gemma4-26b-paintedwolf"}
# The precision each backend's payload was exported at, as the behaviour tolerance names it.
PRECISION = {"vulkan": "Q4_K_M", "mlx": "affine-group64-f16"}
TIERS = Path(__file__).with_name("native_tiers.json")
PROMPT = "later calls replace earlier ones"
PREFILL_CHUNK = 256


class Failed(Exception):
    def __init__(self, code, detail):
        self.code, self.detail = code, str(detail)[-2000:]
        super().__init__(code)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def commit(root, run=subprocess.run):
    head = run(["git", "-C", str(root), "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout.strip()
    if run(["git", "-C", str(root), "status", "--porcelain"], capture_output=True, text=True, check=True).stdout:
        raise ValueError("load probe requires a clean checkout")
    return head


def render(renderer, bundle, case, run=subprocess.run):
    with tempfile.TemporaryDirectory() as directory:
        request = Path(directory) / "request.json"
        request.write_text(json.dumps({"messages": case["messages"], "tools": case["tools"]}))
        result = run([str(renderer), str(bundle), str(request)], capture_output=True, text=True)
    if result.returncode:
        raise Failed("render_failed", result.stderr)
    return json.loads(result.stdout)["prompt_tokens"]


def generate(client, prompt, new_tokens):
    """Load the bundle and decode greedily; returns the runtime's identity, tokens and stop reason."""
    try:
        hello = client.call("hello")
        context = len(prompt) + new_tokens
        plan = client.call("inspect", context_tokens=context, prefill_chunk=PREFILL_CHUNK)["plan"]
        client.call("load", context_tokens=context, prefill_chunk=PREFILL_CHUNK,
                    device_budget_bytes=plan["device_reservation_bytes"], host_budget_bytes=plan["host_reservation_bytes"])
    except (RuntimeError, TimeoutError, ValueError, OSError) as exc:
        raise Failed("load_failed", exc) from exc
    try:
        client.call("begin", request={"request_id": 1, "prompt_tokens": prompt, "output_contract": {"allowed_tools": []},
                                      "max_new_tokens": new_tokens, "temperature": 0, "top_p": 1, "top_k": 0, "seed": 0,
                                      "checkpoint_tokens": 0})
        tokens = []
        while True:
            progress = client.call("advance", request_id=1)["progress"]
            if progress["token"] is not None:
                tokens.append(progress["token"])
            if progress["stop_reason"] is not None:
                break
        client.call("unload")
        client.call("shutdown")
    except (RuntimeError, TimeoutError, ValueError, OSError) as exc:
        raise Failed("generation_failed", exc) from exc
    return hello, tokens, progress["stop_reason"]


def probe(args, run=subprocess.run, popen=subprocess.Popen):
    model = MODELS[args.student]
    verified = run([str(args.verifier), "-bundle", str(args.bundle), "-authority", str(args.authority),
                    "-model", model, "-device", args.device], capture_output=True, text=True)
    if verified.returncode:
        raise ValueError("bundle does not verify against the release authority: " + verified.stderr[-500:])
    signed = json.loads(verified.stdout)["manifest"]
    export = json.loads(args.export_receipt.read_text())
    output = export["outputs"][args.device]
    if export["student"] != args.student or signed["artifact"]["digest"] != output["sha256"]:
        raise ValueError("bundle does not package this export")
    if sha256(args.reference) != export["behavior_sha256"]:
        raise ValueError("behaviour reference is not the export's")
    expected = json.loads(args.reference.read_text())
    wanted = next(prompt for prompt in expected["prompts"] if prompt["name"] == PROMPT)
    receipt = {"schema": SCHEMA, "student": args.student, "bundle": args.device, "model": model,
               "revision": signed["revision"], "manifest_sha256": sha256(args.bundle / "manifest.json"),
               "artifact_sha256": output["sha256"], "export_receipt_sha256": sha256(args.export_receipt),
               "behavior_sha256": export["behavior_sha256"], "host_commit": commit(ROOT, run),
               "binaries": {"runner": sha256(args.runner), "renderer": sha256(args.renderer)},
               "prompt": PROMPT, "device": None, "generated_tokens": [], "stop_reason": None, "behavior": None}
    try:
        if args.student not in json.loads(TIERS.read_text())[args.device]:
            raise Failed("native_runtime_unsupported", "the %s runtime does not load the %s tier" % (args.device, args.student))
        case = next(case for case in served_cases() if case["name"] == PROMPT)
        prompt = render(args.renderer, args.bundle, case, run)
        if prompt != wanted["prompt_tokens"]:
            raise Failed("rendering_mismatch", "the bundle's renderer and training tokenize the fixture differently")
        command = [str(args.runner), "--model", str(args.bundle), "--artifact-id", output["sha256"]]
        command += ["--metallib", str(args.metallib)] if args.metallib else []
        command += ["--device-pci", args.device_pci] if args.device_pci else []
        process = popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=0)
        try:
            hello, tokens, stop = generate(Client(process), prompt, len(wanted["tokens"]))
        finally:
            if process.poll() is None:
                process.kill()
            process.wait()
            process.stdin.close()
            process.stdout.close()
        receipt.update(device=hello, generated_tokens=tokens, stop_reason=stop)
        if hello.get("backend") != args.device or not tokens:
            raise Failed("generation_failed", "the runtime reported another backend or generated nothing")
        try:
            receipt["behavior"] = compare({"prompts": [wanted]}, [{"prompt_tokens": prompt, "tokens": tokens}],
                                          PRECISION[args.device])
        except ValueError as exc:
            raise Failed("behavior_mismatch", exc) from exc
        receipt.update(passed=True, failure=None)
    except Failed as failed:
        receipt.update(passed=False, failure={"code": failed.code, "detail": failed.detail})
    return receipt


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("bundle", "runner", "renderer", "verifier", "authority", "export-receipt", "reference", "out"):
        p.add_argument("--" + name, type=Path, required=True)
    p.add_argument("--student", choices=sorted(MODELS), required=True)
    p.add_argument("--device", choices=sorted(PRECISION), required=True)
    p.add_argument("--metallib", type=Path)
    p.add_argument("--device-pci")
    return p


def main(argv=None, run=subprocess.run, popen=subprocess.Popen):
    arguments = parser().parse_args(argv)
    result = probe(arguments, run, popen)
    arguments.out.write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
    return 0 if result["passed"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
