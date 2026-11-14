"""The corpus the turn decision scores, as the host exports it.

`lycaon-debug decide corpus` is the single source: every coordinator surface's
floor and loadable tools, every tool's option text and request card, every
loaded skill with the card the engine ranks, the question templates, and the
turn kinds. Offline tooling
reads that JSON instead of parsing the packs, so labels are always the host's
own vocabulary. A training row carries the candidates its turn offered;
`row_questions` asks exactly those.

    corpus.py [--out FILE]          export the corpus through the host binary
    from corpus import Corpus, decide_dir; c = Corpus.load(decide_dir() / "corpus.json")

Offline artifacts live under the checkout's resolved build directory
(scripts/artifact_paths.py build), in its decide/ subdirectory.
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from artifact_paths import build_dir  # noqa: E402


def decide_dir():
    """The checkout's resolved directory for decide corpora, data, heads, and evals."""
    return build_dir(ROOT) / "decide"


DEFAULT_PATH = decide_dir() / "corpus.json"

SCHEMA = "pw-decide-corpus/3"

KINDS = ("answer_only", "inspect", "change", "run", "delegate")


class Corpus:
    def __init__(self, data):
        if data.get("schema") != SCHEMA:
            raise ValueError("corpus schema %r, want %r; export a current corpus" % (data.get("schema"), SCHEMA))
        self.data = data
        self.revision = data["catalog_revision"]
        self.spec = data["questions"]
        self.kinds = list(data.get("kinds") or KINDS)
        self.surfaces = {row["id"]: row for row in data["surfaces"]}
        self.tools = {row["name"]: row["description"] for row in data["tools"]}
        self.tool_options = dict(self.spec["tools"].get("options") or {row["name"]: row.get("option", "") for row in data["tools"]})
        if set(self.tool_options) != set(self.tools):
            raise ValueError("corpus tool options must match the callable tool roster")
        self.state_spec = data.get("state", {})
        self.skills = {row["name"]: row for row in data.get("skills") or []}

    @classmethod
    def load(cls, path=DEFAULT_PATH):
        with open(path, encoding="utf-8") as fh:
            return cls(json.load(fh))

    def surface(self, surface_id):
        row = self.surfaces.get(surface_id)
        if row is None:
            raise KeyError("unknown surface %r; known: %s" % (surface_id, ", ".join(sorted(self.surfaces))))
        return row

    def tool_cards(self):
        """Every tool's card, the exact text the host's engine ranks against a request_tools need."""
        return {row["name"]: row["card"] for row in self.data["tools"] if row.get("card")}

    def skill_cards(self):
        """Every loaded skill's card, the exact text the host's engine ranks."""
        return {name: row["card"] for name, row in self.skills.items()}

    def tools_question(self, ids):
        """The host's tools question over candidate ids: every known tool is an option.
        Option order is the sorted name order the engine reads, as the host's map encodes it."""
        self.require_candidates("tool", ids, self.tool_options)
        options = {name: self.tool_options[name] for name in ids}
        question = {"type": "multi", "instructions": self.spec["tools"]["question"], "options": dict(sorted(options.items()))}
        if self.independent():
            question["independent"] = True
        return question

    @staticmethod
    def require_candidates(field, ids, known):
        unknown = set(ids or ()) - set(known)
        if unknown:
            raise ValueError("%s contains candidates absent from this corpus: %s; use matching data, do not relabel history" % (field, ", ".join(sorted(unknown))))

    def validate_row(self, row):
        """Admit recorded candidates and labels without changing their identities."""
        offered, labels = row["offered"], row["labels"]
        for field in ("floor", "loadable"):
            self.require_candidates("offered." + field, offered[field], self.tools)
        loadable = offered["loadable"]
        for field in ("tools", "tool_scores"):
            self.require_candidates("labels." + field, labels.get(field), loadable)
        for field in ("skills", "skill_scores"):
            self.require_candidates("labels." + field, labels.get(field), self.skills)
        second = labels.get("second_scores") or {}
        self.require_candidates("second_scores.tools", second.get("tools"), loadable)
        self.require_candidates("second_scores.skills", second.get("skills"), self.skills)
        for i, request in enumerate(labels.get("requests") or []):
            for field in ("after", "scores"):
                self.require_candidates("requests[%d].%s" % (i, field), request.get(field), loadable)
        for scores in second.get("requests") or []:
            self.require_candidates("second_scores.requests", scores, loadable)

    def independent(self):
        """Whether the tools question encodes one option per row."""
        return bool(self.spec["tools"].get("independent"))

    def questions(self, loadable):
        """The whole turn set the host asks over these candidates: the tools question, and
        the kind choice for a joint head (an independent head reads no roster)."""
        qs = {}
        q = self.tools_question(loadable)
        if q["options"]:
            qs["tools"] = q
        if not self.independent():
            qs["kind"] = self.kind_question()
        return qs

    def row_questions(self, row):
        """The turn set a training row's turn was asked: the candidates it offered."""
        self.validate_row(row)
        return self.questions(row["offered"]["loadable"])

    def surface_questions(self, surface_id):
        """The turn set of a coordinator surface as the corpus declares it."""
        surface = self.surface(surface_id)
        return self.questions(surface["loadable"])

    def kind_question(self):
        return {"type": "choice", "instructions": self.spec["kind"]["instructions"], "options": self.spec["kind"]["options"]}


def export(out_path):
    """Run the host exporter; the binary comes from BUILD_ONLY=true ./task eval:tool-usage."""
    binary = os.environ.get("LYCAON_DEBUG_BINARY") or str(build_dir(ROOT) / "lycaon-debug")
    if not os.path.exists(binary):
        sys.exit("lycaon-debug not found at %s; build it with BUILD_ONLY=true ./task eval:tool-usage or set LYCAON_DEBUG_BINARY" % binary)
    out_path = Path(out_path)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([binary, "decide", "corpus", "--out", str(out_path)], check=True)
    return out_path


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=str(DEFAULT_PATH))
    args = ap.parse_args()
    path = export(args.out)
    corpus = Corpus.load(path)
    sys.stderr.write("corpus %s: %d surfaces, %d tools, %d skills\n" % (
        corpus.revision, len(corpus.surfaces), len(corpus.tools), len(corpus.skills)))


if __name__ == "__main__":
    main()
