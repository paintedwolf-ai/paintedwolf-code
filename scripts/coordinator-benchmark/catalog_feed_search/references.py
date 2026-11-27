"""Independent D3 references: walk, ripgrep 15.x search, byte-splicing replace, S04 lookup.

D3 strictly forbids importing product code or running the product's bundled search engine.
"""

from __future__ import annotations

from dataclasses import dataclass
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

from .generators import BoundaryConfig
from .queries import CodeQuerySpec, ReplaceSpec

# Named, encoded differences between raw ripgrep and the documented product contract (D3).
PRODUCT_DIFFERENCES: dict[str, str] = {
    "hidden_files_default_excluded": (
        "Product code leg excludes hidden files/directories by default unless an explicit "
        "positive path filter or include glob names them; reference ripgrep runs with "
        "--no-ignore and enables --hidden only when the query explicitly targets hidden paths."
    ),
    "boundary_trees_excluded_unless_named": (
        "Product content scans skip dependency/build boundary trees unless explicitly named by "
        "a path filter or include glob; reference search applies equivalent --glob exclusions "
        "when boundary_patterns are provided and not explicitly targeted."
    ),
    "regex_engine_re2_shared_subset": (
        "Product uses Go RE2 semantics; ripgrep uses Rust regex. Generated regexes stay inside "
        "the D3 shared subset (literals, char classes, alternation, anchors, bounded repetition, "
        "numbered groups)."
    ),
    "crlf_line_ending_preservation": (
        "Reference replacement splices matched byte spans directly and preserves each line's "
        "existing LF or CRLF line ending."
    ),
}


@dataclass(frozen=True)
class WalkEntry:
    """One entry returned by the independent Python reference walk."""

    path: str
    kind: str  # "file" | "dir" | "symlink"
    size: int
    sha256: str | None = None
    hidden: bool = False
    boundary: bool = False
    link_target: str | None = None

    def to_dict(self) -> dict[str, object]:
        return {
            "path": self.path,
            "kind": self.kind,
            "size": self.size,
            "sha256": self.sha256,
            "hidden": self.hidden,
            "boundary": self.boundary,
            "link_target": self.link_target,
        }


def _is_hidden_path(rel_posix: str) -> bool:
    return any(part.startswith(".") for part in rel_posix.split("/") if part)


def _matches_any_glob(rel_posix: str, globs: tuple[str, ...]) -> bool:
    for pattern in globs:
        if fnmatch.fnmatchcase(rel_posix, pattern) or fnmatch.fnmatchcase(Path(rel_posix).name, pattern):
            return True
        if pattern.endswith("/**"):
            prefix = pattern[:-3].rstrip("/")
            if rel_posix == prefix or rel_posix.startswith(prefix + "/"):
                return True
    return False


def reference_walk(
    root: Path,
    *,
    boundary_config: BoundaryConfig | None = None,
    include_hidden: bool = True,
    agent_exclusions: tuple[str, ...] = (),
    prune_nested_checkouts: bool = True,
) -> list[WalkEntry]:
    """Plain recursive Python directory walk (D3): never follows symlinks.

    When boundary_config is provided, matching boundary directories are recorded
    with boundary=True and not descended (K01 contract).
    """
    root_path = Path(root)
    results: list[WalkEntry] = []

    def _walk(current: Path, rel_prefix: str) -> None:
        with os.scandir(current) as it:
            entries = sorted(it, key=lambda e: e.name)
        for dirent in entries:
            rel = f"{rel_prefix}/{dirent.name}" if rel_prefix else dirent.name
            hidden = _is_hidden_path(rel)
            if not include_hidden and hidden:
                continue
            if agent_exclusions and _matches_any_glob(rel, agent_exclusions):
                continue

            st = os.lstat(dirent.path)
            if stat.S_ISLNK(st.st_mode):
                target = os.readlink(dirent.path)
                results.append(
                    WalkEntry(
                        path=rel,
                        kind="symlink",
                        size=len(target.encode("utf-8")),
                        sha256=None,
                        hidden=hidden,
                        boundary=False,
                        link_target=target,
                    )
                )
            elif stat.S_ISDIR(st.st_mode):
                is_boundary = bool(boundary_config and boundary_config.is_boundary_dir(rel))
                is_nested_checkout = (
                    prune_nested_checkouts
                    and rel != ".git"
                    and (Path(dirent.path) / ".git").exists()
                )
                boundary_flag = is_boundary or is_nested_checkout
                results.append(
                    WalkEntry(
                        path=rel,
                        kind="dir",
                        size=0,
                        sha256=None,
                        hidden=hidden,
                        boundary=boundary_flag,
                    )
                )
                if not boundary_flag:
                    _walk(Path(dirent.path), rel)
            elif stat.S_ISREG(st.st_mode):
                raw = Path(dirent.path).read_bytes()
                results.append(
                    WalkEntry(
                        path=rel,
                        kind="file",
                        size=len(raw),
                        sha256=hashlib.sha256(raw).hexdigest(),
                        hidden=hidden,
                        boundary=False,
                    )
                )

    _walk(root_path, "")
    results.sort(key=lambda e: (e.path, e.kind))
    return results


