"""Deterministic seeded directory tree generator and manifest builder (D5, K01)."""

from __future__ import annotations

import base64
from dataclasses import dataclass, field
import fnmatch
import hashlib
import os
from pathlib import Path
import random
from typing import Iterable

from .seeds import ReplaySelector, seed_rng

try:
    import yaml  # type: ignore
except ImportError:  # pragma: no cover - PyYAML is in requirements.txt
    yaml = None

DEFAULT_TEXT_TOKENS = (
    "alpha",
    "beta",
    "gamma",
    "delta",
    "catalog",
    "frontier",
    "watcher",
    "snapshot",
    "search_hit",
    "replace_target",
    "PaintedWolf",
    "order_42",
)

DEFAULT_BOUNDARY_PATTERNS = (
    "build",
    "dist",
    "target",
    "node_modules",
    ".git",
    "__pycache__",
    ".venv",
    ".cache",
)


@dataclass(frozen=True)
class BoundaryConfig:
    """Boundary directory patterns loaded from directory-priority.yaml or a list."""

    boundary_patterns: tuple[str, ...]
    non_collapsing_patterns: tuple[str, ...] = ()

    @classmethod
    def from_yaml_file(cls, path: Path) -> "BoundaryConfig":
        raw = path.read_text(encoding="utf-8")
        if yaml is not None:
            data = yaml.safe_load(raw) or {}
            groups = data.get("groups", [])
        else:  # pragma: no cover - fallback if PyYAML unavailable
            groups = _parse_directory_priority_fallback(raw)
        boundary: list[str] = []
        non_collapsing: list[str] = []
        for group in groups:
            patterns = [str(p) for p in group.get("patterns", [])]
            if group.get("collapse") is False:
                non_collapsing.extend(patterns)
            elif group.get("tier") == "boundary":
                boundary.extend(patterns)
        non_collapse_set = set(non_collapsing)
        filtered = tuple(dict.fromkeys(p for p in boundary if p not in non_collapse_set))
        return cls(
            boundary_patterns=filtered,
            non_collapsing_patterns=tuple(dict.fromkeys(non_collapsing)),
        )

    def is_boundary_dir(self, rel_posix_path: str) -> bool:
        """Return True if rel_posix_path matches any boundary pattern at any depth."""
        parts = [p for p in rel_posix_path.strip("/").split("/") if p]
        if not parts:
            return False
        basename = parts[-1]
        for pattern in self.boundary_patterns:
            if "/" in pattern:
                pat_parts = pattern.split("/")
                if len(parts) >= len(pat_parts):
                    tail = parts[-len(pat_parts) :]
                    if all(fnmatch.fnmatchcase(seg, pseg) for seg, pseg in zip(tail, pat_parts)):
                        return True
            elif fnmatch.fnmatchcase(basename, pattern):
                return True
        return False


def _parse_directory_priority_fallback(raw: str) -> list[dict[str, object]]:  # pragma: no cover
    groups: list[dict[str, object]] = []
    current: dict[str, object] | None = None
    in_patterns = False
    for line in raw.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped.startswith("- id:"):
            current = {"patterns": []}
            groups.append(current)
            in_patterns = False
        elif current is not None:
            if stripped.startswith("tier:"):
                current["tier"] = stripped.split(":", 1)[1].strip()
                in_patterns = False
            elif stripped.startswith("collapse:"):
                current["collapse"] = stripped.split(":", 1)[1].strip().lower() != "false"
                in_patterns = False
            elif stripped.startswith("patterns:"):
                rest = stripped.split(":", 1)[1].strip()
                if rest.startswith("[") and rest.endswith("]"):
                    items = [x.strip().strip("'\"") for x in rest[1:-1].split(",") if x.strip()]
                    current["patterns"] = items
                    in_patterns = False
                else:
                    in_patterns = True
            elif in_patterns and stripped.startswith("- "):
                current["patterns"].append(stripped[2:].strip().strip("'\""))
            else:
                in_patterns = False
    return groups


