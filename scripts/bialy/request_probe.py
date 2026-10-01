"""Rank request_tools needs against a surface's loadable tools through the engine.

Each candidate is the text the host sends: "Tool <name>: <first 60 words of the
description>". Use it to see why a request missed, and to try a description
before editing a schema.

Usage:
  request_probe.py --corpus corpus.json --engine "<bialy serve ... --head unit-rank=...>" \
      [--surface implement_investigate] [--override NAME=DESCRIPTION ...] NEED [NEED ...]

corpus.json comes from `lycaon-debug decide corpus`. --override replaces one
tool's description for this probe only.
"""
import argparse
import json
import shlex
import subprocess

CARD_WORDS = 60
CARD_RUNES = 480


def card(name, description):
    words = " ".join(description.split()).split(" ")
    return "Tool %s: %s" % (name, " ".join(words[:CARD_WORDS])[:CARD_RUNES])


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--corpus", required=True)
    ap.add_argument("--engine", required=True, help="the engine command line, quoted")
    ap.add_argument("--surface", default="implement_investigate")
    ap.add_argument("--override", action="append", default=[], help="NAME=DESCRIPTION, repeatable")
    ap.add_argument("--top", type=int, default=6)
    ap.add_argument("needs", nargs="+")
    args = ap.parse_args()
    corpus = json.load(open(args.corpus))
    tools = corpus["tools"]
    tools = tools if isinstance(tools, dict) else {t["name"]: t for t in tools}
    surfaces = corpus["surfaces"]
    surfaces = surfaces if isinstance(surfaces, dict) else {s["id"]: s for s in surfaces}
    roster = surfaces[args.surface]["loadable"]
    overrides = dict(item.split("=", 1) for item in args.override)
    candidates = [card(n, overrides[n]) if n in overrides else tools[n]["card"] for n in roster]
    proc = subprocess.Popen(shlex.split(args.engine), stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)

    def call(msg):
        proc.stdin.write(json.dumps(msg) + "\n")
        proc.stdin.flush()
        return json.loads(proc.stdout.readline())

    hello = call({"id": 1, "method": "hello"})
    print("heads:", sorted((hello.get("heads") or {}).keys()))
    for i, need in enumerate(args.needs, start=2):
        scores = call({"id": i, "method": "rank", "head": "unit-rank", "task": need, "candidates": candidates}).get("scores") or []
        ranked = sorted(zip(roster, scores), key=lambda x: -x[1])
        print("\n== %r" % need)
        for name, score in ranked[:args.top]:
            print("   %-16s %.2f" % (name, score))
    proc.stdin.close()
    proc.wait(timeout=30)


if __name__ == "__main__":
    main()
