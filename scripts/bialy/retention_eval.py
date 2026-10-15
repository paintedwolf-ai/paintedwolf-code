#!/usr/bin/env python3
"""Evaluate optional continuation detail scores against verified labels.

The token objective is a counterfactual proxy. Provider savings and task
continuity require a separate matched run with recorded usage and outcomes.
"""

import argparse
import json
import math
import random
import time

import rows
from corpus import Corpus
from replay_eval import Engine


def replay(examples, engine, instructions, truth):
    groups, abstained = [], 0
    for row in examples:
        labels = rows.retention_targets(row, truth)
        question = {"type": "multi", "instructions": instructions, "independent": True,
                    "options": rows.retention_options(row)}
        if row["partial"] or not question["options"]:
            abstained += 1
            continue
        try:
            result = engine.call({"method": "decide", "head": "retention", "state": row["state"],
                                  "questions": {"retention": question}})
        except RuntimeError:
            abstained += 1
            continue
        probabilities = (result.get("answers", {}).get("retention") or {}).get("probabilities", {})
        candidates = row["candidates"]
        if any(c["ref"] not in probabilities or not isinstance(probabilities[c["ref"]], (int, float))
               or not math.isfinite(probabilities[c["ref"]]) or not 0 <= probabilities[c["ref"]] <= 1
               for c in candidates):
            abstained += 1
            continue
        groups.append({"row": row, "verified": all(v is not None for v in labels.values()),
                       "candidates": [{**c, "p": probabilities[c["ref"]], "needed": labels[c["ref"]]}
                                      for c in candidates]})
    return groups, abstained


def tally(groups, drop, floor):
    candidates = [(g, c) for g in groups for c in g["candidates"] if c["needed"] is not None]
    released = [(g, c) for g, c in candidates if c["p"] < drop and max(c["p"], 1-c["p"]) >= floor]
    objective = sum(c["tokens"] * max(0, g["row"]["labels"]["calls_after"])
                    - (g["row"]["tokens"] + c["tokens"] if c["needed"] else 0) for g, c in released)
    maximum = sum(c["tokens"] * max(0, g["row"]["labels"]["calls_after"]) for g, c in candidates)
    return {"rows": len(groups), "candidates": len(candidates), "released": len(released),
            "needed_released": sum(bool(c["needed"]) for _, c in released), "objective_tokens": objective,
            "objective_per_request": objective / len(groups) if groups else None,
            "objective_share": objective / maximum if maximum else 0}


def bootstrap(groups, drop, floor, repeats, seed):
    if not groups:
        return {"low": 0, "high": 0}
    rng = random.Random(seed)
    values = sorted(tally(rng.choices(groups, k=len(groups)), drop, floor)["objective_tokens"]
                    for _ in range(max(1, repeats)))
    return {"low": values[int((len(values)-1)*0.025)], "high": values[int((len(values)-1)*0.975)]}


def score(groups, drop, floor, abstained=0, repeats=500, seed=7):
    candidates = [c for g in groups for c in g["candidates"] if c["needed"] is not None]
    positive = [c["p"] for c in candidates if c["needed"]]
    negative = [c["p"] for c in candidates if not c["needed"]]
    auc = sum((a > b) + 0.5*(a == b) for a in positive for b in negative) / (len(positive)*len(negative)) if positive and negative else None
    return {"rows": len(groups), "verified_label_rows": sum(g["verified"] for g in groups), "abstained": abstained,
            "unknown_candidates": sum(c["needed"] is None for g in groups for c in g["candidates"]),
            "objective_source": "counterfactual_token_proxy", "auc": auc,
            "policy": {"drop_below": drop, "confidence_floor": floor}, "engine": tally(groups, drop, floor),
            "structural": {"none": tally(groups, 0, 1)},
            "bootstrap": {"engine": bootstrap(groups, drop, floor, repeats, seed)}}


def fit(groups):
    points = [(drop/20, floor/20) for drop in range(1, 11) for floor in range(10, 20)]
    drop, floor = max(points, key=lambda p: (tally(groups, *p)["objective_tokens"], -p[0], p[1]))
    return {"drop_below": drop, "confidence_floor": floor, "tally": tally(groups, drop, floor)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--corpus", required=True)
    parser.add_argument("--examples", required=True)
    parser.add_argument("--engine", required=True)
    parser.add_argument("--truth", choices=rows.RETENTION_TRUTH, default="needed")
    parser.add_argument("--fit", action="store_true")
    parser.add_argument("--holdout")
    parser.add_argument("--bootstrap", type=int, default=500)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--drop-below", type=float, default=0.2)
    parser.add_argument("--confidence-floor", type=float, default=0.6)
    parser.add_argument("--json")
    args = parser.parse_args()
    corpus = Corpus.load(args.corpus)
    instructions = (corpus.data.get("retention") or {}).get("question")
    if not instructions:
        parser.error("corpus has no retention question")
    if not 0 <= args.drop_below <= 1 or not 0 <= args.confidence_floor <= 1 or args.bootstrap < 1:
        parser.error("probability bounds and a positive bootstrap count are required")
    engine = Engine(args.engine)
    try:
        started = time.monotonic()
        groups, abstained = replay(rows.load(args.examples, rows.RETENTION_SCHEMA), engine, instructions, args.truth)
        drop, floor = args.drop_below, args.confidence_floor
        report = {"corpus": corpus.revision, "truth": args.truth, "overall": score(groups, drop, floor, abstained, args.bootstrap, args.seed)}
        if args.fit:
            report["fitted"] = fit(groups)
            drop, floor = report["fitted"]["drop_below"], report["fitted"]["confidence_floor"]
        if args.holdout:
            held, missed = replay(rows.load(args.holdout, rows.RETENTION_SCHEMA), engine, instructions, args.truth)
            report["holdout"] = score(held, drop, floor, missed, args.bootstrap, args.seed)
        report["elapsed_seconds"] = time.monotonic() - started
        report["recommendation"] = {"facts": [], "fragment": "", "runtime_policy": False}
    finally:
        engine.close()
    text = json.dumps(report, indent=2)
    if args.json:
        with open(args.json, "w", encoding="utf-8") as output:
            output.write(text + "\n")
    print(text)


if __name__ == "__main__":
    main()
