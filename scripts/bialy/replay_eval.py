#!/usr/bin/env python3
"""Replay labelled turns through the decision engine and score it.

Reads training rows (rows.py). For every row the engine answers the question
set the row's turn was asked (one option per loadable tool and scored
instruction unit it offered, plus the kind choice) and a skill ranking, and
the answers are compared with the labels:

  tools    load precision, recall, F1 at load_at (micro, over every option),
           macro recall (the mean of per-tool recall, so a rare tool counts
           as much as a common one), calibration error, loads per turn, and
           the share of turns whose every used tool was loaded (no round trip),
           and per-tool AUC with its mean over tools with 5 or more needed turns
           (0.5 is a score that ignores the request)
  guides   omission precision (an omitted unit was unneeded), omission rate,
           and recall of needed units; unknown labels are skipped
  kind     accuracy at the confidence floor, abstention rate
  skills   over the corpus's skill cards, the text the engine ranks: top-1
           hit rate against the skill the coordinator read first, the share
           of turns whose first-read skill the pruned roster listed (score at
           or above list_at within roster_max), and against judged
           skill_scores the precision of preloads (the top skill clears
           preload_at and the judge scored it 3 or more), the preload rate,
           and the share of turns whose roster lists a judged skill
  requests for each request_tools need: the share of the tools the turn
           then used that its spelled-out names already load, and over the
           rest, which unit-rank ranks, the share it loads (score at or
           above request.load_at, at most max_loads, or the nearest_loads
           closest when none reaches load_at), ranked loads per
           need, the share of those judged relevant (3 or more), and the
           mean reciprocal rank of the first used tool
  wanted   for each need, the same over the tools calibration optimizes for: the
           tools used after it and those both judges called relevant (top-1 hit,
           share served, loads per need, precision, MRR)
  sessions each coordinator session's turns replayed in order: the standing set of
           tools its preloads and needs build up, by turn, and the share of later
           turns whose needed tools were already standing
  bytes    prompt bytes the decision saved per turn: omitted unit bodies plus
           unloaded tool schemas, against loading everything
  latency  engine time per turn, p50 and p95
  state    the share of turns whose request reached the catalog's
           user_text_chars bound, so truncation is a measured cost

Results print overall and broken down by host, surface, language, project,
and the model that drove the session, and per pack of origin when
--holdout-pack names packs whose units were held out of training. The engine
is a bialy launcher, from --engine or LYCAON_DECIDE_ENGINE.

Usage: replay_eval.py --corpus FILE --examples FILE [--engine PATH] [--limit N]
                      [--holdout-pack ID ...] [--tool-truth RULE] [--json OUT]
"""

import argparse
import collections
import json
import os
import statistics
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
import rows as rowfile  # noqa: E402
from corpus import Corpus  # noqa: E402


class Engine:
    def __init__(self, binary):
        self.proc = subprocess.Popen([binary], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1)
        self.seq = 0
        # Breakdowns re-score subsets of the same rows; the engine is deterministic, so
        # each distinct request is answered once.
        self.answers = {}
        self.hello = self.call({"method": "hello"})

    def call(self, req):
        key = json.dumps(req, sort_keys=True, ensure_ascii=False)
        if key not in self.answers:
            self.answers[key] = self.ask(dict(req))
        return self.answers[key]

    def ask(self, req):
        self.seq += 1
        req["id"] = self.seq
        self.proc.stdin.write(json.dumps(req, ensure_ascii=False) + "\n")
        self.proc.stdin.flush()
        while True:
            line = self.proc.stdout.readline()
            if not line:
                raise RuntimeError("engine exited")
            resp = json.loads(line)
            if resp.get("id") == self.seq:
                if resp.get("error"):
                    raise RuntimeError(resp["error"])
                return resp

    def close(self):
        self.proc.stdin.close()
        self.proc.wait(timeout=10)


def option_answer(answers, qid, name):
    """One option's (probability, certainty) out of a multi answer; absent when the engine did not answer."""
    probs = (answers.get(qid) or {}).get("probabilities") or {}
    if name not in probs:
        return None, None
    p = float(probs[name])
    return p, max(p, 1.0 - p)


def auc(pairs):
    """Area under the ROC curve of (probability, outcome) pairs: the chance a needed option
    outscores an unneeded one. 0.5 means the score ignores the request."""
    pos = sum(1 for _, y in pairs if y)
    neg = len(pairs) - pos
    if not pos or not neg:
        return None
    ranked = sorted(pairs, key=lambda x: x[0])
    rank_sum, i = 0.0, 0
    while i < len(ranked):
        j = i
        while j < len(ranked) and ranked[j][0] == ranked[i][0]:
            j += 1
        rank_sum += (i + j + 1) / 2 * sum(1 for k in range(i, j) if ranked[k][1])
        i = j
    return (rank_sum - pos * (pos + 1) / 2) / (pos * neg)


