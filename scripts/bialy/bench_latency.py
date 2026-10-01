#!/usr/bin/env python3
"""Measure the engine's turn latency on this machine.

Sends the investigate surface's full question set (every loadable tool and
scored unit plus the kind choice) with realistic states and reports p50 and
p95 per call, so the deadline in decisions.yaml is a measured budget. Runs
against a bialy launcher (--engine or LYCAON_DECIDE_ENGINE), so the same
command measures the base checkpoint or a trained head.

Usage: bench_latency.py --corpus C [--engine PATH] [--surface ID] [--rounds 20]
"""

import argparse
import json
import os
import statistics
import sys
import time

sys.path.insert(0, os.path.dirname(__file__))
from corpus import Corpus  # noqa: E402
from replay_eval import Engine  # noqa: E402

STATES = [
    "Please build a professional todo app with SSO and run it in a docker container for local development.",
    "What improvements would you recommend?",
    "Explain how the auth module decides which session to use.",
    "Bring up Gitea in Docker and build a release-management CLI against it.",
    "commit the current changes in logical groups",
    "Redesign my technical site with hugo and launch it locally so I can review it.",
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--engine", default=os.environ.get("LYCAON_DECIDE_ENGINE"), help="a bialy launcher: `bialy serve --model ... --head turn-load=...` (default $LYCAON_DECIDE_ENGINE)")
    ap.add_argument("--surface", default="implement_investigate")
    ap.add_argument("--rounds", type=int, default=20)
    args = ap.parse_args()
    if not args.engine:
        ap.error("--engine (or LYCAON_DECIDE_ENGINE) must name a bialy launcher")

    corpus = Corpus.load(args.corpus)
    qs = corpus.surface_questions(args.surface)
    engine = Engine(args.engine)
    try:
        # One warm call absorbs model load and allocator warm-up.
        engine.call({"method": "decide", "head": "turn-load", "state": {"host": "coordinator", "user": STATES[0], "surface": args.surface}, "questions": qs})
        samples = []
        for i in range(args.rounds):
            state = {"host": "coordinator", "user": STATES[i % len(STATES)], "root_count": 1, "surface": args.surface}
            started = time.perf_counter()
            engine.call({"method": "decide", "head": "turn-load", "state": state, "questions": qs})
            samples.append((time.perf_counter() - started) * 1000)
        samples.sort()
        report = {
            "engine": engine.hello.get("engine"),
            "surface": args.surface,
            "candidates": len(qs) - 1,
            "rounds": args.rounds,
            "p50_ms": round(statistics.median(samples)),
            "p95_ms": round(samples[min(len(samples) - 1, int(len(samples) * 0.95))]),
            "per_candidate_ms": round(statistics.median(samples) / max(len(qs) - 1, 1), 2),
            "deadline_ms": corpus.spec.get("deadline_ms"),
        }
    finally:
        engine.close()
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