@dataclass(frozen=True)
class TreeEntrySpec:
    """One deterministic entry in a generated tree."""

    path: str
    kind: str  # "file" | "dir" | "symlink"
    content: bytes = b""
    link_target: str | None = None
    mode: int | None = None

    def to_dict(self) -> dict[str, object]:
        data: dict[str, object] = {"path": self.path, "kind": self.kind}
        if self.kind == "file":
            data["content_b64"] = base64.b64encode(self.content).decode("ascii")
        if self.link_target is not None:
            data["link_target"] = self.link_target
        if self.mode is not None:
            data["mode"] = self.mode
        return data

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "TreeEntrySpec":
        raw_b64 = data.get("content_b64")
        content = base64.b64decode(str(raw_b64)) if raw_b64 else b""
        return cls(
            path=str(data["path"]),
            kind=str(data["kind"]),
            content=content,
            link_target=str(data["link_target"]) if data.get("link_target") is not None else None,
            mode=int(data["mode"]) if data.get("mode") is not None else None,
        )


@dataclass(frozen=True)
class TreeSpec:
    """Complete serializable specification for a generated directory tree."""

    selector: ReplaySelector
    entries: tuple[TreeEntrySpec, ...]

    def file_paths(self) -> list[str]:
        return sorted(e.path for e in self.entries if e.kind == "file")

    def to_dict(self) -> dict[str, object]:
        return {
            "selector": self.selector.to_dict(),
            "entries": [e.to_dict() for e in self.entries],
        }

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "TreeSpec":
        return cls(
            selector=ReplaySelector.from_dict(dict(data["selector"])),  # type: ignore[arg-type]
            entries=tuple(TreeEntrySpec.from_dict(dict(e)) for e in data["entries"]),  # type: ignore[union-attr]
        )


@dataclass(frozen=True)
class ManifestEntry:
    """One materialized entry in a deterministic tree manifest."""

    path: str
    kind: str  # "file" | "dir" | "symlink"
    size: int
    sha256: str | None = None
    link_target: str | None = None

    def to_dict(self) -> dict[str, object]:
        return {
            "path": self.path,
            "kind": self.kind,
            "size": self.size,
            "sha256": self.sha256,
            "link_target": self.link_target,
        }


@dataclass(frozen=True)
class TreeManifest:
    """Deterministic manifest emitted on tree materialization."""

    selector: ReplaySelector
    entries: tuple[ManifestEntry, ...]

    def by_path(self) -> dict[str, ManifestEntry]:
        return {entry.path: entry for entry in self.entries}

    def digest(self) -> str:
        hasher = hashlib.sha256()
        for entry in self.entries:
            line = f"{entry.path}|{entry.kind}|{entry.size}|{entry.sha256 or ''}|{entry.link_target or ''}\n"
            hasher.update(line.encode("utf-8"))
        return hasher.hexdigest()

    def to_dict(self) -> dict[str, object]:
        return {
            "selector": self.selector.to_dict(),
            "manifest_digest": self.digest(),
            "entries": [e.to_dict() for e in self.entries],
        }


@dataclass(frozen=True)
class TreeGenOptions:
    """Configurable knobs for seeded tree generation."""

    entry_count: int = 24
    max_depth: int = 4
    include_hidden: bool = True
    include_ignored: bool = True
    include_empty_dirs: bool = True
    include_vcs: bool = True
    include_boundaries: bool = True
    include_nested_checkouts: bool = True
    include_paintedwolf: bool = True
    include_symlinks: bool = False
    include_binary: bool = True
    include_mixed_line_endings: bool = True
    boundary_patterns: tuple[str, ...] = DEFAULT_BOUNDARY_PATTERNS
    corpus_tokens: tuple[str, ...] = DEFAULT_TEXT_TOKENS


def _make_text_body(rng: random.Random, tokens: tuple[str, ...], crlf: bool) -> bytes:
    line_count = rng.randint(2, 6)
    lines: list[str] = []
    for idx in range(line_count):
        chosen = [rng.choice(tokens) for _ in range(rng.randint(2, 4))]
        lines.append(f"line_{idx}: {' '.join(chosen)}")
    sep = "\r\n" if crlf else "\n"
    return (sep.join(lines) + sep).encode("utf-8")


