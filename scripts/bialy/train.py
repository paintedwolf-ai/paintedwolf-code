#!/usr/bin/env python3
"""Fine-tune a Bialy head on turn examples.

Reads training rows (rows.py, `lycaon-debug decide export`) and trains
the head, scorer, and type embedding of a Laya checkpoint on the same
questions the host asks, all as marker classification. `--families` picks
the head: the turn questions (tools, guides, kind) train `turn-load`, and
the rank pairs (skills, requests) train `unit-rank`, kept apart because they
otherwise outnumber the turn questions and pull the shared weights:

  tool.<name>   noul   label 1 when the turn needed that loadable tool, over
                       the loadable tools the row's turn offered
  guide.<id>    noul   label 1 when the turn needed that instruction unit, over
                       the units it offered (unknown labels are masked)
  kind          choice label = the observed turn kind
  skill         score  (request, skill card) pairs over the corpus's cards,
                       the text the engine ranks: level = the judged
                       relevance in labels.skill_scores, 4 for the skill the
                       coordinator read first; without judged scores, 4 for
                       the read skill and 0 for sampled others
  request       score  (request_tools need, tool card) pairs over the tools
                       the host ranks: 4 for the tools the turn used after the
                       need, else the judged score in the request's `scores`

Turn families train only on rows whose engine did not answer (--turn-rows
engine-off): a tool the engine preloaded and the session then called is a
label the engine produced. The rank families read every row's needs, which
under a live engine are the needs it missed. --tool-weight sqrt-inverse
weighs each tool's positives by sqrt(N / (n_t + 1)), clamped to [1, 20], so
rare tools are not drowned by the few every turn uses.

Units from packs named in --holdout-pack are left out of training so the
replay eval can measure generalization to unseen units. Encoder features are
precomputed once, so a few thousand examples train in minutes on a GPU. The
checkpoint records the backbone and a label the engine reports on its
handshake, so receipts name the head that answered.

Usage: train.py --corpus C --train FILE [--val FILE] [--holdout-pack ID ...]
                [--turn-rows engine-off|all] [--tool-weight none|sqrt-inverse]
                [--tool-truth consensus|judged|called] [--rank-levels blended|skills-blended|judged]
                [--out <decide dir>/heads/turn-load-<backbone>.safetensors]
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

sys.path.insert(0, os.path.dirname(__file__))
import headfile  # noqa: E402
import rows as rowfile  # noqa: E402
from corpus import Corpus, decide_dir  # noqa: E402

DEFAULT_MODEL = os.environ.get("LYCAON_DECIDE_MODEL_ID", "convaiinnovations/laya")

# Weight on positive options in the multi-label loss: a turn needs a few of its sixty-odd
# loadable tools, and a missed tool costs a round trip where an extra schema costs bytes.
POS_WEIGHT = 6.0

RANK_QUESTION = {"t": "score", "ins": "How relevant is this candidate to the task?",
                 "crit": ["irrelevant", "low", "moderate", "high", "direct match"]}


def state_text(state):
    return json.dumps(state, ensure_ascii=False, sort_keys=True)


def request_survival(tok, state, kept_tokens):
    """The share of the request's own tokens among the first `kept_tokens` tokens of the
    serialized state, by character offsets into its "user" value."""
    value = json.dumps(json.loads(state).get("user", ""), ensure_ascii=False)
    start = state.find('"user": ' + value)
    if start < 0 or value == '""':
        return 1.0
    start += len('"user": ')
    end = start + len(value)
    offsets = tok(state, add_special_tokens=False, return_offsets_mapping=True)["offset_mapping"]
    inside = [i for i, (a, b) in enumerate(offsets) if a < end and b > start]
    if not inside:
        return 1.0
    return sum(1 for i in inside if i < kept_tokens) / len(inside)


def multi_item(tok, state, question, truth, max_len, head_max_len, family, host="", pos=None):
    """One multi-label item: the question's options as markers, a 0/1 target per option in
    the sorted option order the host encodes. `truth` maps option -> 0/1/None; None options
    are masked out of the loss. `head_max_len` is the engine's option budget, so the options
    are cut exactly as the host's engine cuts them. `pos` maps options to their positive
    weight; options it leaves out take the run's --pos-weight."""
    q = {"t": "choice", "ins": question["instructions"], "crit": dict(question["options"])}
    ids, markers = build_sequence(tok, state, q, max_len=max_len, head_max_len=head_max_len)
    names = list(question["options"].keys())[: len(markers)]
    # [CLS] question [SEP] options [SEP] state [SEP]: the state starts after the separator
    # that closes the options.
    close = next((i for i in range(markers[-1] if markers else 0, len(ids)) if ids[i] == tok.sep_token_id), len(ids))
    state_ids = ids[close + 1:-1] if close < len(ids) else []
    kept = request_survival(tok, state, len(state_ids))
    target = [1.0 if truth.get(n) else 0.0 for n in names]
    weight = [0.0 if truth.get(n) is None else 1.0 for n in names]
    pos_weight = [POS_WEIGHT * (pos or {}).get(n, 1.0) for n in names]
    return {"ids": ids, "markers": markers, "qtype": QTYPES["choice"], "label": -1, "target": target, "weight": weight,
            "pos": pos_weight, "family": family, "host": host, "state_tokens": len(state_ids),
            "request_kept": kept}


