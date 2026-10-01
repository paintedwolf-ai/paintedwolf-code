#!/usr/bin/env python3
"""Compare trainer and native-engine predictions on identical turn inputs.

The head must record the encoding settings it trained with, and they must be the
ones the engine serves at. Exit 0 when they match and the engine agrees within
--tolerance, 1 on a mismatch or a larger gap, 2 when the head records no settings
or nothing was compared (no --engine, or no complete turns).

Usage: parity_probe.py --model ID --head FILE --examples FILE [--engine LAUNCHER] [--n 6]
                       [--corpus <decide dir>/corpus.json] [--tolerance 0.001]
"""
import argparse
import json
import os
import subprocess
import sys

os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

import torch  # noqa: E402
import laya  # noqa: E402
from safetensors.torch import load_file  # noqa: E402

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import headfile  # noqa: E402
import rows as rowfile  # noqa: E402
from corpus import Corpus, decide_dir  # noqa: E402
from train import forward_head, multi_item, precompute, state_text  # noqa: E402


def torch_probs(agent, model, corpus, ex, device):
    context = int(agent.cfg.get("max_len", 512))
    head_max_len = min(int(corpus.state_spec.get("head_tokens", 512)), context - 64)
    tools_q = corpus.multi_question("tool", ex["offered"]["loadable"])
    item = multi_item(agent.tok, state_text(ex["state"]), tools_q, {k: 0 for k in tools_q["options"]}, context, head_max_len, "tools")
    names = list(tools_q["options"].keys())[: len(item["markers"])]
    f = precompute(agent, [item], device)[0]
    with torch.no_grad():
        logits = forward_head(model, f["h"][None].to(device), f["att"][None].to(device), f["marker_pos"][None].to(device),
                              f["marker_mask"][None].to(device), torch.tensor([item["qtype"]], device=device))[0].cpu()
    return dict(zip(names, torch.sigmoid(logits[: len(names)]).tolist()))


def engine_probs(proc, corpus, ex):
    req = {"method": "decide", "head": "turn-load", "state": ex["state"], "questions": corpus.row_questions(ex)}
    proc.stdin.write(json.dumps(req) + "\n")
    proc.stdin.flush()
    return json.loads(proc.stdout.readline())["answers"]["tools"]["probabilities"]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--model", required=True)
    ap.add_argument("--head", required=True)
    ap.add_argument("--examples", required=True)
    ap.add_argument("--engine", default=os.environ.get("LYCAON_DECIDE_ENGINE"))
    ap.add_argument("--corpus", default=str(decide_dir() / "corpus.json"))
    ap.add_argument("--n", type=int, default=6)
    ap.add_argument("--tolerance", type=float, default=1e-3, help="the largest engine-trainer probability gap that passes")
    args = ap.parse_args()
    device = os.environ.get("LYCAON_DECIDE_DEVICE") or ("mps" if torch.backends.mps.is_available() else "cpu")
    agent = laya.load(args.model, device=device)
    model = agent.model
    tensors = load_file(args.head, device=device)
    for module in ("head", "scorer", "type_emb"):
        state = {k[len(module) + 1:]: v for k, v in tensors.items() if k.startswith(module + ".")}
        if state:
            getattr(model, module).load_state_dict(state)
    model.eval()
    # The probe encodes at the checkpoint's context, as the engine does; a head trained at
    # another length would agree here and still have learned from different inputs.
    meta = headfile.read_metadata(args.head)[0]
    context = int(agent.cfg.get("max_len", 512))
    budget = min(int(Corpus.load(args.corpus).state_spec.get("head_tokens", 512)), context - 64)
    if "max_len" not in meta or "head_max_len" not in meta:
        print("parity: unverified; the head records no encoding settings")
        return 2
    if (int(meta["max_len"]), int(meta["head_max_len"])) != (context, budget):
        print("parity: the head was trained at max_len %s, head budget %s; the engine encodes at %d, %d" % (
            meta["max_len"], meta["head_max_len"], context, budget))
        return 1
    corpus = Corpus.load(args.corpus)
    proc = None
    if args.engine:
        proc = subprocess.Popen([args.engine], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)
        proc.stdin.write(json.dumps({"method": "hello"}) + "\n")
        proc.stdin.flush()
        proc.stdout.readline()
    worst = 0.0
    examples = [ex for ex in rowfile.load(args.examples) if not ex["partial"]][: args.n]
    for ex in examples:
        pt = torch_probs(agent, model, corpus, ex, device)
        top = sorted(pt.items(), key=lambda kv: -kv[1])[:5]
        truth = sorted(rowfile.truth_tools(ex))
        print("truth", truth)
        print("   trainer top", [(k, round(v, 3)) for k, v in top], "| truth p", [(t, round(pt.get(t, -1), 3)) for t in truth])
        if proc is not None:
            pe = engine_probs(proc, corpus, ex)
            gap = max(abs(pe.get(k, 0.0) - v) for k, v in pt.items())
            worst = max(worst, gap)
            print("   engine  top", [(k, round(pe.get(k, 0.0), 3)) for k, _ in top], "| max |engine - trainer| %.4f" % gap)
    if proc is not None:
        proc.stdin.close()
        proc.wait()
        print("parity: worst gap %.4f over %d turns" % (worst, len(examples)))
        if worst > args.tolerance:
            return 1
    if proc is None or not examples:
        print("parity: unverified; no engine answers were compared")
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