def _pick_dir_chain(rng: random.Random, max_depth: int) -> str:
    depth = rng.randint(0, max(0, max_depth - 1))
    if depth == 0:
        return ""
    roots = ("src", "pkg", "lib", "docs", "internal", "cmd", "tests")
    parts = [rng.choice(roots)]
    for level in range(1, depth):
        parts.append(f"mod_{level}_{rng.randint(0, 3)}")
    return "/".join(parts)


def generate_tree_spec(
    base_seed: str,
    family_id: str,
    instance_index: int,
    options: TreeGenOptions | None = None,
) -> TreeSpec:
    """Generate a deterministic TreeSpec from (base_seed, family_id, instance_index)."""
    opts = options or TreeGenOptions()
    selector = ReplaySelector(base_seed=base_seed, family_id=family_id, instance_index=instance_index)
    rng = seed_rng(base_seed, family_id, instance_index)
    entries_by_path: dict[str, TreeEntrySpec] = {}

    def add(entry: TreeEntrySpec) -> None:
        if entry.path not in entries_by_path:
            entries_by_path[entry.path] = entry

    if opts.include_vcs:
        add(TreeEntrySpec(".git/HEAD", "file", b"ref: refs/heads/main\n"))
        add(TreeEntrySpec(".git/config", "file", b"[core]\n\trepositoryformatversion = 0\n"))

    if opts.include_ignored:
        add(TreeEntrySpec(".gitignore", "file", b"*.ignored\nignored_dir/\n"))
        add(TreeEntrySpec("scratch.ignored", "file", _make_text_body(rng, opts.corpus_tokens, False)))
        add(TreeEntrySpec("ignored_dir/cache.txt", "file", _make_text_body(rng, opts.corpus_tokens, False)))

    if opts.include_hidden:
        add(TreeEntrySpec(".hidden_note.txt", "file", _make_text_body(rng, opts.corpus_tokens, False)))
        add(TreeEntrySpec(".github/workflows/ci.yml", "file", b"name: ci\non: [push]\n"))

    if opts.include_paintedwolf:
        add(TreeEntrySpec(".paintedwolf/source-scope.yaml", "file", b"exclude:\n  - secrets/**\n"))
        add(TreeEntrySpec(".paintedwolf/notes.md", "file", _make_text_body(rng, opts.corpus_tokens, False)))

    if opts.include_empty_dirs:
        add(TreeEntrySpec("empty_dir", "dir"))
        add(TreeEntrySpec("src/empty_nested", "dir"))

    if opts.include_boundaries and opts.boundary_patterns:
        simple_boundaries = [p for p in opts.boundary_patterns if "*" not in p and "[" not in p and p != ".git"]
        chosen_boundaries = simple_boundaries[:2] if simple_boundaries else ["node_modules"]
        for bname in chosen_boundaries:
            add(TreeEntrySpec(f"{bname}/artifact.txt", "file", _make_text_body(rng, opts.corpus_tokens, False)))

    if opts.include_nested_checkouts:
        add(TreeEntrySpec("subrepo/.git/HEAD", "file", b"ref: refs/heads/dev\n"))
        add(TreeEntrySpec("subrepo/readme.md", "file", _make_text_body(rng, opts.corpus_tokens, False)))

    if opts.include_binary:
        payload = bytes([0x00, 0x01, 0x02, 0xFF, 0xFE, 0x89, 0x50, 0x4E, 0x47]) + bytes(
            rng.randint(0, 255) for _ in range(24)
        )
        add(TreeEntrySpec("assets/blob.bin", "file", payload))

    if opts.include_mixed_line_endings:
        add(TreeEntrySpec("src/crlf_sample.txt", "file", _make_text_body(rng, opts.corpus_tokens, True)))

    extensions = (".txt", ".go", ".py", ".ts", ".md")
    counter = 0
    while len(entries_by_path) < max(1, opts.entry_count):
        folder = _pick_dir_chain(rng, opts.max_depth)
        ext = rng.choice(extensions)
        name = f"file_{counter:04d}{ext}"
        rel_path = f"{folder}/{name}" if folder else name
        crlf = opts.include_mixed_line_endings and (counter % 5 == 0)
        add(TreeEntrySpec(rel_path, "file", _make_text_body(rng, opts.corpus_tokens, crlf)))
        counter += 1

    if opts.include_symlinks:
        regular_files = sorted(p for p, e in entries_by_path.items() if e.kind == "file" and "/" not in p)
        target = regular_files[0] if regular_files else "src/crlf_sample.txt"
        add(TreeEntrySpec("link_to_file", "symlink", link_target=target))
        add(TreeEntrySpec("link_to_dir", "symlink", link_target="src"))

    sorted_entries = tuple(entries_by_path[p] for p in sorted(entries_by_path))
    return TreeSpec(selector=selector, entries=sorted_entries)


