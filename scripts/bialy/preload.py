#!/usr/bin/env python3
"""The option vocabulary a release serves its turn-load head.

A turn-load head answers the tool option texts of the corpus it was trained on,
encoded within that corpus's budgets. The host asks its questions from the
`preload` block of decision-release.json, so the block is derived here, in one
place, from the training corpus and the checkpoint's context:

    preload.py --corpus corpus.json --model CHECKPOINT_DIR > preload.json

The factory freezes the printed block into its selector policy, and
adopt_heads.py writes it into the release exactly as frozen.
"""

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from corpus import Corpus  # noqa: E402

ENCODINGS = ("joint", "independent")
BUDGETS = ("option_words", "max_len", "head_tokens")
KEYS = ("catalog_revision", "encoding", *BUDGETS, "options")


def context(model_dir):
    """The checkpoint's sequence length, the one the trainer and the engine encode at."""
    with open(Path(model_dir) / "rl_agent_config.json", encoding="utf-8") as fh:
        return int(json.load(fh).get("max_len", 512))


def derive(corpus, max_len):
    """The preload block for heads trained on `corpus` over a checkpoint of `max_len` tokens."""
    return {"catalog_revision": corpus.revision, "encoding": "independent" if corpus.independent() else "joint",
            "option_words": int(corpus.spec["tools"]["option_words"]), "max_len": int(max_len),
            "head_tokens": int(corpus.state_spec["head_tokens"]), "options": dict(sorted(corpus.tool_options.items()))}


def faults(block):
    """Why a preload block cannot be served; empty when it can."""
    if not isinstance(block, dict) or set(block) != set(KEYS):
        return ["the preload block must hold exactly %s" % ", ".join(KEYS)]
    out = []
    if not isinstance(block["catalog_revision"], str) or not block["catalog_revision"]:
        out.append("catalog_revision must name the training corpus")
    if block["encoding"] not in ENCODINGS:
        out.append("encoding must be one of %s" % ", ".join(ENCODINGS))
    out += ["%s must be a positive integer" % key for key in BUDGETS
            if isinstance(block[key], bool) or not isinstance(block[key], int) or block[key] <= 0]
    options = block["options"]
    if not isinstance(options, dict) or not options or any(not isinstance(k, str) or not isinstance(v, str) for k, v in options.items()):
        out.append("options must map every tool name to its option text")
    return out


def head_faults(block, metadata):
    """Where a turn-load head's recorded training differs from the block it would be served under."""
    recorded = {"catalog_revision": metadata.get("corpus"), "encoding": metadata.get("tool_encoding", "joint"),
                "option_words": metadata.get("tool_option_words"), "max_len": metadata.get("max_len"),
                "head_tokens": metadata.get("head_max_len")}
    return ["the turn-load head was trained with %s %s, not %s" % (key, value, block[key])
            for key, value in recorded.items() if str(value) != str(block[key])]


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--corpus", required=True, type=Path, help="the corpus the heads were trained on")
    ap.add_argument("--model", required=True, type=Path, help="the installed checkpoint directory")
    args = ap.parse_args(argv)
    block = derive(Corpus.load(args.corpus), context(args.model))
    problems = faults(block)
    if problems:
        ap.exit(1, "preload: %s\n" % "; ".join(problems))
    print(json.dumps(block, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