@dataclass(frozen=True)
class SearchHit:
    """One exact match returned by the ripgrep reference search."""

    path: str
    line_number: int
    column: int  # 1-based byte column within the line
    byte_offset: int  # 0-based byte offset from start of file
    byte_end: int  # 0-based exclusive byte end from start of file
    match_text: str
    line_text: str

    def identity_key(self) -> tuple[str, int, int, int, str]:
        return (self.path, self.line_number, self.byte_offset, self.byte_end, self.match_text)

    def to_dict(self) -> dict[str, object]:
        return {
            "path": self.path,
            "line_number": self.line_number,
            "column": self.column,
            "byte_offset": self.byte_offset,
            "byte_end": self.byte_end,
            "match_text": self.match_text,
            "line_text": self.line_text,
        }


@dataclass(frozen=True)
class SearchReferenceResult:
    """Complete evidence bundle from external ripgrep reference search."""

    rg_version: str
    argv: tuple[str, ...]
    hits: tuple[SearchHit, ...]
    applied_differences: tuple[str, ...]

    def matched_files(self) -> set[str]:
        return {h.path for h in self.hits}


def capture_rg_version(rg_bin: str = "rg") -> str:
    """Capture the exact ripgrep version header string for run evidence (D3)."""
    proc = subprocess.run(
        [rg_bin, "--version"],
        capture_output=True,
        text=True,
        check=True,
    )
    first_line = proc.stdout.splitlines()[0].strip() if proc.stdout else ""
    return first_line


def _query_targets_hidden(query: CodeQuerySpec) -> bool:
    for item in (*query.include_globs, *query.path_filters):
        if item.startswith(".") or "/." in item:
            return True
    return False


def _matches_path_filters(rel_posix: str, path_filters: tuple[str, ...]) -> bool:
    if not path_filters:
        return True
    lowered = rel_posix.lower()
    for pf in path_filters:
        p = pf.lower()
        if p.endswith("/"):
            if lowered.startswith(p):
                return True
        elif "*" in p:
            if fnmatch.fnmatchcase(lowered, p) or fnmatch.fnmatchcase(Path(lowered).name, p):
                return True
        else:
            if lowered == p or lowered.startswith(p + "/") or Path(lowered).name == p:
                return True
    return False


