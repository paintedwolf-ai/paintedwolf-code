"""The change a verification run measures, and how its findings are reported.

The base is the merge base with the main branch, so a branch is measured
against where it left main and a checkout on main measures nothing. Paths and
lines compare that base with the working tree, which in a source snapshot is
the captured commit, including untracked files.

Findings print as one line each; on GitHub Actions they also become file
annotations and a section of the job summary.
"""

import os
from pathlib import Path
import re
import subprocess

BASE_ENV = "PW_CHANGE_BASE"
MAIN_REFS = ("origin/main", "main")
HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")


class Scope:
    def __init__(self, base, label, paths):
        self.base = base
        self.label = label
        self.paths = paths

    def touches(self, sources):
        """Whether any source file, or any file under a source directory, changed."""
        for source in sources:
            prefix = source.rstrip("/") + "/"
            if any(path == source or path.startswith(prefix) for path in self.paths):
                return True
        return False

    def describe(self):
        return f"change base {self.base[:12]} ({self.label}); {len(self.paths)} changed path(s)"


def git(root, *args, check=True):
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True)
    if check and result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)}: {result.stderr.strip()}")
    return result


def resolve(root):
    """The change scope, or None when no main branch is reachable."""
    explicit = os.environ.get(BASE_ENV, "")
    if explicit:
        base = git(root, "rev-parse", "--verify", "--quiet", explicit + "^{commit}", check=False).stdout.strip()
        if not base:
            raise RuntimeError(f"{BASE_ENV}={explicit} is not a commit")
        return Scope(base, f"{BASE_ENV}={explicit}", changed_paths(root, base))
    for ref in MAIN_REFS:
        base = git(root, "merge-base", "HEAD", ref, check=False).stdout.strip()
        if base:
            return Scope(base, f"merge base with {ref}", changed_paths(root, base))
    return None


def untracked(root, head):
    """Untracked work counts only when the change ends at the working tree."""
    if head:
        return ""
    return git(root, "ls-files", "--others", "--exclude-standard", "-z").stdout


def changed_paths(root, base, head=None):
    """Paths changed from base to head, or to the working tree when head is None."""
    diff = git(root, "diff", "--name-only", "--no-renames", "-z", base, *filter(None, [head])).stdout
    return sorted({path for path in (diff + untracked(root, head)).split("\0") if path})


def added_and_removed(root, base, head=None):
    """Paths the change added (untracked files included) and paths it removed."""
    added, removed = set(), set()
    fields = git(root, "diff", "--name-status", "--no-renames", "-z", base, *filter(None, [head])).stdout.split("\0")
    for status, path in zip(fields[0::2], fields[1::2]):
        if status == "A":
            added.add(path)
        elif status == "D":
            removed.add(path)
    added |= {path for path in untracked(root, head).split("\0") if path}
    return sorted(added), sorted(removed)


def tree_blobs(root, revision):
    entries = git(root, "ls-tree", "-r", "-z", revision).stdout
    return {path: metadata.split()[2] for entry in entries.split("\0") if entry
            for metadata, path in [entry.split("\t", 1)] if metadata.split()[1] == "blob"}


def blob_line_counts(root, objects):
    """Read committed additions in one process, without putting paths in a line protocol."""
    if not objects:
        return {}
    result = subprocess.run(["git", "-C", str(root), "cat-file", "--batch"],
                            input="".join(oid + "\n" for oid in objects.values()).encode(),
                            capture_output=True, check=True)
    counts, offset = {}, 0
    for path, oid in objects.items():
        end = result.stdout.index(b"\n", offset)
        identity, kind, size = result.stdout[offset:end].split()
        if identity.decode() != oid or kind != b"blob":
            raise RuntimeError("git cat-file returned an unexpected object")
        offset = end + 1
        length = int(size)
        counts[path] = len(result.stdout[offset:offset + length].decode(errors="replace").splitlines())
        offset += length + 1
    return counts


def changed_lines(root, base, paths, head=None):
    """Measure one complete diff; process count stays bounded as the change grows."""
    if not paths:
        return {}
    selected, out = set(paths), {}
    base_paths = tree_blobs(root, base)
    diff = git(root, "diff", "--raw", "-z", "--patch", "--no-color", "--no-ext-diff",
               "--no-renames", "--submodule=short", "-U0", base, *filter(None, [head])).stdout
    if diff:
        raw, separator, patch_text = diff.partition("\0\0")
        records = raw.split("\0")
        patches = re.split(r"(?m)^diff --git ", patch_text)[1:]
        if not separator or len(records) != 2 * len(patches):
            raise RuntimeError("git diff returned an unexpected raw/patch inventory")
        for path, patch_text in zip(records[1::2], patches):
            if path not in selected or path not in base_paths:
                continue
            lines = set()
            for line in patch_text.splitlines():
                match = HUNK.match(line)
                if match:
                    start, count = int(match.group(1)), int(match.group(2) or "1")
                    lines.update(range(start, start + count))
            if lines:
                out[path] = lines
    additions = selected - base_paths.keys()
    if head:
        blobs = tree_blobs(root, head)
        counts = blob_line_counts(root, {path: blobs[path] for path in sorted(additions) if path in blobs})
    else:
        counts = {path: len((Path(root) / path).read_text(errors="replace").splitlines())
                  for path in additions if (Path(root) / path).is_file()}
    out.update({path: set(range(1, count + 1)) for path, count in counts.items()})
    return out


def ranges(lines):
    """Collapse line numbers into inclusive (start, end) runs."""
    out = []
    for line in sorted(lines):
        if out and line == out[-1][1] + 1:
            out[-1][1] = line
        else:
            out.append([line, line])
    return [tuple(run) for run in out]


class Finding:
    """One reported observation. Levels: error, warning, notice, info."""

    def __init__(self, level, area, text, path=None, line=None, end_line=None):
        self.level, self.area, self.text = level, area, text
        self.path, self.line, self.end_line = path, line, end_line

    def location(self):
        if not self.path:
            return ""
        if self.line is None:
            return f" [{self.path}]"
        if self.end_line and self.end_line != self.line:
            return f" [{self.path}:{self.line}-{self.end_line}]"
        return f" [{self.path}:{self.line}]"


SUMMARY_GROUPS = (("error", "Fails"), ("warning", "Warnings"), ("notice", "Notices"), ("info", "Context"))


def emit(root, title, header, findings, failed):
    """Print findings, and on GitHub Actions annotate files and extend the job summary."""
    print(f"{title}: {header}")
    for finding in findings:
        print(f"{finding.level}: {finding.area}: {finding.text}{finding.location()}")
    if os.environ.get("GITHUB_ACTIONS") != "true":
        return
    for finding in findings:
        if finding.level == "info":
            continue
        properties = []
        if finding.path and (Path(root) / finding.path).exists():
            properties.append(f"file={finding.path}")
            if finding.line is not None:
                properties.append(f"line={finding.line}")
                if finding.end_line:
                    properties.append(f"endLine={finding.end_line}")
        properties.append(f"title={title} ({finding.area})")
        print(f"::{finding.level} {','.join(properties)}::{finding.text}")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if not summary:
        return
    rows = [f"### {title}: {'failed' if failed else 'passed'}", "", header, ""]
    for level, heading in SUMMARY_GROUPS:
        selected = [f for f in findings if f.level == level]
        if selected:
            rows += [f"**{heading}**", ""] + [f"- {f.area}: {f.text}{f.location()}" for f in selected] + [""]
    with Path(summary).open("a") as output:
        output.write("\n".join(rows) + "\n")
