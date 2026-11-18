#!/usr/bin/env python3
"""Adopt a qualified selector policy into the host.

Writes the policy's three head pins and the option vocabulary its turn-load head
was trained with into decision-release.json, and its thresholds into
decisions.yaml, exactly as the policy froze them. Nothing is calibrated here.
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
import preload  # noqa: E402

HEADS = ("turn-load", "unit-rank", "code-rank")
HOST = Path(__file__).resolve().parents[2] / "lycaon/config/packs/painted-wolf/platform/host"
COMMIT = re.compile(r"[0-9a-f]{40}")


class AdoptionError(ValueError):
    pass


def sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def head_pins(policy, heads_dir):
    """Each policy head's digest and label, read from its head file and checked against the
    policy; the turn-load head must have been trained under the policy's vocabulary."""
    if set(policy["heads"]) != set(HEADS):
        raise AdoptionError("the policy must pin exactly %s" % ", ".join(HEADS))
    pins = {}
    for head in HEADS:
        path = Path(heads_dir) / (head + ".safetensors")
        if not path.is_file() or sha256(path) != policy["heads"][head]["sha256"]:
            raise AdoptionError("%s does not match the policy's digest" % path)
        metadata = headfile.read_metadata(path)[0]
        if head == "turn-load" and preload.head_faults(policy["preload"], metadata):
            raise AdoptionError("; ".join(preload.head_faults(policy["preload"], metadata)))
        pins[head] = {"sha256": policy["heads"][head]["sha256"], "label": metadata["label"]}
    return pins


def vocabulary(policy, decisions):
    """The policy's preload block, refused unless this decisions.yaml encodes turn questions
    the way the block says the head was trained."""
    block = policy.get("preload")
    problems = preload.faults(block)
    if problems:
        raise AdoptionError("; ".join(problems))
    encoded = scalars(decisions)
    serving = {"encoding": "independent" if encoded.get("turn.tools.independent") else "joint",
               "option_words": encoded.get("turn.tools.option_words"), "head_tokens": encoded.get("state.head_tokens")}
    differing = sorted(key for key, value in serving.items() if value != block[key])
    if differing:
        raise AdoptionError("decisions.yaml encodes %s differently from the policy's vocabulary" % ", ".join(differing))
    return block


def release(current, pins, block, policy, args):
    if set(current["heads"]) != set(HEADS):
        raise AdoptionError("decision-release.json pins heads other than %s" % ", ".join(HEADS))
    backbone = {key: (policy.get("backbone") or {}).get(key) for key in ("model", "revision")}
    if backbone != current["backbone"]:
        raise AdoptionError("the policy was trained over another backbone than the release pins")
    for name in ("heads_revision", "dataset_revision", "factory_commit"):
        if not COMMIT.fullmatch(getattr(args, name)):
            raise AdoptionError("--%s must be a full revision" % name.replace("_", "-"))
    out = json.loads(json.dumps(current))
    out["release"] = args.release
    out["source"]["heads"].update(tag=args.release, revision=args.heads_revision)
    out["source"]["dataset"].update(tag=args.dataset_tag, revision=args.dataset_revision)
    out["source"]["factory"].update(tag=args.factory_tag, commit=args.factory_commit)
    out["heads"] = pins
    out["preload"] = block
    return out


KEY = re.compile(r"^(?P<indent> *)(?P<key>[A-Za-z_][\w-]*):(?P<rest>.*)$")


def keys(lines):
    """(line index, dotted name, match) for every key line of a decisions.yaml."""
    stack = []
    for index, line in enumerate(lines):
        match = KEY.match(line)
        if match:
            stack = stack[:len(match["indent"]) // 2] + [match["key"]]
            yield index, ".".join(stack), match


def scalars(text):
    """Every dotted key of decisions.yaml whose value is a number or a boolean."""
    out = {}
    for _, name, match in keys(text.split("\n")):
        try:
            out[name] = json.loads(re.sub(r"\s+#.*$", "", match["rest"]).strip())
        except ValueError:
            continue
    return out


def set_thresholds(text, values):
    """decisions.yaml with each dotted threshold set to its value, comments and order kept."""
    lines, found = text.split("\n"), set()
    for index, name, match in keys(lines):
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
    block = vocabulary(policy, decisions)
    pins = head_pins(policy, heads_dir)
    updated = release(json.loads(release_path.read_text(encoding="utf-8")), pins, block, policy, args)
    decisions_path.write_text(set_thresholds(decisions, policy["thresholds"]), encoding="utf-8")
    release_path.write_text(json.dumps(updated, indent=2) + "\n", encoding="utf-8")
    return {"release": args.release, "heads": pins, "thresholds": policy["thresholds"],
            "preload": {key: len(value) if key == "options" else value for key, value in block.items()}}


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
