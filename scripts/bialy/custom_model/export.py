"""Recoverably merge a verified local adapter; never touch stock blobs."""
import argparse
import gc
import json
import os
from pathlib import Path
import uuid

from checkpoint import atomic_json, exclusive_run, sync_directory
from common import digest, inventory


def export(model_path, run, out, resume=False):
    with exclusive_run(out, resume):
        return export_owned(model_path, run, out)


def export_owned(model_path, run, out):
    import torch
    from transformers import AutoModelForCausalLM, AutoTokenizer
    from peft import PeftModel
    from safetensors.torch import save_file, load_file

    inputs = json.loads((run / "inputs.json").read_text())
    result = json.loads((run / "result.json").read_text())
    if result["inputs_sha256"] != digest(inputs) or inventory(model_path) != inputs["model"] or inventory(run / "adapter") != result["adapter"]:
        raise ValueError("training artifacts changed")
    identity = {"training_sha256":digest(result), "model":inputs["model"], "adapter":result["adapter"]}
    if (out / "inputs.json").exists():
        if json.loads((out / "inputs.json").read_text()) != identity:
            raise ValueError("merge resume inputs changed")
    else:
        atomic_json(out / "inputs.json", identity)
    if (out / "manifest.json").exists():
        manifest = json.loads((out / "manifest.json").read_text())
        if manifest["files"] != inventory(out / "model"):
            raise ValueError("completed merge changed")
        return manifest
    stage_file = out / "merged.json"
    if stage_file.exists():
        stage = json.loads(stage_file.read_text())
        observed = inventory(out / stage["directory"])
        if (out / "model").exists():
            observed.update({"model/"+key:value for key,value in inventory(out / "model").items()})
        if stage["files"] != observed:
            raise ValueError("saved merge stage changed")
    else:
        base = AutoModelForCausalLM.from_pretrained(model_path, local_files_only=True, trust_remote_code=False, dtype=torch.float32)
        model = PeftModel.from_pretrained(base, run / "adapter", local_files_only=True).eval()
        tokenizer = AutoTokenizer.from_pretrained(run / "adapter", local_files_only=True, trust_remote_code=False)
        ids = tokenizer("Merge parity fixture.", return_tensors="pt").input_ids
        with torch.no_grad():
            before = model(input_ids=ids).logits
            merged = model.merge_and_unload(safe_merge=True).eval()
            after = merged(input_ids=ids).logits
        torch.testing.assert_close(before, after, rtol=1e-4, atol=1e-4)
        staged = out / (".merged-" + uuid.uuid4().hex)
        merged.save_pretrained(staged / "model", safe_serialization=True)
        tokenizer.save_pretrained(staged / "model")
        save_file({"ids":ids.contiguous(), "logits":after.contiguous()}, staged / "parity.safetensors")
        for path in staged.rglob("*"):
            if path.is_file():
                with path.open("rb") as stream:
                    os.fsync(stream.fileno())
        sync_directory(staged / "model")
        sync_directory(staged)
        stage = {"directory":staged.name, "files":inventory(staged)}
        atomic_json(stage_file, stage)
        del model, base, merged
        gc.collect()
    staged = out / stage["directory"]
    # Publishing the model directory is separate from the recoverable parity probe.
    location = out / "model" if (out / "model").exists() else staged / "model"
    probe = load_file(staged / "parity.safetensors")
    reloaded = AutoModelForCausalLM.from_pretrained(location, local_files_only=True, trust_remote_code=False).eval()
    with torch.no_grad():
        torch.testing.assert_close(probe["logits"], reloaded(input_ids=probe["ids"]).logits, rtol=1e-4, atol=1e-4)
    if inventory(model_path) != inputs["model"] or inventory(run / "adapter") != result["adapter"]:
        raise ValueError("source changed during merge")
    # Copy-free publication retains the staged directory as the recovery source.
    # Keep a separate complete-model receipt before rename to recover its boundary.
    atomic_json(out / "verified.json", {"files":inventory(location)})
    if location != out / "model":
        os.rename(location, out / "model")
        sync_directory(out)
    manifest = {"schema":"pw-custom-merge/1", "training_sha256":digest(result),
                "files":inventory(out / "model"), "merge_parity":"passed", "load_parity":"passed"}
    atomic_json(out / "manifest.json", manifest)
    return manifest


if __name__ == "__main__":
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("model", "run", "out"):
        p.add_argument("--"+name, required=True, type=Path)
    p.add_argument("--resume", action="store_true")
    args = p.parse_args()
    export(args.model, args.run, args.out, args.resume)
