#!/usr/bin/env python3
"""Measure skill discovery: what the skill listing and a skills_read lookup would show.

A turn's skill listing and a lookup's results both show the skills that rank in
the top k and score at least list_at. A preload threshold says nothing about
this: a relevant skill that ranks seventh never reaches the model unless it
asks for the full catalog. For every example with skills both judges scored
likely or certain, the engine ranks the whole roster, and the report counts:

  any_visible@k   the example shows at least one relevant skill
  all_visible@k   it shows every relevant skill
  any_raw@k       at least one relevant skill ranks in the top k, whatever its score

per skill (support and visibility) and pooled by how often each skill was a
training positive (common, mid, rare), so a pooled average cannot hide a
category the head never surfaces. k is the listing (roster_max) and the
lookup's result limit.

Usage: skill_discovery.py --corpus FILE --examples FILE --engine LAUNCHER [--train FILE]
                          [--roster FILE] [--list-at 1.0] [--k 6 --k 8] [--json OUT]

--roster names the skills to rank (a JSON list, e.g. a live chat's loaded roster);
without it the corpus's whole skill catalog is ranked. --train supplies the
training rows whose judged positives set each skill's band.
"""

import argparse
import collections
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import rows as rowfile  # noqa: E402
from corpus import Corpus  # noqa: E402
from replay_eval import Engine  # noqa: E402

BANDS = (("common", 50, 10**9), ("mid", 10, 50), ("rare", 0, 10))


def ranked(engine, cards, task, names):
    scores = engine.call({"method": "rank", "head": "unit-rank", "task": task, "candidates": [cards[n] for n in names]})["scores"]
    return sorted(zip(names, scores), key=lambda x: -x[1])


def visible(order, skill, k, list_at):
    """The skill is shown at k: it ranks in the top k and scores at least list_at."""
    return any(n == skill and s >= list_at for n, s in order[:k])


def measure(engine, cards, examples, names, train_positives, ks, list_at):
    agg = collections.Counter()
    per = collections.defaultdict(collections.Counter)
    for ex in examples:
        relevant = rowfile.relevant(rowfile.skill_pairs(ex)) & set(names)
        if not relevant:
            continue
        order = ranked(engine, cards, ex["state"]["user"], names)
        rank = {n: i + 1 for i, (n, _) in enumerate(order)}
        agg["examples"] += 1
        for k in ks:
            agg["any_raw@%d" % k] += any(rank[s] <= k for s in relevant)
            agg["any_visible@%d" % k] += any(visible(order, s, k, list_at) for s in relevant)
            agg["all_visible@%d" % k] += all(visible(order, s, k, list_at) for s in relevant)
        for s in relevant:
            per[s]["support"] += 1
            for k in ks:
                per[s]["visible@%d" % k] += visible(order, s, k, list_at)
    n = agg["examples"]
    report = {"examples": n, **{key: round(v / n, 3) for key, v in agg.items() if key != "examples" and n}}
    report["per_skill"] = {s: dict(v, train_positives=train_positives[s]) for s, v in sorted(per.items())}
    for band, lo, hi in BANDS:
        skills = [v for s, v in per.items() if lo <= train_positives[s] < hi]
        cases = sum(v["support"] for v in skills)
        entry = {"skills": len(skills), "cases": cases}
        for k in ks:
            entry["visible@%d" % k] = round(sum(v["visible@%d" % k] for v in skills) / cases, 3) if cases else None
            rates = [v["visible@%d" % k] / v["support"] for v in skills if v["support"]]
            entry["macro_visible@%d" % k] = round(sum(rates) / len(rates), 3) if rates else None
        report[band] = entry
    return report


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--examples", required=True)
    ap.add_argument("--engine", default=os.environ.get("LYCAON_DECIDE_ENGINE"))
    ap.add_argument("--train", default="", help="training rows whose judged positives set each skill's band")
    ap.add_argument("--roster", default="", help="a JSON list of the skill names to rank (default: every corpus skill)")
    ap.add_argument("--list-at", type=float, default=None, help="default: the corpus's turn.skills.list_at")
    ap.add_argument("--k", type=int, action="append", default=[], help="listing sizes (default: roster_max and 8)")
    ap.add_argument("--json", default="")
    args = ap.parse_args()
    if not args.engine:
        ap.error("--engine (or LYCAON_DECIDE_ENGINE) must name a bialy launcher")
    corpus = Corpus.load(args.corpus)
    cards = corpus.skill_cards()
    names = [n for n in json.load(open(args.roster, encoding="utf-8")) if n in cards] if args.roster else sorted(cards)
    skills_spec = corpus.spec.get("skills", {})
    list_at = args.list_at if args.list_at is not None else float(skills_spec.get("list_at", 1.0))
    ks = args.k or sorted({int(skills_spec.get("roster_max", 6)), 8})
    train_positives = collections.Counter()
    if args.train:
        for row in rowfile.load(args.train):
            if not row["partial"]:
                train_positives.update(rowfile.relevant(rowfile.skill_pairs(row)))
    examples = [ex for ex in rowfile.load(args.examples) if not ex["partial"]]
    engine = Engine(args.engine)
    try:
        report = {"engine": engine.hello.get("engine"), "roster": len(names), "list_at": list_at, "k": ks,
                  "overall": measure(engine, cards, examples, names, train_positives, ks, list_at)}
    finally:
        engine.close()
    summary = {k: v for k, v in report["overall"].items() if k != "per_skill"}
    print(json.dumps(summary, indent=2))
    if args.json:
        with open(args.json, "w", encoding="utf-8") as fh:
            json.dump(report, fh, indent=2)


if __name__ == "__main__":
    main()
