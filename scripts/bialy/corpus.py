"""The unit corpus the turn decision scores, as the host exports it.

`lycaon-debug decide corpus` is the single source: every coordinator surface's
floor and loadable tools, every tool's option text and request card, every
instruction unit with its front matter, every loaded skill with the card the
engine ranks, the question templates, and the turn kinds. Offline tooling
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

KINDS = ("answer_only", "inspect", "change", "run", "delegate")


class Corpus:
    def __init__(self, data):
        self.data = data
        self.revision = data["catalog_revision"]
        self.spec = data["questions"]
        self.kinds = list(data.get("kinds") or KINDS)
        self.surfaces = {row["id"]: row for row in data["surfaces"]}
        self.tools = {row["name"]: row["description"] for row in data["tools"]}
        self.tool_options = dict(self.spec["tools"].get("options") or {row["name"]: row.get("option", "") for row in data["tools"]})
        self.state_spec = data.get("state", {})
        self.units = {row["id"]: row for row in data["units"]}
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

    def multi_question(self, kind, ids):
        """The host's multi question over candidate ids: every known candidate is an option.
        Option order is the sorted id order the engine reads, as the host's map encodes it."""
        spec = self.spec["tools"] if kind == "tool" else self.spec["guides"]
        if kind == "tool":
            options = {name: self.tool_options[name] for name in ids if name in self.tool_options}
        else:
            options = {uid: self.units[uid].get("option", "") for uid in ids if uid in self.units}
        question = {"type": "multi", "instructions": spec["question"], "options": dict(sorted(options.items()))}
        if self.independent():
            question["independent"] = True
        return question

    def independent(self):
        """Whether the turn's multi questions encode one option per row. The tools
        setting governs every multi question, so tools and guides share one encoding."""
        return bool(self.spec["tools"].get("independent"))

    def questions(self, loadable, guides):
        """The whole turn set the host asks over these candidates: tools and guides, and
        the kind choice for a joint head (an independent head reads no roster)."""
        qs = {}
        for kind, qid, ids in (("tool", "tools", loadable), ("guide", "guides", guides)):
            q = self.multi_question(kind, ids)
            if q["options"]:
                qs[qid] = q
        if not self.independent():
            qs["kind"] = self.kind_question()
        return qs

    def row_questions(self, row):
        """The turn set a training row's turn was asked: the candidates it offered."""
        return self.questions(row["offered"]["loadable"], row["offered"]["guides"])

    def surface_questions(self, surface_id):
        """The turn set of a coordinator surface as the corpus declares it."""
        surface = self.surface(surface_id)
        return self.questions(surface["loadable"], surface["guides"])

    def question_id(self, kind, name):
        return "%s.%s" % (kind, name)

    def kind_question(self):
        return {"type": "choice", "instructions": self.spec["kind"]["instructions"], "options": self.spec["kind"]["options"]}

    def guides_attached(self, tool):
        """Instruction units that follow a tool: needed whenever the tool is."""
        return sorted(uid for uid, u in self.units.items() if tool in (u.get("attaches") or []))

    def unit_pack(self, uid):
        return self.units[uid]["pack_id"]

    def packs(self):
        return sorted({u["pack_id"] for u in self.units.values()})


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
    sys.stderr.write("corpus %s: %d surfaces, %d tools, %d units, %d skills from packs %s\n" % (
        corpus.revision, len(corpus.surfaces), len(corpus.tools), len(corpus.units), len(corpus.skills), ", ".join(corpus.packs())))


if __name__ == "__main__":
    main()
