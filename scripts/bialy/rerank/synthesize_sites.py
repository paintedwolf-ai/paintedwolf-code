#!/usr/bin/env python3
"""Write training rows for the candidate shapes the Go dumps do not cover.

The head learns "Task / Candidate" pairs, so every shape a site emits must be
in the training set. `decide-rerank eval --dump` covers file heads (structure),
symbols (definitions, repomap tags, windows) from real site calls. This script
writes the remaining shapes in the same dump format, with the exact candidate
text each site builds:

  lines     "File: path\\nLine N: text"        project search hits and call sites
  neighbors "File: path\\n<why>"               fit-tier neighbour stubs
  imports   "Import (kind): from -> to"        fit-tier import edges
  next      "Next: read path lines=A-B\\n<why>" follow-up calls
  pages     "Page: url\\nTitle: ...\\n..."        verified web pages

Code shapes come from harvested units and the repository files; pages come
from public documentation sites fetched once into a cache. Labels follow the
site's own structure on the engine's 0..4 rubric.

Usage:
  synthesize_sites.py code  --units units.jsonl --pairs pairs.jsonl --repo PATH --out rows.jsonl [--limit N]
  synthesize_sites.py pages --cache .task/decide/pages --out rows.jsonl [--limit N]
"""

import argparse
import hashlib
import html
import json
import os
import random
import re
import sys
import time
import urllib.parse
import urllib.request
from collections import defaultdict
from html.parser import HTMLParser

LINE_CANDIDATES = 24
FILE_CANDIDATES = 24
EXCERPT_RUNES = 200
PROBE_TEXT_RUNES = 400


def read_jsonl(path):
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line:
                yield json.loads(line)


def write_jsonl(path, rows):
    os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")


def bound(text, n):
    text = text.strip()
    return text if len(text) <= n else text[:n]


def row(site, pair, cands):
    return {"site": site, "repo": pair["repo"], "task": pair["task"], "query": pair.get("query", ""),
            "file": pair["file"], "symbol": pair["symbol"], "line": pair["line"], "lang": pair.get("lang", ""),
            "candidates": cands}


def cand(text, file, label, symbol="", line=0):
    # The Go dump carries lexical scores; synthetic rows carry the label the
    # trainer needs and a lexical placeholder that orders the same way.
    return {"text": text, "file": file, "symbol": symbol, "line": line, "lexical": float(label), "target": label == 4, "label": label}


class Corpus:
    def __init__(self, units, root):
        self.root = root
        self.units = units
        self.by_file = defaultdict(list)
        for u in units:
            self.by_file[u["file"]].append(u)
        self.files = sorted(self.by_file)
        self._lines = {}

    def lines(self, rel):
        if rel not in self._lines:
            try:
                with open(os.path.join(self.root, rel), encoding="utf-8", errors="replace") as fh:
                    self._lines[rel] = fh.read().split("\n")
            except OSError:
                self._lines[rel] = []
        return self._lines[rel]

    def doc_of(self, rel):
        for u in self.by_file.get(rel, []):
            if u.get("doc"):
                return u["doc"]
        return ""


def symbol_uses(corpus, symbol, skip_file, rng, limit):
    """Lines in other files that mention the symbol: call sites."""
    pattern = re.compile(r"\b" + re.escape(symbol) + r"\b")
    out = []
    files = corpus.files[:]
    rng.shuffle(files)
    for rel in files:
        if rel == skip_file:
            continue
        for i, text in enumerate(corpus.lines(rel)):
            if pattern.search(text):
                out.append((rel, i + 1, text.strip()))
                break
        if len(out) >= limit:
            break
    return out


def random_lines(corpus, rng, limit, skip_file):
    out = []
    for _ in range(limit * 4):
        rel = rng.choice(corpus.files)
        if rel == skip_file:
            continue
        lines = corpus.lines(rel)
        if not lines:
            continue
        i = rng.randrange(len(lines))
        if lines[i].strip():
            out.append((rel, i + 1, lines[i].strip()))
        if len(out) >= limit:
            break
    return out


def line_text(rel, n, text):
    return "File: %s\nLine %d: %s" % (rel, n, bound(text, EXCERPT_RUNES))


def lines_row(corpus, pair, rng):
    lines = corpus.lines(pair["file"])
    if not lines or pair["line"] > len(lines):
        return None
    definition = lines[pair["line"] - 1].strip()
    cands = [cand(line_text(pair["file"], pair["line"], definition), pair["file"], 4, pair["symbol"], pair["line"])]
    for rel, n, text in symbol_uses(corpus, pair["symbol"], pair["file"], rng, 6):
        cands.append(cand(line_text(rel, n, text), rel, 2, "", n))
    for rel, n, text in random_lines(corpus, rng, LINE_CANDIDATES - len(cands), pair["file"]):
        cands.append(cand(line_text(rel, n, text), rel, 0, "", n))
    rng.shuffle(cands)
    return row("project_search", pair, cands)


