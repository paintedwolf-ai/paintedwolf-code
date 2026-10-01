#!/usr/bin/env python3
"""Choose decision thresholds from validation answers.

Replays a validation set through the engine and picks the thresholds that
meet the experiment's bars, printing a decisions.yaml fragment:

  turn.tools.load_at         the P(true) that maximizes F-beta (--tool-beta,
                             0.5: precision first) across loadable tools; a
                             preloaded tool stays in the chat's standing set
                             and costs every later call, while a missed one
                             costs a single request_tools round trip
  turn.guides.omit_below and turn.guides.confidence_floor
                             the pair that omits the most units while keeping
                             omission precision at or above --omission-precision
                             (an omitted unit that was needed is a silent drop)
  request.load_at, max_loads, nearest_loads
                             the unit-rank score, cap, and fallback count
                             that maximize F-beta (--request-beta) over the
                             tools each request_tools need was for (the tools
                             the turn used after it, and those the judge
                             scored 3 or more), replaying the fallback: a
                             need nothing reaches load_at loads its nearest
                             tools
  turn.skills.preload_at     the lowest top score that preloads the most turns
                             while the preloaded skill was judged relevant
                             (skill_scores of 3 or more) at or above
                             --preload-precision; a wrong preload steers the
                             whole turn

Each is reported with its support; tools also with the expected calibration
error, and the tool threshold also per host (coordinator turns and worker
legs), so a host that would want a different threshold shows up. Rows are
training rows (rows.py) and every question is the one the row's turn offered.

Usage: calibrate.py --corpus C --examples FILE [--engine PATH]
                    [--tool-beta 0.5] [--request-beta 1.0] [--omission-precision 0.97]
                    [--preload-precision 0.9] [--tool-truth RULE]
"""

import argparse
import collections
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
import rows as rowfile  # noqa: E402
from corpus import Corpus  # noqa: E402
from replay_eval import Engine, ece, option_answer  # noqa: E402


