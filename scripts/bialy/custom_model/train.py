"""Offline SFT/LoRA or sequence-DPO over materialized local checkpoints."""
import argparse
import importlib.metadata
import json
from pathlib import Path
import random
import time
import uuid
import os

from common import digest, file_digest, inventory
from data import assert_disjoint, encode, load_split
from checkpoint import atomic_json, ReferenceCache, sync_directory, exclusive_run
from training_loop import run_loop


def train(args):
    with exclusive_run(args.out, args.resume):
        return train_owned(args)


def train_owned(args):
    started = time.monotonic()
    import torch
    from transformers import AutoModelForCausalLM, AutoTokenizer
    from peft import LoraConfig, get_peft_model
    from objective import dpo, scores

    if args.checkpoint_every < 1 or args.max_updates < 0 or args.max_seconds < 0 or args.keep_checkpoints < 2:
        raise ValueError("invalid recovery or budget settings")
    if args.epochs < 1 or args.learning_rate <= 0 or args.max_length < 2 or args.rank < 1:
        raise ValueError("invalid training parameters")
    if args.mode == "dpo" and args.reference is None:
        raise ValueError("DPO requires the frozen, merged SFT reference checkpoint")
    if args.mode == "sft" and args.reference is not None:
        raise ValueError("SFT does not use a reference")
    source = inventory(args.model)
    reference_source = inventory(args.reference) if args.reference else None
    # Start DPO at the SFT reference; a separate base is an explicit future ablation.
    if reference_source is not None and source != reference_source:
        raise ValueError("DPO policy must start from the frozen SFT reference")
    train_rows, validation_rows = load_split(args.train), load_split(args.validation)
    holdout_rows = load_split(args.holdout)
    assert_disjoint(train_rows, validation_rows)
    assert_disjoint(train_rows, holdout_rows)
    assert_disjoint(validation_rows, holdout_rows)
    wanted = "pw-custom-" + args.mode + "/1"
    if any(r["schema"] != wanted for r in train_rows + validation_rows):
        raise ValueError("split schema does not match objective")
    random.seed(args.seed)
    torch.manual_seed(args.seed)
    tokenizer = AutoTokenizer.from_pretrained(args.model, local_files_only=True, trust_remote_code=False)
    if not tokenizer.chat_template:
        raise ValueError("checkpoint has no pinned chat template")
    def prepare(rows):
        return [[encode(tokenizer, row, row[k], args.max_length) for k in
                 (("target",) if args.mode == "sft" else ("chosen", "rejected"))] for row in rows]
    training, validation = prepare(train_rows), prepare(validation_rows)
    config = {k:v for k,v in vars(args).items() if k not in {"resume","max_seconds","max_updates","checkpoint_every","keep_checkpoints"}}
    config = {k:str(v) if isinstance(v, Path) else v for k,v in config.items()}
    manifest = {"schema":"pw-custom-training/1", "config":config, "model":source,
                "reference":reference_source, "template_sha256":digest(tokenizer.chat_template),
                "train_sha256":file_digest(args.train), "validation_sha256":file_digest(args.validation),
                "holdout_sha256":file_digest(args.holdout),
                "packages":{k:importlib.metadata.version(k) for k in ("torch", "transformers", "peft", "safetensors")},
                "implementation":{p.name:file_digest(p) for p in sorted(Path(__file__).parent.glob("*.py"))}}
    if args.resume and (args.out / "inputs.json").exists():
        if json.loads((args.out / "inputs.json").read_text()) != manifest:
            raise ValueError("resume inputs changed; model, data, tokenizer, code, and optimizer settings must match")
        if (args.out / "result.json").exists():
            completed = json.loads((args.out / "result.json").read_text())
            if completed["inputs_sha256"] != digest(manifest) or completed["adapter"] != inventory(args.out / "adapter"):
                raise ValueError("completed run artifacts changed")
            return completed
    else:
        if (args.out / "checkpoints").exists() or (args.out / "adapter").exists():
            raise ValueError("run artifacts have no input manifest")
        atomic_json(args.out / "inputs.json", manifest)
    dtype = torch.float32 if args.device == "cpu" else torch.bfloat16
    reference = None
    if args.reference:
        reference = AutoModelForCausalLM.from_pretrained(args.reference, local_files_only=True, trust_remote_code=False, dtype=dtype).to(args.device)
        reference.eval()
        reference.requires_grad_(False)
    cache = {}
    if reference is not None:
        durable = ReferenceCache(args.out / "reference.sqlite", digest(manifest))
        samples = [sample for pair in training + validation for sample in pair]
        reference_started = time.monotonic()
        computed = 0
        try:
            with torch.no_grad():
                for index, sample in enumerate(samples):
                    key = digest({"sample":sample,"reference":reference_source,"template":manifest["template_sha256"]})
                    value = durable.get(key)
                    if value is None:
                        if args.max_seconds and time.monotonic()-started >= args.max_seconds:
                            progress = {"status":"paused_reference","completed_samples":index,"total_samples":len(samples)}
                            atomic_json(args.out / "progress.json", progress)
                            return progress
                        value = float(scores(reference, sample, args.device)[0])
                        durable.put(key, value)
                        computed += 1
                    cache[key] = value
                    elapsed = time.monotonic()-reference_started
                    atomic_json(args.out / "progress.json", {"status":"reference","completed_samples":index+1,
                         "total_samples":len(samples),"computed_samples":computed,"elapsed_seconds":elapsed,
                         "estimated_reference_seconds_remaining":elapsed/computed*(len(samples)-index-1) if computed else None})
            probe = samples[0]
            key = digest({"sample":probe,"reference":reference_source,"template":manifest["template_sha256"]})
            with torch.no_grad():
                if abs(cache[key]-float(scores(reference, probe, args.device)[0])) > 1e-5:
                    raise ValueError("reference cache verification failed")
        finally:
            durable.close()
        del reference
        if args.device.startswith("cuda"):
            torch.cuda.empty_cache()
    model = AutoModelForCausalLM.from_pretrained(args.model, local_files_only=True, trust_remote_code=False, dtype=dtype).to(args.device)
    # A regex (re:...) matches full module paths and confines LoRA to the language
    # model of a multimodal checkpoint; a list matches module names anywhere.
    targets = args.targets[3:] if args.targets.startswith("re:") else [t for t in args.targets.split(",") if t]
    if not targets:
        raise ValueError("no LoRA target modules")
    model = get_peft_model(model, LoraConfig(r=args.rank, lora_alpha=2*args.rank, lora_dropout=0.0,
                                           target_modules=targets, task_type="CAUSAL_LM"))
    model.config.use_cache = False
    model.gradient_checkpointing_enable(gradient_checkpointing_kwargs={"use_reentrant":False})
    model.enable_input_require_grads()
    optimizer = torch.optim.AdamW([p for p in model.parameters() if p.requires_grad], lr=args.learning_rate)
    def loss(pair):
        chosen, nll = scores(model, pair[0], args.device)
        if args.mode == "sft":
            return nll
        rejected, _ = scores(model, pair[1], args.device)
        ref = [torch.tensor(cache[digest({"sample":s,"reference":reference_source,"template":manifest["template_sha256"]})], device=args.device) for s in pair]
        return dpo(chosen, rejected, *ref, nll, args.beta, args.anchor)
    progress = run_loop(model, optimizer, training, validation, loss, args, manifest, started)
    if progress["status"] != "complete":
        return progress
    if source != inventory(args.model) or reference_source != (inventory(args.reference) if args.reference else None):
        raise ValueError("checkpoint changed during training")
    if manifest["train_sha256"] != file_digest(args.train) or manifest["validation_sha256"] != file_digest(args.validation) or manifest["holdout_sha256"] != file_digest(args.holdout):
        raise ValueError("dataset changed during training")
    staged = args.out / (".adapter-" + uuid.uuid4().hex)
    model.save_pretrained(staged, safe_serialization=True)
    tokenizer.save_pretrained(staged)
    for path in staged.rglob("*"):
        if path.is_file():
            with path.open("rb") as stream:
                os.fsync(stream.fileno())
    sync_directory(staged)
    if (args.out / "adapter").exists():
        os.rename(args.out / "adapter", args.out / (".interrupted-adapter-" + uuid.uuid4().hex))
    os.rename(staged, args.out / "adapter")
    sync_directory(args.out)
    result = {"inputs_sha256":digest(manifest),"history":progress["history"],
              "adapter":inventory(args.out / "adapter"),"resources":progress}
    atomic_json(args.out / "result.json", result)
    return result


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("model", "train", "validation", "holdout", "out"):
        p.add_argument("--"+name, type=Path, required=True)
    p.add_argument("--reference", type=Path)
    p.add_argument("--mode", choices=("sft", "dpo"), required=True)
    p.add_argument("--device", default="cpu")
    p.add_argument("--targets", required=True, help="Comma-separated LoRA module names, or re:<regex> over full module paths")
    p.add_argument("--rank", type=int, default=16)
    p.add_argument("--max-length", type=int, default=4096)
    p.add_argument("--epochs", type=int, default=1)
    p.add_argument("--seed", type=int, default=7)
    p.add_argument("--learning-rate", type=float, default=2e-5)
    p.add_argument("--beta", type=float, default=0.1)
    p.add_argument("--anchor", type=float, default=0.0)
    p.add_argument("--resume", action="store_true")
    p.add_argument("--max-seconds", type=float, default=0, help="Pause between operations after this wall-time budget; zero disables")
    p.add_argument("--max-updates", type=int, default=0, help="Bounded pilot updates per invocation; resume continues")
    p.add_argument("--checkpoint-every", type=int, default=10)
    p.add_argument("--keep-checkpoints", type=int, default=3)
    return p


if __name__ == "__main__":
    print(json.dumps(train(parser().parse_args()), indent=2))