def neighbors_row(corpus, pair, rng):
    target_dir = os.path.dirname(pair["file"])
    cands = [cand("File: %s\n%s" % (pair["file"], bound(corpus.doc_of(pair["file"]) or "defines " + pair["symbol"], EXCERPT_RUNES)), pair["file"], 4)]
    same_dir = [f for f in corpus.files if os.path.dirname(f) == target_dir and f != pair["file"]]
    others = [f for f in corpus.files if os.path.dirname(f) != target_dir]
    rng.shuffle(same_dir)
    rng.shuffle(others)
    for f in same_dir[:6]:
        cands.append(cand("File: %s\n%s" % (f, bound(corpus.doc_of(f) or "sibling in " + target_dir, EXCERPT_RUNES)), f, 2))
    for f in others[: FILE_CANDIDATES - len(cands)]:
        cands.append(cand("File: %s\n%s" % (f, bound(corpus.doc_of(f) or "elsewhere in the tree", EXCERPT_RUNES)), f, 0))
    rng.shuffle(cands)
    return row("summarize_neighbors", pair, cands)


IMPORT_RE = re.compile(r'^\s*(?:import|from|use|require|#include|using)\s+["<]?([\w./:\-]+)', re.M)
# Go and similar block imports list one quoted path per line.
IMPORT_BLOCK_RE = re.compile(r'^\s*(?:\w+\s+)?"([\w./\-]+)"\s*$', re.M)


def imports_of(corpus, rel):
    text = "\n".join(corpus.lines(rel)[:80])
    found = [m.group(1) for m in IMPORT_RE.finditer(text)] + [m.group(1) for m in IMPORT_BLOCK_RE.finditer(text)]
    return list(dict.fromkeys(found))[:8]


def imports_row(corpus, pair, rng):
    own = imports_of(corpus, pair["file"])
    if not own:
        return None
    cands = [cand("Import (outbound): %s -> %s" % (pair["file"], to), pair["file"], 3) for to in own]
    others = corpus.files[:]
    rng.shuffle(others)
    for f in others:
        if f == pair["file"]:
            continue
        for to in imports_of(corpus, f)[:2]:
            cands.append(cand("Import (outbound): %s -> %s" % (f, to), f, 0))
        if len(cands) >= FILE_CANDIDATES:
            break
    cands[0]["label"], cands[0]["target"], cands[0]["lexical"] = 4, True, 4.0
    rng.shuffle(cands)
    return row("summarize_imports", pair, cands)


def next_row(corpus, pair, rng):
    def action(u, why, label):
        end = u["line"] + 23
        text = "Next: read %s lines=%d-%d\n%s" % (u["file"], u["line"], end, why)
        return cand(text, u["file"], label, u["symbol"], u["line"])

    unit = next((u for u in corpus.by_file[pair["file"]] if u["symbol"] == pair["symbol"] and u["line"] == pair["line"]), None)
    if unit is None:
        return None
    cands = [action(unit, "read %s for detail" % unit["symbol"], 4)]
    for u in corpus.by_file[pair["file"]]:
        if u is not unit and len(cands) < 6:
            cands.append(action(u, "read %s for detail" % u["symbol"], 2))
    others = [u for u in corpus.units if u["file"] != pair["file"]]
    for u in rng.sample(others, min(len(others), FILE_CANDIDATES - len(cands))):
        cands.append(action(u, "read %s for detail" % u["symbol"], 0))
    rng.shuffle(cands)
    return row("summarize_next_actions", pair, cands)


def code_mode(args):
    rng = random.Random(args.seed)
    units = list(read_jsonl(args.units))
    pairs = list(read_jsonl(args.pairs))
    rng.shuffle(pairs)
    if args.limit:
        pairs = pairs[: args.limit]
    corpus = Corpus(units, args.repo)
    rows = []
    for p in pairs:
        for make in (lines_row, neighbors_row, imports_row, next_row):
            r = make(corpus, p, rng)
            if r:
                rows.append(r)
    write_jsonl(args.out, rows)
    counts = defaultdict(int)
    for r in rows:
        counts[r["site"]] += 1
    print("wrote %d rows to %s: %s" % (len(rows), args.out, dict(counts)), file=sys.stderr)


# --- pages -----------------------------------------------------------------

DOC_INDEXES = [
    ("python", "https://docs.python.org/3/library/index.html", "https://docs.python.org/3/library/"),
    ("rust", "https://doc.rust-lang.org/std/index.html", "https://doc.rust-lang.org/std/"),
    ("go", "https://go.dev/doc/", "https://go.dev/doc/"),
    ("mdn-js", "https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects", "https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/"),
    ("mdn-css", "https://developer.mozilla.org/en-US/docs/Web/CSS/Reference", "https://developer.mozilla.org/en-US/docs/Web/CSS/"),
    ("postgres", "https://www.postgresql.org/docs/current/sql-commands.html", "https://www.postgresql.org/docs/current/sql-"),
    ("git", "https://git-scm.com/docs", "https://git-scm.com/docs/git-"),
    ("kubernetes", "https://kubernetes.io/docs/concepts/", "https://kubernetes.io/docs/concepts/"),
]


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.title = ""
        self.description = ""
        self.headings = []
        self.text = []
        self.links = []
        self._stack = []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        self._stack.append(tag)
        if tag == "meta" and a.get("name") == "description":
            self.description = a.get("content", "")
        if tag == "a" and a.get("href"):
            self.links.append(a["href"])

    def handle_endtag(self, tag):
        if self._stack and self._stack[-1] == tag:
            self._stack.pop()

    def handle_data(self, data):
        if not self._stack:
            return
        tag = self._stack[-1]
        text = data.strip()
        if not text:
            return
        if tag == "title" and not self.title:
            self.title = text
        elif tag in ("h1", "h2", "h3") and len(self.headings) < 8:
            self.headings.append(text)
        elif tag in ("p", "li", "td", "pre", "code") and len(" ".join(self.text)) < 3000:
            self.text.append(text)


