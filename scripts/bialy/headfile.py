#!/usr/bin/env python3
"""Head files: the weights one training run changes over the frozen backbone.

A head file is a safetensors file holding the head, scorer, type embedding,
and (optionally) action head state of a Laya `DecisionModel`, with header
metadata naming the backbone it was trained over. `bialy` refuses a head
whose `backbone` is not the loaded checkpoint's encoder.

    headfile.py show head.safetensors
"""

import argparse
import json
import os
import struct
import sys
from pathlib import Path

HEAD_MODULES = ("head", "scorer", "type_emb", "act_head")
FORMAT = "pw-decide-head/1"


def encoder_id(model_id):
    """The backbone id a checkpoint records, without loading its weights."""
    import laya  # noqa: PLC0415

    model_dir = model_id
    if not Path(model_dir).exists():
        from huggingface_hub import snapshot_download  # noqa: PLC0415

        model_dir = snapshot_download(model_id, allow_patterns=["rl_agent_config.json"])
    with open(Path(model_dir) / "rl_agent_config.json", encoding="utf-8") as fh:
        return json.load(fh)["encoder"]


def save(path, model, label, model_id, extra=None):
    """Write the head modules of `model` as a head file."""
    from safetensors.torch import save_file  # noqa: PLC0415

    tensors = {}
    for name in HEAD_MODULES:
        module = getattr(model, name, None)
        if module is None:
            continue
        for key, value in module.state_dict().items():
            tensors["%s.%s" % (name, key)] = value.detach().contiguous().cpu()
    meta = {"format": FORMAT, "label": label, "model": model_id, "backbone": encoder_id(model_id)}
    for key, value in (extra or {}).items():
        meta[key] = value if isinstance(value, str) else json.dumps(value)
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    # Written beside the head and renamed over it, so a reader never sees half a head.
    partial = Path(str(path) + ".partial")
    save_file(tensors, str(partial), metadata=meta)
    os.replace(partial, path)


def claim(path):
    """Hold the head's lock for the life of the process, so two runs can never write the
    same head; a second run for a path in use exits instead of overwriting it."""
    import fcntl  # noqa: PLC0415

    Path(path).parent.mkdir(parents=True, exist_ok=True)
    # Opened without truncating, so a refused run leaves the owner's PID readable.
    fh = open(str(path) + ".lock", "a+")
    try:
        fcntl.flock(fh, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        fh.seek(0)
        raise SystemExit("%s is being written by another run (pid %s)" % (path, fh.read().strip() or "unknown")) from None
    fh.seek(0)
    fh.truncate()
    fh.write("%d\n" % os.getpid())
    fh.flush()
    return fh


def read_metadata(path):
    with open(path, "rb") as fh:
        n = struct.unpack("<Q", fh.read(8))[0]
        header = json.loads(fh.read(n))
    meta = header.pop("__metadata__", {})
    return meta, header


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("show")
    s.add_argument("path")
    args = ap.parse_args()
    meta, tensors = read_metadata(args.path)
    print(json.dumps(meta, indent=2))
    print("%d tensors, %d bytes" % (len(tensors), sum(t["data_offsets"][1] - t["data_offsets"][0] for t in tensors.values())))


if __name__ == "__main__":
    main()