def macro_auc(tools, support=5):
    """Mean per-tool AUC over tools with at least `support` needed turns."""
    areas = [v["auc"] for v in tools.values() if v.get("auc") is not None and v["support"] >= support]
    return round(statistics.mean(areas), 3) if areas else None


def session_standing(standing, schema_bytes):
    """Replay each coordinator session's turns in receipt order: the standing set's size and
    schema bytes by turn, and the share of later turns whose needed tools were already
    standing. Every turn after the first counts as warm; a chat idle past the cache window
    drops its set, so real chats with pauses carry less than this reports."""
    by_turn = collections.defaultdict(list)
    covered = later = 0
    for turns in standing.values():
        held = set()
        for k, (_, joined, needed) in enumerate(sorted(turns, key=lambda t: t[0])):
            if k and needed:
                later += 1
                covered += needed <= held
            held |= joined
            by_turn[min(k + 1, 3)].append((len(held), sum(schema_bytes.get(t, 0) for t in held)))
    return {"sessions": len(standing),
            "by_turn": {("%d" % k if k < 3 else "3+"): {"turns": len(v), "standing_tools": round(statistics.mean(a for a, _ in v), 2),
                                                          "standing_bytes": round(statistics.mean(b for _, b in v))}
                        for k, v in sorted(by_turn.items())},
            "later_turns_already_standing": round(covered / later, 3) if later else None}


def ece(pairs, bins=10):
    """Expected calibration error over (probability, outcome) pairs."""
    if not pairs:
        return 0.0
    buckets = collections.defaultdict(list)
    for p, y in pairs:
        buckets[min(int(p * bins), bins - 1)].append((p, y))
    total = 0.0
    for items in buckets.values():
        conf = sum(p for p, _ in items) / len(items)
        acc = sum(y for _, y in items) / len(items)
        total += abs(conf - acc) * len(items) / len(pairs)
    return total


def prf(tp, fp, fn):
    prec = tp / (tp + fp) if tp + fp else 0.0
    rec = tp / (tp + fn) if tp + fn else 0.0
    f1 = 2 * prec * rec / (prec + rec) if prec + rec else 0.0
    return round(prec, 3), round(rec, 3), round(f1, 3)


