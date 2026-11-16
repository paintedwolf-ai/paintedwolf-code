"""Structural defects a change adds or makes.

arch-lint (lycaon/cmd/arch-lint) reports code no shipped program reaches,
context keys nothing sets, tables nothing reads, forwarders, dropped fields,
test-support imports, and unreferenced commands and scripts; knip reports Den
files, exports and types production code never uses. Each finding is keyed by
symbol, never by line. This runs arch-lint on the working tree and on the change base and
fails on findings the base does not have, so deleting a function's last caller
fails as surely as adding a dead function. unparam reports unused parameters
on changed lines. `--all` lists every finding at the working tree instead.

Policy, including the test-support package rule, is scripts/arch-lint-policy.json.
"""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

from artifact_paths import artifact_root, bin_dir
import change_report
from change_report import Finding

ROOT = Path(__file__).resolve().parent.parent
POLICY = ROOT / "scripts" / "arch-lint-policy.json"
TITLE = "arch-lint"


def run(args, cwd=None):
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True)
    if result.returncode != 0:
        raise RuntimeError(f"{' '.join(map(str, args))}: {result.stderr.strip()}")
    return result.stdout


def build(root, out_dir):
    """Build arch-lint from the working tree once; the same binary judges both trees."""
    out_dir.mkdir(parents=True, exist_ok=True)
    binary = out_dir / "arch-lint"
    run(["go", "build", "-o", str(binary), "./cmd/arch-lint"], cwd=root / "lycaon")
    return binary


def knip(tree, policy):
    """Den's unused files, exports and types under knip's production mode."""
    den = policy["den"]
    den_dir = tree / den["dir"]
    modules = den_dir / "node_modules"
    if tree != ROOT:
        # The base tree is judged with the working tree's dependencies and knip policy.
        if not modules.exists():
            modules.symlink_to(ROOT / den["dir"] / "node_modules", target_is_directory=True)
        config = ROOT / den["dir"] / "knip.json"
        if config.exists():
            shutil.copyfile(config, den_dir / "knip.json")
    # Node can cut piped output short when it exits; a file receives all of it.
    with tempfile.TemporaryFile(mode="w+") as out:
        result = subprocess.run(den["command"], cwd=den_dir, stdout=out, stderr=subprocess.PIPE, text=True)
        if result.returncode != 0:
            raise RuntimeError(f"knip: {result.stderr.strip()}")
        out.seek(0)
        report = json.loads(out.read() or "{}")
    found = {}
    for path in report.get("files") or []:
        found[("unused-file", path)] = {"kind": "unused-file", "key": path, "pos": f"{den['dir']}/{path}",
                                        "message": f"{path} is not imported by Den production code"}
    for issue in report.get("issues") or []:
        for kind in ("exports", "types"):
            for item in issue.get(kind) or []:
                key = f"{issue['file']}:{item['name']}"
                label = "unused-export" if kind == "exports" else "unused-type"
                found[(label, key)] = {"kind": label, "key": key,
                                       "pos": f"{den['dir']}/{issue['file']}:{item.get('line', 0)}",
                                       "message": f"{item['name']} in {issue['file']} is not used by Den production code"}
    return found


def emitter_flags(policy, tree):
    """Reach also judges what the product emits when the policy names the product and its declarations."""
    if "emitters" not in policy:
        return []
    return ["-product", ",".join(policy["reach"]["product"]), "-emitters", json.dumps(policy["emitters"]), "-repo", tree]


def checks(binary, tree, policy, go=True, den=False):
    """Every arch-lint check over one tree, as (kind, key) → finding. Without go,
    the Go checks are skipped; with den, knip runs over Den."""
    go_dir = tree / "lycaon"
    support = ",".join(policy["test_support"])
    reach = policy["reach"]
    go_commands = [
        [binary, "reach", "-dir", go_dir, "-goos", ",".join(reach["goos"]), "-report", reach["report"],
         "-skip", support, "-schema", go_dir / reach["schema"], "-scope", ",".join(reach.get("scope", [])),
         *emitter_flags(policy, tree), *reach["roots"]],
        [binary, "vet", "-dir", go_dir, "-test-support", support, *policy["vet"]["packages"]],
    ]
    found = {}
    with tempfile.NamedTemporaryFile("w", suffix=".index") as index:
        index.write(run(["git", "-C", str(tree), "ls-files", "-s", "-z"]))
        index.flush()
        commands = (go_commands if go else []) + [[binary, "refs", "-root", tree, "-index", index.name]]
        for command in commands:
            for finding in json.loads(run([str(part) for part in command]) or "null") or []:
                found[(finding["kind"], finding["key"])] = finding
    if den:
        found.update(knip(tree, policy))
    return found