FAMILIES = ("tools", "guides", "kind", "skills", "requests")
RANK_FAMILIES = {"skills", "requests"}
# The families whose answers change what a turn carries; their validation loss picks
# the checkpoint. Kind only reports, and the rank families train their own head.
SELECT = ("tools", "guides")


def skill_levels(row, cards, rng, scored, zeros, observed=True):
    """(skill, level) pairs one turn trains a rank head on: with `observed`, the skill the
    coordinator read first at 4 whatever the judges scored it; then the `scored`
    best-judged skills and `zeros` judged zeros, or without judged scores a sample of
    other skills at 0."""
    read = [s for s in row["labels"].get("skills") or [] if s in cards][:1] if observed else []
    levels = {s: 4 for s in read}
    scores = {s: rowfile.level(p) for s, p in rowfile.skill_pairs(row).items() if s in cards and s not in levels}
    if scores:
        ranked = sorted(scores, key=lambda s: (-scores[s], rng.random()))
        for s in [s for s in ranked if scores[s] > 0][:scored]:
            levels[s] = scores[s]
        zero = [s for s in ranked if scores[s] == 0]
        for s in rng.sample(zero, min(zeros, len(zero))):
            levels[s] = 0
    elif read:
        others = [s for s in cards if s not in levels]
        for s in rng.sample(others, min(zeros, len(others))):
            levels[s] = 0
    return sorted(levels.items())


def request_levels(row, n, loadable, rng, scored, zeros, observed=True):
    """(tool, level) pairs one request_tools need trains a rank head on, over the tools the
    host would rank (loadable, minus the names the need spells out): with `observed`, the
    tools the turn used after the need at 4 whatever the judges scored them; then the
    best-judged tools and judged zeros, or without judged scores a sample of other tools
    at 0."""
    request = row["labels"]["requests"][n]
    exact = set(request.get("exact") or [])
    rest = [t for t in loadable if t not in exact]
    levels = {t: 4 for t in request.get("after") or [] if t in rest} if observed else {}
    scores = {t: rowfile.level(p) for t, p in rowfile.need_pairs(row, n).items() if t in rest and t not in levels}
    if scores:
        ranked = sorted(scores, key=lambda t: (-scores[t], rng.random()))
        for t in [t for t in ranked if scores[t] > 0][:scored]:
            levels[t] = scores[t]
        zero = [t for t in ranked if scores[t] == 0]
        for t in rng.sample(zero, min(zeros, len(zero))):
            levels[t] = 0
    elif levels:
        others = [t for t in rest if t not in levels]
        for t in rng.sample(others, min(zeros, len(others))):
            levels[t] = 0
    return sorted(levels.items())


def tool_weights(rows, mode, tool_truth):
    """Per-tool multipliers on the positive loss term, from the training rows' tool labels."""
    if mode == "none":
        return {}
    counts = {}
    for row in rows:
        for name in rowfile.truth_tools(row, tool_truth):
            counts[name] = counts.get(name, 0) + 1
    total = sum(counts.values())
    return {name: min(max((total / (n + 1)) ** 0.5, 1.0), 20.0) for name, n in counts.items()}


