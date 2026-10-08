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


def changed_paths(root, base):
    diff = git(root, "diff", "--name-only", "--no-renames", "-z", base).stdout
    untracked = git(root, "ls-files", "--others", "--exclude-standard", "-z").stdout
    return sorted({path for path in (diff + untracked).split("\0") if path})


def added_and_removed(root, base):
    """Paths the change added (untracked files included) and paths it removed."""
    added, removed = set(), set()
    fields = git(root, "diff", "--name-status", "--no-renames", "-z", base).stdout.split("\0")
    for status, path in zip(fields[0::2], fields[1::2]):
        if status == "A":
            added.add(path)
        elif status == "D":
            removed.add(path)
    added |= {path for path in git(root, "ls-files", "--others", "--exclude-standard", "-z").stdout.split("\0") if path}
    return sorted(added), sorted(removed)


def changed_lines(root, base, paths):
    """Line numbers each path adds or changes relative to base; new files count whole."""
    out = {}
    for path in paths:
        if git(root, "cat-file", "-e", f"{base}:{path}", check=False).returncode != 0:
            file = Path(root) / path
            if file.is_file():
                out[path] = set(range(1, len(file.read_text(errors="replace").splitlines()) + 1))
            continue
        lines = set()
        diff = git(root, "diff", "--no-color", "--no-ext-diff", "--no-renames", "-U0", base, "--", path).stdout
        for line in diff.splitlines():
            match = HUNK.match(line)
            if match:
                start, count = int(match.group(1)), int(match.group(2) or "1")
                lines.update(range(start, start + count))
        if lines:
            out[path] = lines
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
