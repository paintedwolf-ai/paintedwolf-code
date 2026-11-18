"""Stage a pinned, untrained checkpoint in the training conversation format.

The base arm of a paired evaluation is the stock weights served exactly as a trained
student is: same tokenizer files, same chat template. Weights are linked, never rewritten.
"""
import argparse
import json
import os
from pathlib import Path
import shutil

from checkpoint import atomic_json, exclusive_run
from common import inventory
from rendering import configure_training_renderer, template_sha256


def stage(model, out):
    from transformers import AutoTokenizer
    model, out = Path(model).resolve(strict=True), Path(out)
    with exclusive_run(out, False):
        source = inventory(model)
        tokenizer = AutoTokenizer.from_pretrained(model, local_files_only=True, trust_remote_code=False)
        profile = configure_training_renderer(tokenizer, model)
        for name in source:
            target = out / "model" / name
            target.parent.mkdir(parents=True, exist_ok=True)
            # Only weights are linked: the tokenizer files are rewritten below.
            if name.endswith(".safetensors"):
                try:
                    os.link(model / name, target)
                    continue
                except OSError:
                    pass
            shutil.copyfile(model / name, target)
        tokenizer.save_pretrained(out / "model")
        if inventory(model) != source:
            raise ValueError("checkpoint changed while it was staged")
        manifest = {"schema": "pw-base-checkpoint/2", "source": source, "files": inventory(out / "model"),
                    "rendering_profile": profile, "template_sha256": template_sha256(tokenizer.chat_template)}
        atomic_json(out / "manifest.json", manifest)
        return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(stage(args.model, args.out)))