def fetch(url, cache):
    key = hashlib.sha1(url.encode()).hexdigest()
    path = os.path.join(cache, key + ".html")
    if os.path.exists(path):
        with open(path, encoding="utf-8", errors="replace") as fh:
            return fh.read()
    req = urllib.request.Request(url, headers={"User-Agent": "painted-wolf-rerank-corpus/1 (+bespoke experiment)"})
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            body = resp.read(600_000).decode("utf-8", errors="replace")
    except Exception as exc:  # noqa: BLE001 - a missing page is just skipped
        print("skip %s: %s" % (url, exc), file=sys.stderr)
        body = ""
    os.makedirs(cache, exist_ok=True)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(body)
    time.sleep(0.4)
    return body


def parse_page(url, body):
    p = Page()
    try:
        p.feed(body)
    except Exception:  # noqa: BLE001
        return None
    title = html.unescape(re.sub(r"\s+", " ", p.title))
    if not title:
        return None
    return {"url": url, "title": title, "headings": [html.unescape(h) for h in p.headings],
            "description": html.unescape(p.description), "sample": html.unescape(" ".join(p.text))[:PROBE_TEXT_RUNES], "links": p.links}


def page_text(pg):
    parts = ["Page: " + pg["url"]]
    if pg["title"]:
        parts.append("Title: " + pg["title"])
    if pg["headings"]:
        parts.append("Headings: " + " | ".join(pg["headings"]))
    if pg["description"]:
        parts.append("Description: " + pg["description"])
    if pg["sample"]:
        parts.append(pg["sample"])
    return "\n".join(parts)


def page_task(pg):
    """A request the page answers: its description without the title's words."""
    words = {w.lower() for w in re.findall(r"[A-Za-z0-9_]+", pg["title"]) if len(w) > 2}
    desc = re.split(r"(?<=[.!?])\s", pg["description"].strip(), maxsplit=1)[0] if pg["description"] else ""
    if len(desc.split()) >= 5 and not ({w.lower() for w in re.findall(r"[A-Za-z0-9_]+", desc)} & words):
        return desc
    return ""


def pages_mode(args):
    rng = random.Random(args.seed)
    pages = []
    for site, index_url, prefix in DOC_INDEXES:
        index = parse_page(index_url, fetch(index_url, args.cache))
        if not index:
            continue
        seen = set()
        for href in index["links"]:
            url = urllib.parse.urljoin(index_url, href).split("#")[0]
            if not url.startswith(prefix) or url == index_url or url in seen:
                continue
            seen.add(url)
            if len(seen) > args.per_site:
                break
            pg = parse_page(url, fetch(url, args.cache))
            if pg:
                pg["site"] = site
                pages.append(pg)
    rows = []
    for pg in pages:
        task = page_task(pg)
        if not task:
            continue
        cands = [cand(page_text(pg), pg["url"], 4)]
        same = [q for q in pages if q["site"] == pg["site"] and q is not pg]
        other = [q for q in pages if q["site"] != pg["site"]]
        for q in rng.sample(same, min(len(same), 5)):
            cands.append(cand(page_text(q), q["url"], 1))
        for q in rng.sample(other, min(len(other), 10)):
            cands.append(cand(page_text(q), q["url"], 0))
        rng.shuffle(cands)
        rows.append({"site": "web_pages", "repo": pg["site"], "task": task, "file": pg["url"], "symbol": pg["title"], "line": 0, "candidates": cands})
        if args.limit and len(rows) >= args.limit:
            break
    write_jsonl(args.out, rows)
    print("fetched %d pages, wrote %d page rows to %s" % (len(pages), len(rows), args.out), file=sys.stderr)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="mode", required=True)
    code = sub.add_parser("code")
    code.add_argument("--units", required=True)
    code.add_argument("--pairs", required=True)
    code.add_argument("--repo", required=True)
    code.add_argument("--out", required=True)
    code.add_argument("--limit", type=int, default=0)
    code.add_argument("--seed", type=int, default=7)
    pages = sub.add_parser("pages")
    pages.add_argument("--cache", required=True)
    pages.add_argument("--out", required=True)
    pages.add_argument("--per-site", type=int, default=80)
    pages.add_argument("--limit", type=int, default=0)
    pages.add_argument("--seed", type=int, default=7)
    args = ap.parse_args()
    if args.mode == "code":
        code_mode(args)
    else:
        pages_mode(args)


if __name__ == "__main__":
    main()