def fbeta(tp, fp, fn, beta):
    if tp == 0:
        return 0.0
    p = tp / (tp + fp)
    r = tp / (tp + fn)
    b2 = beta * beta
    return (1 + b2) * p * r / (b2 * p + r)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--examples", required=True)
    ap.add_argument("--engine", default=os.environ.get("LYCAON_DECIDE_ENGINE"), help="a bialy launcher: `bialy serve --model ... --head turn-load=...` (default $LYCAON_DECIDE_ENGINE)")
    ap.add_argument("--tool-beta", type=float, default=0.5)
    ap.add_argument("--request-beta", type=float, default=1.0)
    ap.add_argument("--tool-truth", choices=rowfile.TOOL_TRUTH, default="consensus", help="how a turn's tool labels are read (rows.py)")
    ap.add_argument("--omission-precision", type=float, default=0.97)
    ap.add_argument("--preload-precision", type=float, default=0.9)
    args = ap.parse_args()
    if not args.engine:
        ap.error("--engine (or LYCAON_DECIDE_ENGINE) must name a bialy launcher")

    corpus = Corpus.load(args.corpus)
    examples = rowfile.load(args.examples)
    tool_pairs = []
    guide_pairs = []
    skill_tops = []
    request_pairs = []
    tool_cards = corpus.tool_cards()
    cards = corpus.skill_cards()
    roster = sorted(cards)
    engine = Engine(args.engine)
    try:
        for ex in examples:
            loadable = [t for t in ex["offered"]["loadable"] if t in tool_cards]
            for n, request in enumerate(ex["labels"].get("requests") or []):
                exact = set(request.get("exact") or [])
                rest = [t for t in loadable if t not in exact]
                wanted = set(request.get("after") or []) | rowfile.relevant(rowfile.need_pairs(ex, n))
                if not rest or not wanted & set(rest):
                    continue
                scores = engine.call({"method": "rank", "head": "unit-rank", "task": request["need"], "candidates": [tool_cards[t] for t in rest]})["scores"]
                request_pairs.append([(scores[i], rest[i] in wanted) for i in range(len(rest))])
            if ex["partial"]:
                continue
            qs = corpus.row_questions(ex)
            answers = engine.call({"method": "decide", "head": "turn-load", "state": ex["state"], "questions": qs})["answers"]
            targets = rowfile.tool_targets(ex, args.tool_truth)
            for name in qs.get("tools", {}).get("options", {}):
                if targets.get(name, 0) is None:
                    continue
                p, _ = option_answer(answers, "tools", name)
                tool_pairs.append((0.0 if p is None else p, targets.get(name, 0) == 1, ex["host"]))
            guides = ex["labels"]["guides"]
            for uid in qs.get("guides", {}).get("options", {}):
                needed = guides.get(uid)
                if needed is None:
                    continue
                p, conf = option_answer(answers, "guides", uid)
                guide_pairs.append((0.0 if p is None else p, 0.0 if conf is None else conf, bool(needed)))
            judged = rowfile.skill_pairs(ex)
            if roster and judged:
                scores = engine.call({"method": "rank", "head": "unit-rank", "task": ex["state"]["user"], "candidates": [cards[name] for name in roster]})["scores"]
                best = max(range(len(scores)), key=lambda i: scores[i])
                skill_tops.append((scores[best], roster[best] in rowfile.relevant(judged)))
    finally:
        engine.close()

    def best_load_at(pairs):
        best = (0.0, 0.5)
        for step in range(5, 96):
            thr = step / 100
            tp = sum(1 for p, y, _ in pairs if p >= thr and y)
            fp = sum(1 for p, y, _ in pairs if p >= thr and not y)
            fn = sum(1 for p, y, _ in pairs if p < thr and y)
            score = fbeta(tp, fp, fn, args.tool_beta)
            if score > best[0]:
                best = (score, thr)
        return best

    best = best_load_at(tool_pairs)
    print("turn:\n  tools:\n    load_at: %.2f  # F%g=%.3f support=%d ece=%.3f" % (
        best[1], args.tool_beta, best[0], sum(1 for _, y, _ in tool_pairs if y), ece([(p, 1 if y else 0) for p, y, _ in tool_pairs])))
    for host in sorted({h for _, _, h in tool_pairs}):
        pairs = [pair for pair in tool_pairs if pair[2] == host]
        score, thr = best_load_at(pairs)
        print("    # %s alone: load_at %.2f, F%g=%.3f, support=%d" % (host, thr, args.tool_beta, score, sum(1 for _, y, _ in pairs if y)))

    chosen = None
    for omit in range(5, 51):
        for floor in range(50, 100, 5):
            o, f = omit / 100, floor / 100
            omitted = [(needed) for p, c, needed in guide_pairs if p < o and c >= f]
            if not omitted:
                continue
            precision = sum(1 for needed in omitted if not needed) / len(omitted)
            if precision >= args.omission_precision and (chosen is None or len(omitted) > chosen[0]):
                chosen = (len(omitted), o, f, precision)
    if chosen is None:
        print("  guides:\n    # no threshold pair reaches omission precision %.2f on %d judged units; keep every unit until the head improves" % (args.omission_precision, len(guide_pairs)))
    else:
        print("  guides:\n    omit_below: %.2f\n    confidence_floor: %.2f  # omits %d of %d judged units at precision %.3f" % (
            chosen[1], chosen[2], chosen[0], len(guide_pairs), chosen[3]))
    counts = collections.Counter(needed for _, _, needed in guide_pairs)
    print("# judged units: needed=%d unneeded=%d" % (counts[True], counts[False]))

    ranked = [sorted(pairs, key=lambda x: -x[0]) for pairs in request_pairs]
    best = None
    for max_loads in range(1, 7):
        for nearest in range(1, max_loads + 1):
            for step in range(50, 501, 5):
                thr = step / 100
                tp = fp = fn = served = 0
                for pairs in ranked:
                    loaded = [(sc, y) for sc, y in pairs[:max_loads] if sc >= thr] or pairs[:nearest]
                    hits = sum(1 for _, y in loaded if y)
                    tp, fp, fn = tp + hits, fp + len(loaded) - hits, fn + sum(1 for _, y in pairs if y) - hits
                    served += hits > 0
                score = fbeta(tp, fp, fn, args.request_beta)
                if best is None or score > best[0]:
                    best = (score, thr, max_loads, nearest, tp, fp, fn, served)
    if not request_pairs:
        print("request:\n  # no request_tools needs in the examples; thresholds unchanged")
    else:
        score, thr, max_loads, nearest, tp, fp, fn, served = best
        print("request:\n  load_at: %.2f\n  max_loads: %d\n  nearest_loads: %d  # F%g=%.3f over %d needs: %.1f loads per need at precision %.3f, recall %.3f; %.3f of needs get a tool they used" % (
            thr, max_loads, nearest, args.request_beta, score, len(ranked), (tp + fp) / len(ranked), tp / (tp + fp), tp / (tp + fn), served / len(ranked)))

    chosen = None
    for step in range(100, 401):
        thr = step / 100
        preloads = [ok for top, ok in skill_tops if top >= thr]
        if not preloads:
            continue
        precision = sum(preloads) / len(preloads)
        if precision >= args.preload_precision and (chosen is None or len(preloads) > chosen[0]):
            chosen = (len(preloads), thr, precision)
    if not skill_tops:
        print("  skills:\n    # no judged skill_scores in the examples; preload_at unchanged")
    elif chosen is None:
        print("  skills:\n    # no top score reaches preload precision %.2f over %d judged turns; keep preload_at above every top score until the head improves" % (args.preload_precision, len(skill_tops)))
    else:
        print("  skills:\n    preload_at: %.2f  # preloads %d of %d judged turns at precision %.3f" % (chosen[1], chosen[0], len(skill_tops), chosen[2]))


if __name__ == "__main__":
    main()