def base_checks(root, base, binary, policy, cache, go=True, den=False):
    """Findings at the base, cached by base commit, scope and the binary that judged it."""
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()[:16]
    cached = cache / f"base-{base}-{digest}{'' if go else '-xrefs'}{'-den' if den else ''}.json"
    if cached.exists():
        return {(f["kind"], f["key"]): f for f in json.loads(cached.read_text())}
    tree = cache / "base-tree"
    if tree.exists():
        run(["git", "-C", str(tree), "checkout", "-q", "--force", "--detach", base])
    else:
        run(["git", "-C", str(root), "worktree", "add", "-q", "--force", "--detach", str(tree), base])
    found = checks(binary, tree, policy, go, den)
    cached.write_text(json.dumps(list(found.values())))
    return found


def golangci_lint(root):
    run([sys.executable, str(root / "scripts" / "analysis_tools.py"), "ensure", "golangci-lint"])
    return bin_dir(root) / "golangci-lint"


def changed_go_packages(root, paths):
    """Package directories under lycaon/ holding a changed non-test Go file."""
    dirs = set()
    for path in paths:
        if path.startswith("lycaon/") and path.endswith(".go") and not path.endswith("_test.go") \
                and "/testdata/" not in path:
            dirs.add("./" + str(Path(path).parent.relative_to("lycaon")))
    return sorted(d for d in dirs if (root / "lycaon" / d).is_dir())


def unparam(root, base, linter, packages):
    """Unused parameters golangci-lint's unparam finds on lines the change touched."""
    if not packages:
        return []
    result = subprocess.run(
        [str(linter), "run", "--no-config", "--default=none", "--enable=unparam", "--tests=false",
         f"--new-from-rev={base}", "--output.json.path=stdout", "--output.text.path=stderr", *packages],
        cwd=root / "lycaon", capture_output=True, text=True)
    if result.returncode not in (0, 1):
        raise RuntimeError(f"golangci-lint: {result.stderr.strip()}")
    # The JSON report is the first document; golangci-lint may print a summary after it.
    report = json.JSONDecoder().raw_decode(result.stdout.strip() or "{}")[0]
    return [{"kind": "unparam", "key": issue["Text"],
             "pos": f"lycaon/{issue['Pos']['Filename']}:{issue['Pos']['Line']}", "message": issue["Text"]}
            for issue in report.get("Issues") or []]


def to_finding(level, f):
    path, _, line = f["pos"].partition(":")
    line = line.split(":")[0]
    return Finding(level, f["kind"], f["message"], path=path or None, line=int(line) if line.isdigit() else None)


def changes(root, scope, policy, cache, binary, linter):
    go = scope.touches(["lycaon/"])
    den = scope.touches([policy["den"]["dir"] + "/"])
    head = checks(binary, root, policy, go, den)
    base = base_checks(root, scope.base, binary, policy, cache, go, den)
    new = [f for key, f in sorted(head.items()) if key not in base]
    new += unparam(root, scope.base, linter, changed_go_packages(root, scope.paths))
    findings = [to_finding("error", f) for f in new]
    header = f"{scope.describe()}; {len(head)} finding(s) at the working tree, {len(base)} at the base"
    return header, findings, bool(findings)


def everything(root, policy, binary):
    head = checks(binary, root, policy, den=True)
    findings = [to_finding("notice", f) for _, f in sorted(head.items())]
    counts = {}
    for kind, _ in head:
        counts[kind] = counts.get(kind, 0) + 1
    header = "every finding at the working tree: " + ", ".join(f"{k} {n}" for k, n in sorted(counts.items()))
    return header, findings, False


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--all", action="store_true", help="list every finding at the working tree")
    args = parser.parse_args(argv)
    policy = json.loads(POLICY.read_text())
    cache = artifact_root(ROOT) / "arch-lint"
    binary = build(ROOT, cache / "bin")
    if args.all:
        header, findings, failed = everything(ROOT, policy, binary)
    else:
        scope = change_report.resolve(ROOT)
        if scope is None:
            raise RuntimeError("no main branch to measure the change against; set PW_CHANGE_BASE")
        header, findings, failed = changes(ROOT, scope, policy, cache, binary, golangci_lint(ROOT))
    change_report.emit(ROOT, TITLE, header, findings, failed)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
