#!/usr/bin/env python3
"""Fine-tune the Bialy head on reranking candidates.

Reads the candidate dumps lycaon/cmd/decide-rerank writes (one row per pair,
every candidate the site offered with its lexical score and target flag) and
trains the head, scorer, and type embedding of a Laya checkpoint on the
engine's own relevance question: the exact rubric bialy serves, read from
scripts/bialy/rank-rubric.json so training and serving never drift.

Labels come from the site's own structure, on the rubric's 0..4 scale:

  4  the target unit
  2  another unit in the target's file
  1  a lexical neighbour: top-ranked by the site, another file
  0  a random candidate from the rest

The encoder stays frozen and runs per batch every epoch, so memory is bounded
by the batch size rather than the corpus; run one training job per host. The
head file records the backbone it was trained over, which the engine checks
before loading it (see scripts/bialy/headfile.py).

Usage:
  train_rerank.py --dump rows.jsonl [--dump more.jsonl ...] --out .task/decide/heads/code-rank.safetensors
                  [--model convaiinnovations/laya-multilingual] [--epochs 6] [--val-fraction 0.1]
"""

import argparse
import json
import os
import random
import sys
import time
from pathlib import Path

import torch
import torch.nn.functional as F
from torch.utils.data import DataLoader, Dataset

import laya
from laya.common import QTYPES, build_sequence, collate_items

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import headfile  # noqa: E402

# The rubric the engine serves (scripts/bialy/rank-rubric.json, compiled into bialy).
with open(Path(__file__).resolve().parents[1] / "rank-rubric.json", encoding="utf-8") as _fh:
    _RUBRIC_FILE = json.load(_fh)
RUBRIC = {"instructions": _RUBRIC_FILE["instructions"], "criteria": list(_RUBRIC_FILE["levels"])}
NEIGHBOURS_PER_ROW = 2
RANDOMS_PER_ROW = 2


def read_jsonl(path):
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                yield json.loads(line)


def state_text(task, candidate):
    """Exactly what the daemon encodes for one rank candidate."""
    return "Task: %s\n\nCandidate:\n%s" % (task, candidate)


def labelled_examples(rows, rng):
    """(task, text, level) triples from one dump, with the site's own labels."""
    out = []
    for row in rows:
        cands = row["candidates"]
        target = next((c for c in cands if c.get("target")), None)
        if target is None or len(cands) < 2:
            continue
        task = row["task"]
        if all("label" in c for c in cands):
            # Synthetic site rows carry explicit rubric levels.
            picked = [c for c in cands if c["label"] > 0]
            zeros = [c for c in cands if c["label"] == 0]
            rng.shuffle(zeros)
            for c in picked + zeros[:RANDOMS_PER_ROW]:
                out.append((task, c["text"], int(c["label"])))
            continue
        out.append((task, target["text"], 4))
        same_file = [c for c in cands if not c.get("target") and c.get("file") == row["file"]]
        for c in same_file[:1]:
            out.append((task, c["text"], 2))
        others = [c for c in cands if not c.get("target") and c.get("file") != row["file"]]
        by_lexical = sorted(others, key=lambda c: -c["lexical"])
        for c in by_lexical[:NEIGHBOURS_PER_ROW]:
            out.append((task, c["text"], 1))
        rest = by_lexical[NEIGHBOURS_PER_ROW:]
        rng.shuffle(rest)
        for c in rest[:RANDOMS_PER_ROW]:
            out.append((task, c["text"], 0))
    return out


class Tokenized(Dataset):
    """Token ids only. Encoder states are computed per batch and discarded, so
    memory is bounded by the batch, not the corpus."""

    def __init__(self, items):
        self.items = items

    def __len__(self):
        return len(self.items)

    def __getitem__(self, i):
        return self.items[i]


def tokenize(tok, examples, max_len):
    q = {"t": "score", "ins": RUBRIC["instructions"], "crit": list(RUBRIC["criteria"])}
    items = []
    for task, text, level in examples:
        ids, markers = build_sequence(tok, state_text(task, text), q, max_len=max_len)
        items.append({"ids": ids, "markers": markers, "qtype": QTYPES["score"], "label": level})
    return items


def collate_with(pad_id):
    def collate(batch):
        b = collate_items([[it] for it in batch], pad_id)
        b["label"] = torch.tensor([it["label"] for it in batch])
        return b

    return collate


def encode(model, b, device):
    """Frozen encoder forward for one batch; nothing is kept afterwards."""
    with torch.no_grad():
        return model.encoder(input_ids=b["input_ids"].to(device), attention_mask=b["attention_mask"].to(device)).last_hidden_state