def reference_search(
    root: Path,
    query: CodeQuerySpec,
    *,
    rg_bin: str = "rg",
    include_hidden: bool | None = None,
    boundary_patterns: tuple[str, ...] = (),
) -> SearchReferenceResult:
    """Run external ripgrep 15.x (--json) outside the product and parse exact byte-range hits."""
    rg_version = capture_rg_version(rg_bin)
    applied_diffs: list[str] = ["regex_engine_re2_shared_subset"]

    effective_hidden = include_hidden if include_hidden is not None else _query_targets_hidden(query)
    if not effective_hidden:
        applied_diffs.append("hidden_files_default_excluded")

    argv: list[str] = [
        rg_bin,
        "--json",
        "--no-ignore",
        "--color",
        "never",
        "--sort",
        "path",
    ]
    if effective_hidden:
        argv.append("--hidden")
    if not query.regex:
        argv.append("--fixed-strings")
    argv.append("--case-sensitive" if query.case_sensitive else "--ignore-case")
    if query.whole_word:
        argv.append("--word-regexp")

    for glob in query.include_globs:
        argv.extend(["--glob", glob])
    for glob in query.exclude_globs:
        neg = glob if glob.startswith("!") else f"!{glob}"
        argv.extend(["--glob", neg])

    if boundary_patterns:
        applied_diffs.append("boundary_trees_excluded_unless_named")
        explicit_targets = (*query.include_globs, *query.path_filters)
        for pat in boundary_patterns:
            if any(pat in t for t in explicit_targets):
                continue
            argv.extend(["--glob", f"!{pat}/**"])

    argv.extend(["--", query.pattern, "."])

    proc = subprocess.run(
        argv,
        cwd=str(root),
        capture_output=True,
        text=True,
    )
    # ripgrep exit codes: 0 = matches found, 1 = no matches, 2 = error
    if proc.returncode not in (0, 1):
        raise RuntimeError(f"ripgrep failed (exit {proc.returncode}): {proc.stderr.strip()}")

    hits: list[SearchHit] = []
    for raw_line in proc.stdout.splitlines():
        if not raw_line.strip():
            continue
        event = json.loads(raw_line)
        if event.get("type") != "match":
            continue
        data = event["data"]
        rel_path = str(data["path"]["text"]).removeprefix("./").replace("\\", "/")
        if not _matches_path_filters(rel_path, query.path_filters):
            continue
        line_number = int(data["line_number"])
        abs_line_offset = int(data["absolute_offset"])
        line_text = str(data["lines"].get("text", ""))
        for sub in data.get("submatches", ()):
            start = int(sub["start"])
            end = int(sub["end"])
            match_text = str(sub["match"].get("text", ""))
            hits.append(
                SearchHit(
                    path=rel_path,
                    line_number=line_number,
                    column=start + 1,
                    byte_offset=abs_line_offset + start,
                    byte_end=abs_line_offset + end,
                    match_text=match_text,
                    line_text=line_text,
                )
            )

    hits.sort(key=lambda h: (h.path, h.byte_offset, h.byte_end))
    return SearchReferenceResult(
        rg_version=rg_version,
        argv=tuple(argv),
        hits=tuple(hits),
        applied_differences=tuple(applied_diffs),
    )


@dataclass(frozen=True)
class ReplaceFileResult:
    """Independent byte-spliced replacement result for one file."""

    path: str
    before_sha256: str
    after_sha256: str
    resulting_bytes: bytes
    match_count: int
    changed: bool


def _expand_replacement_bytes(
    replacement: str,
    match_bytes: bytes,
    compiled: re.Pattern[bytes] | None,
) -> bytes:
    """Expand numbered capture groups ($1, $2, ..., $0) and escaped $$ into bytes."""
    if compiled is None:
        return replacement.encode("utf-8")
    m = compiled.fullmatch(match_bytes)
    if m is None:
        m = compiled.search(match_bytes)

    out: list[bytes] = []
    i = 0
    rep = replacement
    while i < len(rep):
        ch = rep[i]
        if ch == "$" and i + 1 < len(rep):
            nxt = rep[i + 1]
            if nxt == "$":
                out.append(b"$")
                i += 2
                continue
            if nxt.isdigit():
                j = i + 1
                while j < len(rep) and rep[j].isdigit():
                    j += 1
                group_idx = int(rep[i + 1 : j])
                if m is not None and group_idx <= (m.lastindex or 0):
                    val = m.group(group_idx)
                    out.append(val if val is not None else b"")
                i = j
                continue
        out.append(ch.encode("utf-8"))
        i += 1
    return b"".join(out)


