"""Deterministic corpus, drift, and expression generators for CF08 (R01-R04)."""

from __future__ import annotations

from dataclasses import dataclass, field
import hashlib
from pathlib import Path
import random

from ..generators import DEFAULT_TEXT_TOKENS, TreeEntrySpec, TreeSpec
from ..queries import ReplaceSpec
from ..references import SUPPORTED_REPLACE_ENCODINGS
from ..seeds import ReplaySelector, seed_rng


def sha256_bytes(raw: bytes) -> str:
    return hashlib.sha256(raw).hexdigest()


def encode_text_with_bom(text: str, encoding: str) -> bytes:
    """Encode text into one of SUPPORTED_REPLACE_ENCODINGS with exact BOM."""
    if encoding == "utf-8":
        return text.encode("utf-8")
    if encoding == "utf-8-bom":
        return b"\xef\xbb\xbf" + text.encode("utf-8")
    if encoding == "utf-16-le-bom":
        return b"\xff\xfe" + text.encode("utf-16-le")
    if encoding == "utf-16-be-bom":
        return b"\xfe\xff" + text.encode("utf-16-be")
    raise ValueError(f"unsupported encoding: {encoding}")


@dataclass(frozen=True)
class DriftAction:
    """One deterministic drift action executed between preview and apply (R02)."""

    writer: str  # "person" | "external" | "agent"
    kind: str  # "edit" | "delete" | "delete_recreate" | "create_post_preview"
    rel_path: str
    content: bytes = b""


@dataclass
class FamilySummary:
    """Per-family ledger counters, declared-gap rates, and failure records."""

    family: str
    lane: str
    base_seed: str
    planned: int
    edges_planned: int
    executed: int = 0
    passed: int = 0
    failed: int = 0
    blocked: int = 0
    inconclusive: int = 0
    edges_executed: int = 0
    edges_passed: int = 0
    edges_failed: int = 0
    declared_gaps: dict[str, int] = field(default_factory=dict)
    failures: list[dict[str, object]] = field(default_factory=list)
    shrunk_failures: list[dict[str, object]] = field(default_factory=list)

    def record_gap(self, reason: str) -> None:
        self.declared_gaps[reason] = self.declared_gaps.get(reason, 0) + 1

    def to_dict(self) -> dict[str, object]:
        denom = max(1, self.executed + self.edges_executed)
        gap_rates = {k: round(v / denom, 6) for k, v in sorted(self.declared_gaps.items())}
        return {
            "family": self.family,
            "lane": self.lane,
            "base_seed": self.base_seed,
            "planned": self.planned,
            "executed": self.executed,
            "passed": self.passed,
            "failed": self.failed,
            "blocked": self.blocked,
            "inconclusive": self.inconclusive,
            "edges_planned": self.edges_planned,
            "edges_executed": self.edges_executed,
            "edges_passed": self.edges_passed,
            "edges_failed": self.edges_failed,
            "declared_gaps": dict(sorted(self.declared_gaps.items())),
            "declared_gap_rates": gap_rates,
            "failures": self.failures[:20],
            "shrunk_failures": self.shrunk_failures[:10],
        }


def generate_r02_case(
    base_seed: str,
    instance_index: int,
) -> tuple[TreeSpec, ReplaceSpec, tuple[DriftAction, ...]]:
    """Generate an R02 corpus, literal replace spec, and 1-20 drift actions."""
    rng = seed_rng(base_seed, "R02", instance_index)
    tok = rng.choice(DEFAULT_TEXT_TOKENS)
    spec = ReplaceSpec(
        pattern=tok,
        replacement=f"{tok}_applied",
        regex=False,
        case_sensitive=True,
        whole_word=False,
    )
    drift_count = rng.randint(1, 20)
    file_count = max(4, min(14, drift_count + 2))
    entries: list[TreeEntrySpec] = []
    paths: list[str] = []
    for idx in range(file_count):
        rel = f"pkg/file_{idx:02d}.txt"
        paths.append(rel)
        body = f"line 1: {tok}_a\nline 2: {tok}_b\n".encode("utf-8")
        entries.append(TreeEntrySpec(rel, "file", body))

    writers = ("person", "external", "agent")
    kinds = ("edit", "delete", "delete_recreate", "create_post_preview")
    drifts: list[DriftAction] = []
    # Reserve last path so at least one healthy file always applies.
    mutable_paths = paths[:-1]
    for step in range(drift_count):
        writer = rng.choice(writers)
        kind = rng.choice(kinds)
        if kind == "create_post_preview":
            rel = f"pkg/post_preview_{step:02d}.txt"
            body = f"post preview by {writer} step {step}: {tok}\n".encode("utf-8")
        else:
            rel = rng.choice(mutable_paths)
            body = f"drifted ({kind}) by {writer} step {step}: {tok}\n".encode("utf-8")
        drifts.append(DriftAction(writer=writer, kind=kind, rel_path=rel, content=body))

    selector = ReplaySelector(base_seed=base_seed, family_id="R02", instance_index=instance_index)
    return TreeSpec(selector=selector, entries=tuple(entries)), spec, tuple(drifts)