def forward_head(model, h, att, mpos, mmask, qtype):
    d = h.size(-1)
    h = h + model.type_emb(qtype)[:, None, :]
    if model.head is not None:
        pad = ~att.bool()
        for layer in model.head.layers:
            h = layer(h, src_key_padding_mask=pad)
    m = torch.gather(h, 1, mpos.clamp(min=0)[:, :, None].expand(-1, -1, d))
    logits = model.scorer(m).squeeze(-1).float()
    return logits.masked_fill(~mmask, -1e4)


def evaluate(model, loader, device):
    model.eval()
    loss_sum, n, correct, mae = 0.0, 0, 0, 0.0
    with torch.no_grad():
        for b in loader:
            h = encode(model, b, device)
            logits = forward_head(model, h, b["attention_mask"].to(device), b["marker_pos"].to(device), b["marker_mask"].to(device), b["qtype"].to(device))
            labels = b["label"].to(device)
            loss_sum += F.cross_entropy(logits, labels).item() * len(labels)
            n += len(labels)
            correct += (logits.argmax(-1) == labels).sum().item()
            expected = (F.softmax(logits, -1) * torch.arange(logits.size(-1), device=device)).sum(-1)
            mae += (expected - labels.float()).abs().sum().item()
    return loss_sum / max(n, 1), correct / max(n, 1), mae / max(n, 1)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dump", action="append", default=[], help="candidate dump JSONL; repeatable")
    ap.add_argument("--out", required=True)
    ap.add_argument("--model", default=os.environ.get("LYCAON_DECIDE_MODEL_ID", "convaiinnovations/laya-multilingual"))
    ap.add_argument("--label", default="code-rank")
    ap.add_argument("--epochs", type=int, default=6)
    ap.add_argument("--batch-size", type=int, default=32)
    ap.add_argument("--lr", type=float, default=3e-4)
    ap.add_argument("--val-fraction", type=float, default=0.1)
    ap.add_argument("--max-rows", type=int, default=0)
    ap.add_argument("--seed", type=int, default=7)
    args = ap.parse_args()

    if not args.dump:
        ap.error("give at least one --dump")
    out = Path(args.out)
    lock = headfile.claim(out)  # noqa: F841 - held until the process exits, before any expensive work
    rng = random.Random(args.seed)
    torch.manual_seed(args.seed)
    rows = []
    for path in args.dump:
        rows.extend(read_jsonl(path))
    rng.shuffle(rows)
    if args.max_rows:
        rows = rows[: args.max_rows]
    split = int(len(rows) * (1 - args.val_fraction))
    train_ex = labelled_examples(rows[:split], rng)
    val_ex = labelled_examples(rows[split:], rng)
    print(f"rows {len(rows)}: {len(train_ex)} train examples, {len(val_ex)} validation examples", file=sys.stderr)

    device = "mps" if torch.backends.mps.is_available() else ("cuda" if torch.cuda.is_available() else "cpu")
    agent = laya.load(args.model, device=device)
    # The engine encodes at the checkpoint's full context; training at another length
    # teaches the head on candidates cut where the engine never cuts them.
    max_len = int(agent.cfg.get("max_len", 512))
    model = agent.model
    model.eval()
    collate = collate_with(agent.tok.pad_token_id)
    train_items = tokenize(agent.tok, train_ex, max_len)
    val_items = tokenize(agent.tok, val_ex, max_len)
    train_loader = DataLoader(Tokenized(train_items), batch_size=args.batch_size, shuffle=True, collate_fn=collate)
    val_loader = DataLoader(Tokenized(val_items), batch_size=args.batch_size, shuffle=False, collate_fn=collate)

    params = list(model.head.parameters()) + list(model.scorer.parameters()) + list(model.type_emb.parameters())
    opt = torch.optim.AdamW(params, lr=args.lr, weight_decay=0.01)
    loss, acc, mae = evaluate(model, val_loader, device)
    print(f"base: val loss {loss:.4f} acc {acc:.3f} mae {mae:.3f}", file=sys.stderr)
    best = float("inf")
    out.parent.mkdir(parents=True, exist_ok=True)
    for epoch in range(1, args.epochs + 1):
        model.train()
        model.encoder.eval()
        t0 = time.time()
        for b in train_loader:
            opt.zero_grad()
            h = encode(model, b, device)
            logits = forward_head(model, h, b["attention_mask"].to(device), b["marker_pos"].to(device), b["marker_mask"].to(device), b["qtype"].to(device))
            F.cross_entropy(logits, b["label"].to(device)).backward()
            opt.step()
        loss, acc, mae = evaluate(model, val_loader, device)
        print(f"epoch {epoch}: val loss {loss:.4f} acc {acc:.3f} mae {mae:.3f} ({time.time() - t0:.0f}s)", file=sys.stderr)
        if loss < best:
            best = loss
            headfile.save(out, model, args.label, args.model, {
                "max_len": max_len, "val_loss": loss, "val_acc": acc, "val_mae": mae, "train_examples": len(train_ex),
            })
            print(f"  saved {out}", file=sys.stderr)


if __name__ == "__main__":
    main()
