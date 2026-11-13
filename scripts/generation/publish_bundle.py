"""Upload a signed student model bundle to the downloads bucket, once.

The bundle is verified with `model-bundle` against the release authority first, so only
a manifest the authority signed, whose payloads match it byte for byte, is uploaded.
Every payload goes under models/<model>/<device>/<revision>/ through the immutable put,
and the signed manifest.json goes last, so a reader never finds a manifest whose payloads
are missing. The immutable put accepts identical bytes and refuses different ones, so a
published revision is never replaced and a rerun completes an interrupted upload.
"""

import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
MODELS = ("gemma4-e4b-paintedwolf", "gemma4-12b-paintedwolf", "gemma4-26b-paintedwolf")
DEVICES = ("mlx", "vulkan")
CONTENT_TYPES = {".json": "application/json", ".jinja": "text/plain; charset=utf-8"}


def verified(model_bundle, bundle, authority, model, device, run=subprocess.run):
    """The signed manifest model-bundle verified for this model and device."""
    result = run([str(model_bundle), "-bundle", str(bundle), "-authority", str(authority), "-model", model, "-device", device],
                 check=True, capture_output=True, text=True)
    return json.loads(result.stdout)


def publish(model_bundle, bundle, authority, model, device, put=ROOT / "scripts/release-r2-immutable-put.sh", run=subprocess.run):
    """Upload a verified bundle; returns the object keys in upload order."""
    if model not in MODELS or device not in DEVICES:
        raise ValueError("a student bundle is one of %s on %s" % (", ".join(MODELS), " or ".join(DEVICES)))
    bundle = Path(bundle)
    signed = verified(model_bundle, bundle, authority, model, device, run)
    manifest = signed["manifest"]
    if manifest["model"] != model or manifest["device"] != device:
        raise ValueError("the verified bundle is for another model or device")
    prefix = "models/%s/%s/%s" % (model, device, manifest["revision"])
    names = [manifest["artifact"]["name"], *(payload["name"] for payload in manifest.get("files") or [])]
    keys = []
    for name in [*names, "manifest.json"]:
        key = "%s/%s" % (prefix, name)
        content_type = CONTENT_TYPES.get(Path(name).suffix, "application/octet-stream")
        run(["bash", str(put), "--key", key, "--file", str(bundle / name), "--content-type", content_type], check=True)
        keys.append(key)
    return keys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model-bundle", required=True, type=Path, help="the model-bundle binary")
    parser.add_argument("--bundle", required=True, type=Path, help="a signed bundle directory")
    parser.add_argument("--authority", required=True, type=Path, help="the release-key registry")
    parser.add_argument("--model", required=True, choices=MODELS)
    parser.add_argument("--device", required=True, choices=DEVICES)
    args = parser.parse_args()
    for key in publish(args.model_bundle, args.bundle, args.authority, args.model, args.device):
        print(key)


if __name__ == "__main__":
    main()