def generate_r03_case(
    base_seed: str,
    instance_index: int,
) -> tuple[TreeSpec, ReplaceSpec]:
    """Generate an R03 corpus (1-200 files, mixed EOL, 4 encodings) and ReplaceSpec."""
    rng = seed_rng(base_seed, "R03", instance_index)
    # Span 1..200 files while keeping 200 sidecar instances fast and thorough.
    if instance_index % 50 == 0:
        file_count = 60 + rng.randint(0, 140)
    elif instance_index % 15 == 0:
        file_count = 12 + rng.randint(0, 20)
    else:
        file_count = rng.randint(1, 5)

    tok = rng.choice(DEFAULT_TEXT_TOKENS)
    regex_mode = rng.choice((True, False))
    case_sensitive = rng.choice((True, False))
    whole_word = (not regex_mode) and rng.choice((True, False))

    if regex_mode:
        pattern = f"({tok})_([0-9]{{1,2}})"
        replacement = rng.choice(("rewritten_${2}_$1", "${1}-${2}-$$", "v$2_${1}"))
    else:
        pattern = tok
        replacement = rng.choice((f"{tok}_$1_${{2}}_replaced", f"{tok}_replaced"))

    entries: list[TreeEntrySpec] = []
    for f_idx in range(file_count):
        enc = rng.choice(SUPPORTED_REPLACE_ENCODINGS)
        base_eol = "\r\n" if rng.randint(0, 2) == 0 else "\n"
        lines: list[str] = []
        for l_idx in range(rng.randint(2, 4)):
            eol = base_eol
            if l_idx % 2 == 1 and rng.randint(0, 3) == 0:
                eol = "\n" if base_eol == "\r\n" else "\r\n"
            t_cur = tok.upper() if l_idx % 2 == 1 else tok
            lines.append(f"row_{l_idx}: {t_cur}_{l_idx + 1} pre{tok} {tok}.{eol}")
        rel = f"d{f_idx % 4}/f_{f_idx:03d}.txt"
        entries.append(TreeEntrySpec(rel, "file", encode_text_with_bom("".join(lines), enc)))

    selector = ReplaySelector(base_seed=base_seed, family_id="R03", instance_index=instance_index)
    spec = ReplaceSpec(
        pattern=pattern,
        replacement=replacement,
        regex=regex_mode,
        case_sensitive=case_sensitive,
        whole_word=whole_word,
    )
    return TreeSpec(selector=selector, entries=tuple(entries)), spec


def generate_r04_case(
    base_seed: str,
    instance_index: int,
) -> tuple[TreeSpec, ReplaceSpec, str, str]:
    """Generate an R04 corpus (1-20 files), replace spec, open-draft path, and draft content."""
    rng = seed_rng(base_seed, "R04", instance_index)
    tok = rng.choice(DEFAULT_TEXT_TOKENS)
    file_count = rng.randint(2, 12 if instance_index % 10 != 0 else 20)
    entries: list[TreeEntrySpec] = []
    for idx in range(file_count):
        rel = f"src/doc_{idx:02d}.txt"
        body = f"header line {idx}\nbody {tok} value {idx}\nfooter line {idx}\n".encode("utf-8")
        entries.append(TreeEntrySpec(rel, "file", body))

    open_path = "src/doc_00.txt"
    draft_content = f"header line 0 [unsaved draft {instance_index}]\nbody {tok} value 0\nfooter line 0\n"
    spec = ReplaceSpec(
        pattern=tok,
        replacement=f"{tok}_v2",
        regex=False,
        case_sensitive=True,
        whole_word=False,
    )
    selector = ReplaySelector(base_seed=base_seed, family_id="R04", instance_index=instance_index)
    return TreeSpec(selector=selector, entries=tuple(entries)), spec, open_path, draft_content


def collect_disk_hashes(root_dir: Path, rel_paths: list[str]) -> dict[str, str]:
    """Read raw on-disk bytes independently and return {rel_path: sha256}."""
    out: dict[str, str] = {}
    for rel in rel_paths:
        full = root_dir / rel
        if full.is_file():
            out[rel] = sha256_bytes(full.read_bytes())
    return out