def independent_items(tok, state, question, truth, max_len, head_max_len, family, host, pos=None, keep=None, weight=None):
    """One item per option of a multi question, for a head that reads options on their own
    rows. Options without a label are skipped; `keep` names the negative options to keep
    and `weight` the loss weight that restores the sampled negatives' share."""
    items = []
    for name, text in question["options"].items():
        label = truth.get(name)
        if label is None or (not label and keep is not None and name not in keep):
            continue
        one = dict(question, options={name: text})
        item = multi_item(tok, state, one, {name: label}, max_len, head_max_len, family, host, pos)
        if not label and weight is not None:
            item["weight"] = [weight]
        items.append(item)
    return items


def build_items(examples, corpus, held_units, rng, tok, max_len, head_max_len, families, skill_scored, skill_zeros, turn_rows, pos, tool_truth, observed, tool_negatives=0):
    """One training item per (state, question) for the families trained. A joint head
    trains tools and guides as two multi-label items per turn and the kind as one choice;
    an independent head trains one item per tool or guide option, with `tool_negatives`
    sampled negative tools per turn (every guide option trains). Skills and needs are
    score pairs."""
    # Choice options in the engine's order: it keys them by name, sorted.
    kind_q = {"t": "choice", "ins": corpus.spec["kind"]["instructions"], "crit": dict(sorted(corpus.spec["kind"]["options"].items()))}
    kinds = list(kind_q["crit"])
    skill_cards = corpus.skill_cards()
    tool_cards = corpus.tool_cards()
    items = []
    for ex in examples:
        host = ex["host"]
        state = state_text(ex["state"])
        turn_ok = not ex["partial"] and (turn_rows == "all" or not rowfile.engine_answered(ex))
        families_here = tuple(f for f in families if f in RANK_FAMILIES or turn_ok)
        if ex["partial"]:
            families_here = tuple(f for f in families_here if f == "requests")
        targets = rowfile.tool_targets(ex, tool_truth)
        tools_q = corpus.multi_question("tool", ex["offered"]["loadable"])
        if "tools" in families_here and tools_q["options"]:
            truth = {n: targets.get(n) for n in tools_q["options"]}
            if tools_q.get("independent"):
                negatives = [n for n, v in truth.items() if v == 0]
                kept = set(rng.sample(negatives, min(tool_negatives, len(negatives)))) if tool_negatives else set(negatives)
                weight = len(negatives) / len(kept) if kept else None
                items.extend(independent_items(tok, state, tools_q, truth, max_len, head_max_len, "tools", host, pos, kept, weight))
            else:
                items.append(multi_item(tok, state, tools_q, truth, max_len, head_max_len, "tools", host, pos))
        guides = ex["labels"]["guides"]
        guides_q = corpus.multi_question("guide", ex["offered"]["guides"])
        if "guides" in families_here and guides_q["options"]:
            truth = {uid: (None if uid in held_units else guides.get(uid)) for uid in guides_q["options"]}
            if guides_q.get("independent"):
                items.extend(independent_items(tok, state, guides_q, truth, max_len, head_max_len, "guides", host))
            elif any(v is not None for v in truth.values()):
                items.append(multi_item(tok, state, guides_q, truth, max_len, head_max_len, "guides", host))
        kind = ex["labels"].get("kind")
        if "kind" in families_here and kind in kinds and not corpus.independent():
            ids, markers = build_sequence(tok, state, kind_q, max_len=max_len)
            items.append({"ids": ids, "markers": markers, "qtype": QTYPES["choice"], "label": kinds.index(kind), "family": "kind", "host": host})
        if "requests" in families_here:
            loadable = [t for t in ex["offered"]["loadable"] if t in tool_cards]
            for n, request in enumerate(ex["labels"]["requests"]):
                for name, level in request_levels(ex, n, loadable, rng, skill_scored, skill_zeros, observed["requests"]):
                    text = "Task: %s\n\nCandidate:\n%s" % (request["need"], tool_cards[name])
                    ids, markers = build_sequence(tok, text, RANK_QUESTION, max_len=max_len)
                    items.append({"ids": ids, "markers": markers, "qtype": QTYPES["score"], "label": level, "family": "requests", "host": host})
        if "skills" not in families_here or ex["partial"]:
            continue
        for name, level in skill_levels(ex, skill_cards, rng, skill_scored, skill_zeros, observed["skills"]):
            text = "Task: %s\n\nCandidate:\n%s" % (ex["state"]["user"], skill_cards[name])
            ids, markers = build_sequence(tok, text, RANK_QUESTION, max_len=max_len)
            items.append({"ids": ids, "markers": markers, "qtype": QTYPES["score"], "label": level, "family": "skills", "host": host})
    return items


