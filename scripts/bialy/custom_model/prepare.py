"""Validate and encode complete splits before allocating training resources."""
import argparse
import importlib.metadata
import json
from pathlib import Path

from checkpoint import exclusive_run
from admission import derive
from common import digest, file_digest, inventory, write_new
from data import EncodingExclusion, assert_disjoint, encode, load_split


def prepare(paths, out, tokenizer, max_length, model_identity, checkpoint_matches=None):
    if max_length < 2 or set(paths) != {"train", "validation", "holdout"}:
        raise ValueError("preparation requires three splits and a valid token limit")
    with exclusive_run(out, False):
        inputs = {name: file_digest(path) for name, path in paths.items()}
        rows = {name: load_split(path, "system_test" if name == "holdout" else name) for name, path in paths.items()}
        for first, second in (("train", "validation"), ("train", "holdout"), ("validation", "holdout")):
            assert_disjoint(rows[first], rows[second])
        report = {"schema": "pw-training-preparation/1", "inputs": inputs, "model": model_identity,
                  "max_length": max_length, "splits": {}, "excluded": []}
        for name, values in rows.items():
            lengths = []
            destination = Path(out) / (name + ".jsonl")
            with destination.open("x", encoding="utf-8") as stream:
                for row in values:
                    keys = ("target",) if row["schema"] == "pw-custom-sft/1" else ("chosen", "rejected")
                    try:
                        tokens = max(len(encode(tokenizer, row, row[key], max_length)["input_ids"]) for key in keys)
                    except EncodingExclusion as exc:
                        report["excluded"].append({"split": name, "request_sha256": row["request_capture"]["sha256"],
                                                   "reason": exc.reason, **exc.details})
                        continue
                    stream.write(json.dumps(row, ensure_ascii=False) + "\n")
                    lengths.append(tokens)
            report["splits"][name] = {"input_rows": len(values), "kept_rows": len(lengths),
                                      "maximum_tokens": max(lengths, default=0), "sha256": file_digest(destination)}
        if inputs != {name: file_digest(path) for name, path in paths.items()}:
            raise ValueError("dataset changed during preparation")
        if checkpoint_matches is not None and not checkpoint_matches():
            raise ValueError("checkpoint changed during preparation")
        report["ready"] = all(value["kept_rows"] for value in report["splits"].values())
        derive([Path(out) / (name + ".jsonl") for name in paths], paths.values(), Path(out) / "source.json")
        write_new(Path(out) / "manifest.json", report)
        return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("model", "train", "validation", "holdout", "out"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--max-length", type=int, required=True)
    args = parser.parse_args()
    from transformers import AutoTokenizer
    source = inventory(args.model)
    tokenizer = AutoTokenizer.from_pretrained(args.model, local_files_only=True, trust_remote_code=False)
    identity = {"files": source, "template_sha256": digest(tokenizer.chat_template),
                "transformers": importlib.metadata.version("transformers")}
    report = prepare({name: getattr(args, name) for name in ("train", "validation", "holdout")},
                     args.out, tokenizer, args.max_length, identity, lambda: inventory(args.model) == source)
    print(json.dumps(report, indent=2))
    raise SystemExit(0 if report["ready"] else 2)
