#!/usr/bin/env python3
"""Adopt a qualified selector policy into the host.

Writes the policy's three head pins into decision-release.json and its thresholds
into decisions.yaml, exactly as the policy froze them. Nothing is calibrated here.
The factory's adoption check reads the same two files and refuses any difference.

    adopt_heads.py --policy p0.json --heads DIR --release NAME --heads-revision SHA \
        --dataset-tag TAG --dataset-revision SHA --factory-tag TAG --factory-commit SHA
"""

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import headfile  # noqa: E402

HEADS = ("turn-load", "unit-rank", "code-rank")
HOST = Path(__file__).resolve().parents[2] / "lycaon/config/packs/painted-wolf/platform/host"
COMMIT = re.compile(r"[0-9a-f]{40}")


class AdoptionError(ValueError):
    pass


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def head_pins(policy, heads_dir):
    """Each policy head's digest and label, read from its head file and checked against the policy."""
    if set(policy["heads"]) != set(HEADS):
        raise AdoptionError("the policy must pin exactly %s" % ", ".join(HEADS))
    pins = {}
    for head in HEADS:
        path = Path(heads_dir) / (head + ".safetensors")
        if not path.is_file() or sha256(path) != policy["heads"][head]["sha256"]:
            raise AdoptionError("%s does not match the policy's digest" % path)
        pins[head] = {"sha256": policy["heads"][head]["sha256"], "label": headfile.read_metadata(path)[0]["label"]}
    return pins


def release(current, pins, args):
    if set(current["heads"]) != set(HEADS):
        raise AdoptionError("decision-release.json pins heads other than %s" % ", ".join(HEADS))
    for name in ("heads_revision", "dataset_revision", "factory_commit"):
        if not COMMIT.fullmatch(getattr(args, name)):
            raise AdoptionError("--%s must be a full revision" % name.replace("_", "-"))
    out = json.loads(json.dumps(current))
    out["release"] = args.release
    out["source"]["heads"].update(tag=args.release, revision=args.heads_revision)
    out["source"]["dataset"].update(tag=args.dataset_tag, revision=args.dataset_revision)
    out["source"]["factory"].update(tag=args.factory_tag, commit=args.factory_commit)
    out["heads"] = pins
    return out


KEY = re.compile(r"^(?P<indent> *)(?P<key>[A-Za-z_][\w-]*):(?P<rest>.*)$")


def set_thresholds(text, values):
    """decisions.yaml with each dotted threshold set to its value, comments and order kept."""
    lines, stack, found = text.split("\n"), [], set()
    for index, line in enumerate(lines):
        match = KEY.match(line)
        if not match:
            continue
        depth = len(match["indent"]) // 2
        stack = stack[:depth] + [match["key"]]
        name = ".".join(stack)
        if name in values:
            comment = re.search(r"\s+#.*$", match["rest"])
            lines[index] = "%s%s: %s%s" % (match["indent"], match["key"], json.dumps(values[name]), comment.group(0) if comment else "")
            found.add(name)
    missing = sorted(set(values) - found)
    if missing:
        raise AdoptionError("decisions.yaml has no %s" % ", ".join(missing))
    return "\n".join(lines)


def adopt(policy, heads_dir, args, host=None):
    host = Path(host or HOST)
    decisions_path, release_path = host / "decisions.yaml", host / "decision-release.json"
    decisions = decisions_path.read_text(encoding="utf-8")
    version = re.search(r"^version: (\d+)$", decisions, re.M)
    if not version or int(version.group(1)) != policy["decision_contract"]:
        raise AdoptionError("the policy was frozen against another decision contract")
    pins = head_pins(policy, heads_dir)
    updated = release(json.loads(release_path.read_text(encoding="utf-8")), pins, args)
    decisions_path.write_text(set_thresholds(decisions, policy["thresholds"]), encoding="utf-8")
    release_path.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")
    return {"release": args.release, "heads": pins, "thresholds": policy["thresholds"]}


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--policy", required=True, type=Path)
    ap.add_argument("--heads", required=True, type=Path)
    for name in ("release", "heads-revision", "dataset-tag", "dataset-revision", "factory-tag", "factory-commit"):
        ap.add_argument("--" + name, required=True)
    args = ap.parse_args(argv)
    try:
        result = adopt(json.loads(args.policy.read_text()), args.heads, args)
    except AdoptionError as exc:
        ap.exit(1, "adopt_heads: %s\n" % exc)
    print(json.dumps(result, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