def observed_levels(rule):
    """Which rank families let observed behaviour (a skill read, a tool used after a need)
    train at the top level over the judges' levels, under a --rank-levels rule."""
    return {"skills": rule in ("blended", "skills-blended"), "requests": rule == "blended"}


def state_room(items):
    """Per multi-label family, how much of the turn's state survives after the options: the
    median count of state tokens, the share of items keeping fewer than 16, and the median
    share of the request's own tokens that survive."""
    out = {}
    for family in sorted({it["family"] for it in items if it.get("target") is not None}):
        fam = [it for it in items if it.get("family") == family and it.get("target") is not None]
        room = sorted(it["state_tokens"] for it in fam)
        kept = sorted(it["request_kept"] for it in fam)
        out[family] = {"median": room[len(room) // 2], "under_16": round(sum(1 for r in room if r < 16) / len(room), 3),
                       "request_kept": round(kept[len(kept) // 2], 3)}
    return out


class Features(Dataset):
    def __init__(self, rows):
        self.rows = rows

    def __len__(self):
        return len(self.rows)

    def __getitem__(self, i):
        return self.rows[i]


def collate(batch):
    n = len(batch)
    L = max(b["h"].shape[0] for b in batch)
    d = batch[0]["h"].shape[1]
    k = max(b["marker_pos"].shape[0] for b in batch)
    h = torch.zeros((n, L, d))
    att = torch.zeros((n, L), dtype=torch.long)
    mpos = torch.zeros((n, k), dtype=torch.long)
    mmask = torch.zeros((n, k), dtype=torch.bool)
    target = torch.zeros((n, k))
    weight = torch.zeros((n, k))
    pos = torch.zeros((n, k))
    for i, b in enumerate(batch):
        h[i, : b["h"].shape[0]] = b["h"]
        att[i, : b["att"].shape[0]] = b["att"]
        mpos[i, : b["marker_pos"].shape[0]] = b["marker_pos"]
        mmask[i, : b["marker_mask"].shape[0]] = b["marker_mask"]
        if b.get("target") is not None:
            kk = len(b["target"])
            target[i, :kk] = torch.tensor(b["target"])
            weight[i, :kk] = torch.tensor(b["weight"])
            pos[i, :kk] = torch.tensor(b["pos"]) if b.get("pos") is not None else POS_WEIGHT
    return {"h": h, "attention_mask": att, "marker_pos": mpos, "marker_mask": mmask, "target": target, "weight": weight, "pos": pos,
            "qtype": torch.tensor([b["qtype"] for b in batch]), "label": torch.tensor([b["label"] for b in batch]),
            "family": [b.get("family", "") for b in batch], "host": [b.get("host", "") for b in batch]}


def precompute(agent, items, device, batch_size=32):
    model = agent.model.to(device).eval()
    pad = agent.tok.pad_token_id
    out = []
    t0 = time.time()
    with torch.no_grad():
        for start in range(0, len(items), batch_size):
            chunk = items[start : start + batch_size]
            batch = collate_items([[it] for it in chunk], pad)
            h = model.encoder(input_ids=batch["input_ids"].to(device), attention_mask=batch["attention_mask"].to(device)).last_hidden_state.cpu()
            att = batch["attention_mask"]
            for i, it in enumerate(chunk):
                seq = int(att[i].sum())
                kk = int(batch["marker_mask"][i].sum())
                out.append({"h": h[i, :seq], "att": att[i, :seq], "marker_pos": batch["marker_pos"][i, :kk],
                            "marker_mask": batch["marker_mask"][i, :kk], "qtype": it["qtype"], "label": it["label"],
                            "target": it.get("target"), "weight": it.get("weight"), "pos": it.get("pos"),
                            "family": it.get("family", ""), "host": it.get("host", "")})
    sys.stderr.write("precomputed %d items in %.1fs\n" % (len(out), time.time() - t0))
    return out


def forward_head(model, h, att, mpos, mmask, qtype):
    d = h.size(-1)
    h = h + model.type_emb(qtype)[:, None, :]
    if model.head is not None:
        pad = ~att.bool()
        for layer in model.head.layers:
            h = layer(h, src_key_padding_mask=pad)
    idx = mpos.clamp(min=0)[:, :, None].expand(-1, -1, d)
    logits = model.scorer(torch.gather(h, 1, idx)).squeeze(-1).float()
    return logits.masked_fill(~mmask, -1e4)


def row_losses(logits, b, device):
    """Per row: cross-entropy for rows with one answer; for multi rows the summed
    per-marker binary cross-entropy over known options, with the option count beside
    it so callers can average per option."""
    labels = b["label"].to(device)
    single = labels >= 0
    out = logits.new_zeros(len(labels))
    options = logits.new_zeros(len(labels))
    if single.any():
        out[single] = F.cross_entropy(logits[single], labels[single], reduction="none")
    multi = ~single
    if multi.any():
        target = b["target"].to(device)[multi]
        weight = b["weight"].to(device)[multi]
        pos_weight = b["pos"].to(device)[multi]
        per = F.binary_cross_entropy_with_logits(logits[multi], target, reduction="none", pos_weight=pos_weight) * weight
        out[multi] = per.sum(-1)
        options[multi] = weight.sum(-1)
    return out, options


def pooled_loss(rows, options):
    """The loss a batch of rows trains on: single-answer rows add their cross-entropy;
    multi rows add their mean per option, pooled over the batch, once per row. A tool
    row with sixty options therefore weighs its options, not its row, against a guide
    row with six."""
    multi = options > 0
    loss = rows[~multi].sum()
    if multi.any():
        loss = loss + rows[multi].sum() / options[multi].sum().clamp(min=1.0) * multi.sum()
    return loss


def batch_loss(logits, b, device):
    rows, options = row_losses(logits, b, device)
    return pooled_loss(rows, options)


def evaluate(model, loader, device, select):
    """Loss per family (per option for multi families, per row otherwise) and the pooled
    loss over the `select` families, which picks the checkpoint; accuracy per kind, where a multi
    row counts each known option as its own yes/no decision, with precision and recall
    of the positives per family (tools, guides) and the share of skill levels within one
    of the judged level."""
    model.eval()
    n = correct = 0
    per_type = {t: [0, 0] for t in QTYPES.values()}
    within_one = [0, 0]
    multi = {}
    family_loss = {}
    turn_rows, turn_options = [], []
    with torch.no_grad():
        for b in loader:
            logits = forward_head(model, b["h"].to(device), b["attention_mask"].to(device), b["marker_pos"].to(device),
                                  b["marker_mask"].to(device), b["qtype"].to(device))
            labels = b["label"].to(device)
            rows, options = row_losses(logits, b, device)
            for family, value, count in zip(b["family"], rows.tolist(), options.tolist()):
                acc = family_loss.setdefault(family, [0.0, 0.0])
                acc[0] += value
                acc[1] += count if count else 1
            turn = torch.tensor([f in select for f in b["family"]], device=rows.device)
            turn_rows.append(rows[turn])
            turn_options.append(options[turn])
            n += len(labels)
            single = labels >= 0
            if single.any():
                pred = logits[single].argmax(-1)
                hits = pred == labels[single]
                correct += hits.sum().item()
                for t, hit in zip(b["qtype"][single.cpu()].tolist(), hits.tolist()):
                    per_type[t][0] += hit
                    per_type[t][1] += 1
                near = (pred - labels[single]).abs() <= 1
                for t, ok in zip(b["qtype"][single.cpu()].tolist(), near.tolist()):
                    if t == QTYPES["score"]:
                        within_one[0] += ok
                        within_one[1] += 1
            for i in torch.nonzero(~single).flatten().tolist():
                pred = (logits[i] > 0).float()
                target = b["target"].to(device)[i]
                weight = b["weight"].to(device)[i] > 0
                for key in (b["family"][i], "%s@%s" % (b["family"][i], b["host"][i])):
                    m = multi.setdefault(key, {"tp": 0, "fp": 0, "fn": 0, "tn": 0})
                    m["tp"] += int(((pred == 1) & (target == 1) & weight).sum())
                    m["fp"] += int(((pred == 1) & (target == 0) & weight).sum())
                    m["fn"] += int(((pred == 0) & (target == 1) & weight).sum())
                    m["tn"] += int(((pred == 0) & (target == 0) & weight).sum())
                correct += int(((pred == target) & weight).sum() == weight.sum())
    names = {v: k for k, v in QTYPES.items()}
    by_type = {names[t]: round(c / max(m, 1), 3) for t, (c, m) in per_type.items() if m}
    if within_one[1]:
        by_type["score_within_one"] = round(within_one[0] / within_one[1], 3)
    by_type["loss"] = {family: round(total / max(count, 1), 4) for family, (total, count) in sorted(family_loss.items())}
    for family, m in multi.items():
        tp, fp, fn = m["tp"], m["fp"], m["fn"]
        if tp + fp + fn:
            by_type[family] = {"precision": round(tp / max(tp + fp, 1), 3), "recall": round(tp / max(tp + fn, 1), 3),
                               "positives": tp + fn, "options": tp + fp + fn + m["tn"]}
    rows = torch.cat(turn_rows)
    options = torch.cat(turn_options)
    return pooled_loss(rows, options).item() / max(len(rows), 1), correct / max(n, 1), by_type


def main():
    global POS_WEIGHT
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--train", required=True)
    ap.add_argument("--val", default="")
    ap.add_argument("--out", default="")
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--holdout-pack", action="append", default=[])
    ap.add_argument("--epochs", type=int, default=60)
    ap.add_argument("--patience", type=int, default=12, help="stop after this many epochs without a better selection loss")
    ap.add_argument("--batch-size", type=int, default=32)
    ap.add_argument("--lr", type=float, default=5e-4)
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--label", default="")
    ap.add_argument("--pos-weight", type=float, default=POS_WEIGHT, help="weight on positive options in the multi-label loss")
    ap.add_argument("--families", default=",".join(FAMILIES), help="comma-separated subset of tools,guides,kind,skills to train")
    ap.add_argument("--skill-scored", type=int, default=2, help="judged skills or tools kept per turn or need, highest scores first")
    ap.add_argument("--skill-zeros", type=int, default=2, help="judged zero-score skills or tools sampled per turn or need")
    ap.add_argument("--turn-rows", choices=("engine-off", "all"), default="engine-off", help="rows the turn families train on")
    ap.add_argument("--tool-truth", choices=rowfile.TOOL_TRUTH, default="consensus", help="how a turn's tool labels are read (rows.py)")
    ap.add_argument("--rank-levels", choices=("blended", "skills-blended", "judged"), default="blended",
                    help="blended: a skill read or a tool used after a need trains at 4 whatever the judges said; skills-blended: only a skill read does; judged: the judges' levels alone")
    ap.add_argument("--tool-weight", choices=("none", "sqrt-inverse"), default="none", help="per-tool positive weighting")
    ap.add_argument("--tool-negatives", type=int, default=0,
                    help="independent heads: sample this many negative tools per training row, weighted to keep their share; validation keeps all (0 keeps all)")
    args = ap.parse_args()
    if args.tool_negatives < 0:
        ap.error("--tool-negatives must be nonnegative")
    POS_WEIGHT = args.pos_weight
    families = tuple(f for f in args.families.split(",") if f)
    if set(families) - set(FAMILIES):
        ap.error("unknown families %s" % ", ".join(sorted(set(families) - set(FAMILIES))))
    select = tuple(f for f in SELECT if f in families) or families
    head_name = "unit-rank" if set(families) <= RANK_FAMILIES else "turn-load"
    out = Path(args.out) if args.out else decide_dir() / "heads" / ("%s-%s.safetensors" % (head_name, args.model.rsplit("/", 1)[-1]))
    lock = headfile.claim(out)  # noqa: F841 - held until the process exits, before any expensive work

    rng = random.Random(args.seed)
    torch.manual_seed(args.seed)
    device = os.environ.get("LYCAON_DECIDE_DEVICE") or ("cuda" if torch.cuda.is_available() else "mps" if torch.backends.mps.is_available() else "cpu")
    corpus = Corpus.load(args.corpus)
    held = {uid for uid, u in corpus.units.items() if u["pack_id"] in args.holdout_pack}
    if held:
        sys.stderr.write("holding out %d units from %s\n" % (len(held), ", ".join(args.holdout_pack)))

    train = rowfile.load(args.train)
    rng.shuffle(train)
    if args.val:
        val = rowfile.load(args.val)
    else:
        cut = max(1, len(train) // 10)
        val, train = train[:cut], train[cut:]

    agent = laya.load(args.model, device=device)
    tok, model = agent.tok, agent.model
    # Match the serving engine's question and context budgets.
    context = int(agent.cfg.get("max_len", 512))
    head_max_len = min(int(corpus.state_spec.get("head_tokens", 512)), max(context - 64, 16))
    max_len = context
    print("context %d head budget %d" % (max_len, head_max_len), flush=True)
    pos = tool_weights(train, args.tool_weight, args.tool_truth)
    train_items = build_items(train, corpus, held, rng, tok, max_len, head_max_len, families, args.skill_scored, args.skill_zeros, args.turn_rows, pos, args.tool_truth, observed_levels(args.rank_levels), args.tool_negatives)
    val_items = build_items(val, corpus, held, rng, tok, max_len, head_max_len, families, args.skill_scored, args.skill_zeros, args.turn_rows, None, args.tool_truth, observed_levels(args.rank_levels))
    sys.stderr.write("items: train=%d val=%d\n" % (len(train_items), len(val_items)))
    state_tokens = state_room(train_items)
    print("state tokens after the options: %s" % state_tokens, flush=True)
    train_loader = DataLoader(Features(precompute(agent, train_items, device)), batch_size=args.batch_size, shuffle=True, collate_fn=collate)
    val_loader = DataLoader(Features(precompute(agent, val_items, device)), batch_size=args.batch_size, shuffle=False, collate_fn=collate)

    params = list(model.head.parameters()) + list(model.scorer.parameters()) + list(model.type_emb.parameters())
    opt = torch.optim.AdamW(params, lr=args.lr, weight_decay=0.01)
    loss, acc, by_type = evaluate(model, val_loader, device, select)
    print("baseline  loss=%.4f acc=%.3f %s" % (loss, acc, by_type), flush=True)

    out.parent.mkdir(parents=True, exist_ok=True)
    best = float("inf")
    stale = 0
    for epoch in range(1, args.epochs + 1):
        model.train()
        t0 = time.time()
        train_loss = rows = 0
        for b in train_loader:
            opt.zero_grad()
            logits = forward_head(model, b["h"].to(device), b["attention_mask"].to(device), b["marker_pos"].to(device),
                                  b["marker_mask"].to(device), b["qtype"].to(device))
            total = batch_loss(logits, b, device)
            (total / len(b["label"])).backward()
            opt.step()
            train_loss += total.item()
            rows += len(b["label"])
        loss, acc, by_type = evaluate(model, val_loader, device, select)
        print("epoch %2d  train=%.4f loss=%.4f acc=%.3f %s (%.1fs)" % (epoch, train_loss / max(rows, 1), loss, acc, by_type, time.time() - t0), flush=True)
        if loss >= best:
            stale += 1
            if stale >= args.patience:
                print("  no better selection loss for %d epochs; stopping" % stale, flush=True)
                break
            continue
        stale = 0
        if True:
            best = loss
            label = args.label or (head_name + "@" + args.model.rsplit("/", 1)[-1] + "+" + corpus.revision)
            headfile.save(out, model, label, args.model, {
                "corpus": corpus.revision, "holdout": sorted(held), "val_loss": loss, "val_acc": acc, "by_type": by_type,
                "train_rows": len(train), "turn_rows": args.turn_rows, "tool_weight": args.tool_weight, "tool_truth": args.tool_truth, "rank_levels": args.rank_levels, "seed": args.seed,
                "max_len": max_len, "head_max_len": head_max_len, "pos_weight": POS_WEIGHT, "state_tokens": state_tokens,
                "tool_encoding": "independent" if corpus.independent() else "joint", "tool_negatives": args.tool_negatives,
                "tool_option_words": corpus.spec["tools"]["option_words"], "families": ",".join(families)})
            print("  saved %s" % out, flush=True)


if __name__ == "__main__":
    main()