def reference_replace(
    root: Path,
    spec: ReplaceSpec,
    *,
    rg_bin: str = "rg",
    include_hidden: bool = False,
) -> dict[str, ReplaceFileResult]:
    """Compute reference replacement by splicing bytes at ripgrep's exact match ranges (D3)."""
    query = CodeQuerySpec(
        form="regex" if spec.regex else "literal",
        pattern=spec.pattern,
        case_sensitive=spec.case_sensitive,
        whole_word=spec.whole_word,
        regex=spec.regex,
    )
    search_res = reference_search(root, query, rg_bin=rg_bin, include_hidden=include_hidden)

    compiled: re.Pattern[bytes] | None = None
    if spec.regex:
        flags = 0 if spec.case_sensitive else re.IGNORECASE
        compiled = re.compile(spec.pattern.encode("utf-8"), flags)

    hits_by_file: dict[str, list[SearchHit]] = {}
    for hit in search_res.hits:
        hits_by_file.setdefault(hit.path, []).append(hit)

    results: dict[str, ReplaceFileResult] = {}
    for rel_path, file_hits in sorted(hits_by_file.items()):
        raw = (Path(root) / rel_path).read_bytes()
        before_sha = hashlib.sha256(raw).hexdigest()
        chunks: list[bytes] = []
        cursor = 0
        for hit in sorted(file_hits, key=lambda h: (h.byte_offset, h.byte_end)):
            if hit.byte_offset < cursor:
                continue
            chunks.append(raw[cursor : hit.byte_offset])
            matched = raw[hit.byte_offset : hit.byte_end]
            chunks.append(_expand_replacement_bytes(spec.replacement, matched, compiled))
            cursor = hit.byte_end
        chunks.append(raw[cursor:])
        out_bytes = b"".join(chunks)
        after_sha = hashlib.sha256(out_bytes).hexdigest()
        results[rel_path] = ReplaceFileResult(
            path=rel_path,
            before_sha256=before_sha,
            after_sha256=after_sha,
            resulting_bytes=out_bytes,
            match_count=len(file_hits),
            changed=(out_bytes != raw),
        )
    return results


@dataclass(frozen=True)
class FileLookupHit:
    """One ranked S04 file-lookup match with matched code-point ranges."""

    path: str
    tier: int  # 0=basename_prefix, 1=basename_substring, 2=full_path_window
    window_len: int
    matched_ranges: tuple[tuple[int, int], ...]


def _tightest_subsequence_window(haystack: str, needle: str) -> tuple[int, tuple[tuple[int, int], ...]] | None:
    """Find the tightest (shortest end-start) subsequence match of needle in haystack."""
    if not needle:
        return 0, ()
    n_len = len(needle)
    best: tuple[int, int, list[int]] | None = None

    for start_idx, ch in enumerate(haystack):
        if ch != needle[0]:
            continue
        positions = [start_idx]
        pos = start_idx + 1
        matched_all = True
        for target in needle[1:]:
            found = haystack.find(target, pos)
            if found == -1:
                matched_all = False
                break
            positions.append(found)
            pos = found + 1
        if not matched_all:
            break
        win_len = positions[-1] - positions[0] + 1
        candidate = (win_len, positions[0], positions)
        if best is None or (candidate[0], candidate[1]) < (best[0], best[1]):
            best = candidate

    if best is None:
        return None
    return best[0], _merge_positions(best[2])


def _merge_positions(positions: list[int]) -> tuple[tuple[int, int], ...]:
    if not positions:
        return ()
    ranges: list[tuple[int, int]] = []
    start = positions[0]
    end = positions[0] + 1
    for p in positions[1:]:
        if p == end:
            end = p + 1
        else:
            ranges.append((start, end))
            start = p
            end = p + 1
    ranges.append((start, end))
    return tuple(ranges)


def reference_file_lookup(paths: list[str], query_text: str) -> list[FileLookupHit]:
    """Rank paths using the documented S04 order (docs/source-navigation.md):

    1. Basename prefix (tier 0)
    2. Basename substring (tier 1)
    3. Tightest full-path window (tier 2)
    4. Shorter path
    5. Lexicographic path (deterministic tie-breaker)
    """
    q = query_text.strip().lower()
    if not q:
        return []
    hits: list[FileLookupHit] = []
    for path in paths:
        norm = path.replace("\\", "/")
        lower_path = norm.lower()
        slash_idx = lower_path.rfind("/")
        base_start = slash_idx + 1
        basename = lower_path[base_start:]

        if basename.startswith(q):
            hits.append(
                FileLookupHit(
                    path=norm,
                    tier=0,
                    window_len=len(q),
                    matched_ranges=((base_start, base_start + len(q)),),
                )
            )
            continue
        sub_idx = basename.find(q)
        if sub_idx != -1:
            start = base_start + sub_idx
            hits.append(
                FileLookupHit(
                    path=norm,
                    tier=1,
                    window_len=len(q),
                    matched_ranges=((start, start + len(q)),),
                )
            )
            continue
        window = _tightest_subsequence_window(lower_path, q)
        if window is not None:
            win_len, ranges = window
            hits.append(
                FileLookupHit(
                    path=norm,
                    tier=2,
                    window_len=win_len,
                    matched_ranges=ranges,
                )
            )

    hits.sort(key=lambda h: (h.tier, h.window_len, len(h.path), h.path))
    return hits