def score(examples, engine, corpus, schema_bytes, unit_bytes, preload_at, tool_truth, timed=True):
    q = corpus.spec
    load_at = float(q["tools"]["load_at"])
    omit_below = float(q["guides"]["omit_below"])
    conf_floor = float(q["guides"]["confidence_floor"])
    kind_floor = float(q["kind"]["confidence_floor"])
    veto = bool(q["kind"].get("veto_tools"))
    tool_stats = collections.defaultdict(collections.Counter)
    tool_calib = collections.defaultdict(list)
    guide_stats = collections.defaultdict(collections.Counter)
    kind_hits = kind_total = kind_abstain = 0
    skill_hits = skill_total = skill_listed = 0
    preloads = preload_hits = judged_total = judged_listed = 0
    roster_sizes = []
    list_at = float(q.get("skills", {}).get("list_at", 1.0))
    roster_max = int(q.get("skills", {}).get("roster_max", 6))
    if preload_at is None:
        preload_at = float(q.get("skills", {}).get("preload_at", 3.0))
    saved_turns = eligible_turns = 0
    bytes_saved = []
    latencies = []
    cards = corpus.skill_cards()
    roster = sorted(cards)
    tool_cards = corpus.tool_cards()
    req_load_at = float(corpus.data.get("request", {}).get("load_at", 2.5))
    req_max = int(corpus.data.get("request", {}).get("max_loads", 12))
    req_nearest = int(corpus.data.get("request", {}).get("nearest_loads", 0))
    req_needs = req_used = req_hit = req_loads = req_rel = 0
    req_named = req_named_total = 0
    req_rr = []
    want_needs = want_top1 = want_served = want_loads = want_hits = 0
    want_rr = []
    loads_per_turn = []
    truncated = 0
    user_chars = int(corpus.state_spec.get("user_text_chars", 900))
    # Per coordinator turn, what joined the chat's standing set: the head's preloads and
    # the tools its request_tools needs loaded.
    standing = collections.defaultdict(list)
    for ex in examples:
        loadable = [t for t in ex["offered"]["loadable"] if t in tool_cards]
        requested = set()
        for n, request in enumerate(ex["labels"].get("requests") or []):
            exact = set(request.get("exact") or [])
            rest = [t for t in loadable if t not in exact]
            after = [t for t in request.get("after") or [] if t in loadable]
            req_named_total += len(after)
            req_named += sum(1 for t in after if t in exact)
            used = [t for t in after if t not in exact]
            judged = rowfile.relevant(rowfile.need_pairs(ex, n))
            wanted = (set(used) | judged) & set(rest)
            # Production loads the named tools and the ranked ones for every need, whether or
            # not this row labels any of them; the standing set counts them all.
            requested |= exact
            if not rest:
                continue
            scores = engine.call({"method": "rank", "head": "unit-rank", "task": request["need"], "candidates": [tool_cards[t] for t in rest]})["scores"]
            order = sorted(range(len(rest)), key=lambda i: -scores[i])
            loaded = {rest[i] for i in order[:req_max] if scores[i] >= req_load_at}
            if not loaded:
                loaded = {rest[i] for i in order[:req_nearest]}
            requested |= loaded
            if not wanted:
                continue
            # Against what calibration optimizes: the tools used after the need and those
            # both judges called relevant.
            want_needs += 1
            want_top1 += rest[order[0]] in wanted
            want_served += bool(loaded & wanted)
            want_loads += len(loaded)
            want_hits += len(loaded & wanted)
            want_rr.append(1 / min(k + 1 for k, i in enumerate(order) if rest[i] in wanted))
            if not used:
                continue
            req_needs += 1
            req_used += len(used)
            req_hit += sum(1 for t in used if t in loaded)
            req_loads += len(loaded)
            req_rel += sum(1 for t in loaded if t in judged)
            positions = {rest[i]: n + 1 for n, i in enumerate(order)}
            req_rr.append(1 / min(positions[t] for t in used))
        if ex["partial"]:
            continue
        truncated += len(ex["state"].get("user", "")) >= user_chars
        qs = corpus.row_questions(ex)
        started = time.perf_counter()
        answers = engine.call({"method": "decide", "head": "turn-load", "state": ex["state"], "questions": qs})["answers"]
        latencies.append((time.perf_counter() - started) * 1000)
        kind = answers.get("kind", {})
        vetoed = veto and float(kind.get("confidence", 0.0)) >= kind_floor and kind.get("choice") == "answer_only"
        targets = rowfile.tool_targets(ex, tool_truth)
        truth_tools = {t for t, v in targets.items() if v == 1}
        loaded = set()
        saved = 0
        for name in qs.get("tools", {}).get("options", {}):
            p, _ = option_answer(answers, "tools", name)
            p = 0.0 if p is None else p
            load = p >= load_at and not vetoed
            if load:
                loaded.add(name)
            else:
                saved += schema_bytes.get(name, 0)
            if targets.get(name, 0) is None:
                continue  # the judges disagreed; neither a hit nor a miss
            hit = targets.get(name, 0) == 1
            tool_calib[name].append((p, 1 if hit else 0))
            tool_stats[name][("tp" if hit else "fp") if load else ("fn" if hit else "tn")] += 1
        loads_per_turn.append(len(loaded))
        if ex["host"] == "coordinator":
            standing[ex["session"]].append((ex["receipt"], loaded | requested, truth_tools))
        if truth_tools:
            eligible_turns += 1
            if truth_tools <= loaded:
                saved_turns += 1
        guide_truth = ex["labels"]["guides"]
        for uid in qs.get("guides", {}).get("options", {}):
            p, conf = option_answer(answers, "guides", uid)
            p, conf = (0.0, 0.0) if p is None else (p, conf)
            omitted = uid in q["guides"].get("omittable", []) and p < omit_below and conf >= conf_floor
            needed = guide_truth.get(uid)
            if omitted:
                saved += unit_bytes.get(uid, 0)
            if needed is None:
                guide_stats[uid]["unknown"] += 1
                guide_stats[uid]["omitted_unknown"] += omitted
                continue
            if omitted and needed:
                guide_stats[uid]["omitted_needed"] += 1
            elif omitted:
                guide_stats[uid]["omitted_unneeded"] += 1
            elif needed:
                guide_stats[uid]["kept_needed"] += 1
            else:
                guide_stats[uid]["kept_unneeded"] += 1
        bytes_saved.append(saved)
        kind_total += 1
        if float(kind.get("confidence", 0.0)) < kind_floor:
            kind_abstain += 1
        elif kind.get("choice") == ex["labels"]["kind"]:
            kind_hits += 1
        read = [name for name in ex["labels"].get("skills") or [] if name in cards]
        judged = {name for name in rowfile.relevant(rowfile.skill_pairs(ex)) if name in cards}
        if roster and (read or ex["labels"].get("skill_scores")):
            scores = engine.call({"method": "rank", "head": "unit-rank", "task": ex["state"]["user"], "candidates": [cards[name] for name in roster]})["scores"]
            best = max(range(len(scores)), key=lambda i: scores[i])
            order = sorted(range(len(scores)), key=lambda i: -scores[i])
            listed = [roster[i] for i in order[:roster_max] if scores[i] >= list_at]
            roster_sizes.append(len(listed))
            if read:
                skill_total += 1
                if scores[best] >= preload_at and roster[best] == read[0]:
                    skill_hits += 1
                if read[0] in listed:
                    skill_listed += 1
            if ex["labels"].get("skill_scores"):
                judged_total += 1
                if scores[best] >= preload_at:
                    preloads += 1
                    preload_hits += roster[best] in judged
                if judged and judged & set(listed):
                    judged_listed += 1
    report = {"turns": len(examples), "tools": {}, "guides": {}, "kind": {}, "skills": {}, "requests": {}, "savings": {}, "latency_ms": {}}
    tp = fp = fn = 0
    for name, c in sorted(tool_stats.items()):
        tp += c["tp"]
        fp += c["fp"]
        fn += c["fn"]
        prec, rec, f1 = prf(c["tp"], c["fp"], c["fn"])
        area = auc(tool_calib[name])
        report["tools"][name] = {"precision": prec, "recall": rec, "f1": f1, "support": c["tp"] + c["fn"], "ece": round(ece(tool_calib[name]), 3),
                                 "auc": None if area is None else round(area, 3)}
    prec, rec, f1 = prf(tp, fp, fn)
    recalls = [v["recall"] for v in report["tools"].values() if v["support"]]
    report["tools"]["_all"] = {"precision": prec, "recall": rec, "f1": f1, "support": tp + fn,
                               "macro_recall": round(statistics.mean(recalls), 3) if recalls else None,
                               "macro_auc": macro_auc(report["tools"]),
                               "loads_per_turn": round(statistics.mean(loads_per_turn), 2) if loads_per_turn else 0.0}
    omitted_unneeded = omitted_needed = kept_needed = 0
    for uid, c in sorted(guide_stats.items()):
        omitted = c["omitted_needed"] + c["omitted_unneeded"]
        report["guides"][uid] = {
            "omission_precision": round(c["omitted_unneeded"] / omitted, 3) if omitted else None,
            "omission_rate": round(omitted / max(sum(c.values()) - c["unknown"], 1), 3),
            "recall": round(c["kept_needed"] / max(c["kept_needed"] + c["omitted_needed"], 1), 3),
            "unknown": c["unknown"],
        }
        omitted_unneeded += c["omitted_unneeded"]
        omitted_needed += c["omitted_needed"]
        kept_needed += c["kept_needed"]
    omitted = omitted_unneeded + omitted_needed
    report["guides"]["_all"] = {
        "omission_precision": round(omitted_unneeded / omitted, 3) if omitted else None,
        "recall": round(kept_needed / max(kept_needed + omitted_needed, 1), 3),
    }
    report["kind"] = {"accuracy": round(kind_hits / kind_total, 3) if kind_total else 0.0,
                      "abstained": round(kind_abstain / kind_total, 3) if kind_total else 0.0}
    report["skills"] = {"top1": round(skill_hits / skill_total, 3) if skill_total else 0.0,
                        "listed": round(skill_listed / skill_total, 3) if skill_total else 0.0,
                        "roster_size": round(statistics.mean(roster_sizes), 1) if roster_sizes else 0,
                        "roster_max": roster_max, "list_at": list_at, "preload_at": preload_at, "support": skill_total,
                        "judged": {"preload_precision": round(preload_hits / preloads, 3) if preloads else None,
                                   "preload_rate": round(preloads / judged_total, 3) if judged_total else 0.0,
                                   "listed_relevant": round(judged_listed / judged_total, 3) if judged_total else 0.0,
                                   "support": judged_total}}
    report["requests"] = {"used_named": round(req_named / req_named_total, 3) if req_named_total else None,
                          "ranked_needs": req_needs, "used_loaded": round(req_hit / req_used, 3) if req_used else None,
                          "loads_per_need": round(req_loads / req_needs, 2) if req_needs else None,
                          "loads_judged_relevant": round(req_rel / req_loads, 3) if req_loads else None,
                          "mrr": round(statistics.mean(req_rr), 3) if req_rr else None, "load_at": req_load_at,
                          "wanted": {"needs": want_needs, "top1": round(want_top1 / want_needs, 3) if want_needs else None,
                                     "served": round(want_served / want_needs, 3) if want_needs else None,
                                     "loads_per_need": round(want_loads / want_needs, 2) if want_needs else None,
                                     "precision": round(want_hits / want_loads, 3) if want_loads else None,
                                     "mrr": round(statistics.mean(want_rr), 3) if want_rr else None}}
    report["sessions"] = session_standing(standing, schema_bytes)
    report["savings"] = {"turns_fully_preloaded": saved_turns, "turns_needing_loads": eligible_turns,
                         "bytes_saved_per_turn": round(statistics.mean(bytes_saved)) if bytes_saved else 0}
    report["state"] = {"truncated_share": round(truncated / max(len(loads_per_turn), 1), 3), "user_text_chars": user_chars}
    if timed and latencies:
        latencies.sort()
        report["latency_ms"] = {"p50": round(statistics.median(latencies)), "p95": round(latencies[min(len(latencies) - 1, int(len(latencies) * 0.95))])}
    return report