def materialize_tree(spec: TreeSpec, target_root: Path) -> TreeManifest:
    """Materialize spec under target_root and return a deterministic TreeManifest."""
    root = Path(target_root)
    root.mkdir(parents=True, exist_ok=True)
    created_dirs: set[str] = set()

    for entry in spec.entries:
        rel = Path(entry.path)
        full = root / rel
        if entry.kind == "dir":
            full.mkdir(parents=True, exist_ok=True)
            _record_parent_dirs(rel, created_dirs, include_self=True)
        elif entry.kind == "file":
            full.parent.mkdir(parents=True, exist_ok=True)
            _record_parent_dirs(rel, created_dirs, include_self=False)
            full.write_bytes(entry.content)
            if entry.mode is not None:
                os.chmod(full, entry.mode)
        elif entry.kind == "symlink":
            if entry.link_target is None:
                raise ValueError(f"symlink entry {entry.path} requires link_target")
            full.parent.mkdir(parents=True, exist_ok=True)
            _record_parent_dirs(rel, created_dirs, include_self=False)
            if full.exists() or full.is_symlink():
                full.unlink()
            os.symlink(entry.link_target, full)
        else:
            raise ValueError(f"unsupported entry kind: {entry.kind}")

    manifest_entries: list[ManifestEntry] = []
    for dpath in sorted(created_dirs):
        manifest_entries.append(ManifestEntry(path=dpath, kind="dir", size=0, sha256=None))

    for entry in spec.entries:
        if entry.kind == "dir":
            continue
        full = root / Path(entry.path)
        if entry.kind == "file":
            raw = full.read_bytes()
            digest = hashlib.sha256(raw).hexdigest()
            manifest_entries.append(
                ManifestEntry(path=entry.path, kind="file", size=len(raw), sha256=digest)
            )
        elif entry.kind == "symlink":
            target = os.readlink(full)
            manifest_entries.append(
                ManifestEntry(
                    path=entry.path,
                    kind="symlink",
                    size=len(target.encode("utf-8")),
                    sha256=None,
                    link_target=target,
                )
            )

    manifest_entries.sort(key=lambda e: (e.path, e.kind))
    return TreeManifest(selector=spec.selector, entries=tuple(manifest_entries))


def _record_parent_dirs(rel: Path, acc: set[str], include_self: bool) -> None:
    parts = rel.parts
    limit = len(parts) if include_self else len(parts) - 1
    for i in range(1, limit + 1):
        acc.add("/".join(parts[:i]))


def load_repo_boundary_config(repo_root: Path) -> BoundaryConfig:
    """Load boundary rules from lycaon/config/runtime/source/directory-priority.yaml."""
    yaml_path = repo_root / "lycaon" / "config" / "runtime" / "source" / "directory-priority.yaml"
    return BoundaryConfig.from_yaml_file(yaml_path)


def filter_entry_paths(entries: Iterable[TreeEntrySpec], predicate) -> tuple[TreeEntrySpec, ...]:
    """Return entries matching predicate, preserving deterministic path order."""
    return tuple(e for e in entries if predicate(e))