def load_sizes(corpus):
    """Bytes each tool schema and unit body would add to a prompt."""
    root = corpus_root()
    schemas = root / "lycaon/config/packs/painted-wolf/platform/tools/schemas"
    schema_bytes = {name: (schemas / (name + ".yaml")).stat().st_size for name in corpus.tools if (schemas / (name + ".yaml")).exists()}
    unit_bytes = {}
    for uid, unit in corpus.units.items():
        for pack_dir in (root / "lycaon/config/packs/painted-wolf").iterdir():
            path = pack_dir / "shared/units" / (uid + ".md")
            if path.exists():
                unit_bytes[uid] = path.stat().st_size
    return schema_bytes, unit_bytes


def corpus_root():
    from pathlib import Path
    return Path(__file__).resolve().parents[2]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--examples", required=True)
    ap.add_argument("--engine", default=os.environ.get("LYCAON_DECIDE_ENGINE"), help="a bialy launcher: `bialy serve --model ... --head turn-load=...` (default $LYCAON_DECIDE_ENGINE)")
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--tool-truth", choices=rowfile.TOOL_TRUTH, default="consensus", help="how a turn's tool labels are read (rows.py)")
    ap.add_argument("--preload-at", type=float, default=None, help="skill preload threshold (default: the corpus's turn.skills.preload_at)")
    ap.add_argument("--holdout-pack", action="append", default=[], help="pack ids whose units were held out of training; reported separately")
    ap.add_argument("--json", default="")
    args = ap.parse_args()
    if not args.engine:
        ap.error("--engine (or LYCAON_DECIDE_ENGINE) must name a bialy launcher")

    corpus = Corpus.load(args.corpus)
    examples = rowfile.load(args.examples)
    if args.limit:
        examples = examples[: args.limit]
    schema_bytes, unit_bytes = load_sizes(corpus)
    engine = Engine(args.engine)
    try:
        report = {"engine": engine.hello.get("engine"), "corpus": corpus.revision, "tool_truth": args.tool_truth,
                  "overall": score(examples, engine, corpus, schema_bytes, unit_bytes, args.preload_at, args.tool_truth)}
        for key, field in (("by_host", "host"), ("by_surface", "surface"), ("by_lang", "lang"), ("by_project", "project"), ("by_model", "model")):
            groups = collections.defaultdict(list)
            for ex in examples:
                groups[ex[field]].append(ex)
            if len(groups) > 1:
                report[key] = {name: score(exs, engine, corpus, schema_bytes, unit_bytes, args.preload_at, args.tool_truth, timed=False)
                               for name, exs in sorted(groups.items())}
        if args.holdout_pack:
            held = {uid for uid, u in corpus.units.items() if u["pack_id"] in args.holdout_pack}
            report["holdout"] = {"packs": args.holdout_pack, "units": sorted(held),
                                 "guides": {uid: v for uid, v in report["overall"]["guides"].items() if uid in held}}
    finally:
        engine.close()
    text = json.dumps(report, indent=2, ensure_ascii=False)
    if args.json:
        open(args.json, "w", encoding="utf-8").write(text + "\n")
    print(text)


if __name__ == "__main__":
    main()
