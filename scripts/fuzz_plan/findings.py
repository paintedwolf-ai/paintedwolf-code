"""Store, gate, file and clear nightly Go fuzz findings on the fuzz-findings branch."""

import argparse
import contextlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tempfile

from verification_plan import FUZZ_TARGET, GO_MODULE, catalog

DEFAULT_REMOTE = "origin"
DEFAULT_BRANCH = "fuzz-findings"
MANIFEST_NAME = "manifest.json"
SIDECAR_SUFFIX = ".finding.json"
README_NAME = "README.md"
MAX_PUSH_ATTEMPTS = 5
MODULE = GO_MODULE.name
FINDING_FIELDS = ("package", "target", "id", "commit", "run_id", "failure_output", "reproduced")


class FindingsError(ValueError):
    """Raised when a finding path, manifest, branch or git operation is invalid."""


def _git(repo, *args, check=True, text=True):
    result = subprocess.run(["git", *args], cwd=repo, capture_output=True, text=text)
    if check and result.returncode != 0:
        stderr = result.stderr if text else result.stderr.decode(errors="replace")
        stdout = result.stdout if text else result.stdout.decode(errors="replace")
        detail = (stderr or stdout).strip()
        raise FindingsError(f"git {' '.join(args)} failed: {detail}")
    return result


def declared_targets(plan=None):
    """{package: set(target)} declared in verification-plan.json's fuzz section."""
    fuzz = (plan or catalog())["fuzz"]
    return {pkg: set(targets) for pkg, targets in fuzz["targets"].items()}


def validate_finding_path(rel_path, targets):
    """Validate a POSIX repo-relative finding path and return (package, target, id)."""
    if not isinstance(rel_path, str) or not rel_path or "\\" in rel_path or "\x00" in rel_path:
        raise FindingsError(f"invalid finding path: {rel_path!r}")
    if rel_path.startswith("/"):
        raise FindingsError(f"finding path must be relative: {rel_path!r}")
    parts = PurePosixPath(rel_path).parts
    if any(part in ("", ".", "..") for part in parts):
        raise FindingsError(f"finding path contains disallowed segment: {rel_path!r}")
    if len(parts) < 5 or parts[0] != MODULE or "testdata" not in parts[1:]:
        raise FindingsError(f"finding path outside module testdata/fuzz: {rel_path!r}")
    td_idx = parts.index("testdata")
    if len(parts) != td_idx + 4 or parts[td_idx + 1] != "fuzz":
        raise FindingsError(f"finding path must match {MODULE}/<pkg>/testdata/fuzz/<Target>/<id>: {rel_path!r}")
    pkg_parts = parts[1:td_idx]
    target, finding_id = parts[td_idx + 2], parts[td_idx + 3]
    if not pkg_parts or not FUZZ_TARGET.fullmatch(target) or not finding_id or finding_id.endswith(SIDECAR_SUFFIX):
        raise FindingsError(f"invalid finding target or id in path: {rel_path!r}")
    package = "./" + "/".join(pkg_parts)
    if target not in targets.get(package, ()):
        raise FindingsError(f"undeclared fuzz target {package} {target} in path {rel_path!r}")
    return package, target, finding_id


def finding_rel_path(package, target, finding_id):
    """Repo-relative POSIX path for a finding input."""
    pkg = package.removeprefix("./")
    return f"{MODULE}/{pkg}/testdata/fuzz/{target}/{finding_id}"


def sidecar_rel_path(rel_path):
    return f"{rel_path}{SIDECAR_SUFFIX}"


def validate_entry(entry, targets, expected_path=None):
    """Validate one manifest/sidecar entry and return its normalized repo-relative input path."""
    if not isinstance(entry, dict):
        raise FindingsError("finding manifest entry must be a JSON object")
    missing = [key for key in FINDING_FIELDS if key not in entry]
    if missing:
        raise FindingsError(f"finding manifest entry missing fields: {', '.join(missing)}")
    for key in ("package", "target", "id", "commit", "run_id", "failure_output"):
        if not isinstance(entry[key], str):
            raise FindingsError(f"finding manifest field {key!r} must be a string")
    if not isinstance(entry["reproduced"], bool):
        raise FindingsError("finding manifest field 'reproduced' must be a boolean")
    rel_path = finding_rel_path(entry["package"], entry["target"], entry["id"])
    package, target, finding_id = validate_finding_path(rel_path, targets)
    if (package, target, finding_id) != (entry["package"], entry["target"], entry["id"]):
        raise FindingsError(f"finding manifest identity mismatch for {rel_path!r}")
    if expected_path is not None and rel_path != expected_path:
        raise FindingsError(f"finding manifest path {rel_path!r} does not match {expected_path!r}")
    return rel_path


def _check_no_symlinks(root, target_path):
    root_resolved = root.resolve()
    current = root
    for part in PurePosixPath(target_path).parts:
        current = current / part
        if current.is_symlink():
            raise FindingsError(f"symlink disallowed in finding path: {target_path!r}")
    if not current.is_file():
        raise FindingsError(f"finding input missing or not a regular file: {target_path!r}")
    try:
        current.resolve().relative_to(root_resolved)
    except ValueError as error:
        raise FindingsError(f"finding path escapes root: {target_path!r}") from error
    return current


def load_artifact(artifact_dir, plan=None):
    """Validate a downloaded fuzz-findings artifact directory and return [(rel_path, bytes, entry)]."""
    root = Path(artifact_dir)
    manifest_path = root / MANIFEST_NAME
    if manifest_path.is_symlink() or not manifest_path.is_file():
        raise FindingsError(f"missing or invalid {MANIFEST_NAME} in {root}")
    try:
        raw = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise FindingsError(f"malformed {MANIFEST_NAME}: {error}") from error
    if not isinstance(raw, list):
        raise FindingsError(f"{MANIFEST_NAME} must contain a JSON array")
    targets = declared_targets(plan)
    entries_by_path = {}
    for item in raw:
        rel_path = validate_entry(item, targets)
        input_file = _check_no_symlinks(root, rel_path)
        entries_by_path[rel_path] = (input_file.read_bytes(), item)
    # Reject any unexpected files or symlinks anywhere in the artifact directory.
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        current_dir = Path(dirpath)
        for name in dirnames:
            if (current_dir / name).is_symlink():
                raise FindingsError(f"symlink disallowed in artifact: {(current_dir / name).relative_to(root)}")
        for name in filenames:
            full = current_dir / name
            rel = full.relative_to(root).as_posix()
            if full.is_symlink():
                raise FindingsError(f"symlink disallowed in artifact: {rel!r}")
            if rel != MANIFEST_NAME and rel not in entries_by_path:
                raise FindingsError(f"unexpected file in artifact: {rel!r}")
    return [(path, data, entry) for path, (data, entry) in sorted(entries_by_path.items())]


def read_branch_findings(repo, ref, plan=None):
    """Read and validate all findings on git ref; return (commit_sha, [(rel_path, bytes, entry)])."""
    commit = _git(repo, "rev-parse", "--verify", f"{ref}^{{commit}}").stdout.strip()
    listed = _git(repo, "ls-tree", "-r", "-z", commit).stdout
    targets = declared_targets(plan)
    inputs, sidecars = {}, {}
    for record in listed.split("\x00"):
        if not record:
            continue
        meta, rel_path = record.split("\t", 1)
        mode, obj_type, blob_sha = meta.split(" ")
        if mode == "120000":
            raise FindingsError(f"symlink disallowed on findings branch: {rel_path!r}")
        if obj_type != "blob":
            continue
        if rel_path == README_NAME:
            continue
        if rel_path.endswith(SIDECAR_SUFFIX):
            input_path = rel_path[: -len(SIDECAR_SUFFIX)]
            validate_finding_path(input_path, targets)
            sidecars[input_path] = blob_sha
        else:
            validate_finding_path(rel_path, targets)
            inputs[rel_path] = blob_sha
    missing_sidecars = sorted(set(inputs) - set(sidecars))
    if missing_sidecars:
        raise FindingsError(f"finding missing manifest sidecar: {', '.join(missing_sidecars)}")
    orphan_sidecars = sorted(set(sidecars) - set(inputs))
    if orphan_sidecars:
        raise FindingsError(f"manifest sidecar missing input file: {', '.join(orphan_sidecars)}")
    findings = []
    for rel_path in sorted(inputs):
        input_bytes = _git(repo, "cat-file", "blob", inputs[rel_path], text=False).stdout
        sidecar_raw = _git(repo, "cat-file", "blob", sidecars[rel_path]).stdout
        try:
            entry = json.loads(sidecar_raw)
        except json.JSONDecodeError as error:
            raise FindingsError(f"malformed finding manifest {sidecar_rel_path(rel_path)}: {error}") from error
        validate_entry(entry, targets, expected_path=rel_path)
        findings.append((rel_path, input_bytes, entry))
    return commit, findings


def _fetch_branch(repo, remote, branch):
    _git(repo, "fetch", "--no-tags", remote, f"refs/heads/{branch}")
    return _git(repo, "rev-parse", "--verify", "FETCH_HEAD^{commit}").stdout.strip()


def _resolve_check_ref(repo, remote, branch, ref, offline):
    if ref:
        return ref
    if offline:
        return f"refs/remotes/{remote}/{branch}"
    return _fetch_branch(repo, remote, branch)


def format_unresolved(rel_path, entry, remote, branch, tags):
    tag_flag = f"-tags={','.join(tags)} " if tags else ""
    lines = [
        f"UNRESOLVED fuzz finding: {entry['package']} {entry['target']} ({entry['id']})",
        f"  path: {rel_path}",
        f"  found_at: {entry['commit']} (run {entry['run_id']})",
        f"  reproduced: {str(entry['reproduced']).lower()}",
        "  reproduce locally:",
        f"    git fetch {remote} {branch}",
        f"    mkdir -p {PurePosixPath(rel_path).parent.as_posix()}",
        f"    git show FETCH_HEAD:{rel_path} > {rel_path}",
        f"    go test -C {MODULE} {tag_flag}-run=^{entry['target']}$/{entry['id']} {entry['package']}",
        "  recorded failure output:",
    ]
    output = entry["failure_output"].strip() or "(empty)"
    lines.extend(f"    {line}" for line in output.splitlines())
    return "\n".join(lines)


def check_findings(repo, *, remote=DEFAULT_REMOTE, branch=DEFAULT_BRANCH, ref="", offline=False,
                   receipt_path=None, plan=None, stream=None):
    """Fail if any finding on fuzz-findings is not committed byte-identically in repo."""
    out = stream or sys.stderr
    repo_path = Path(repo)
    resolved_plan = plan or catalog()
    resolved_ref = _resolve_check_ref(repo_path, remote, branch, ref, offline)
    commit, findings = read_branch_findings(repo_path, resolved_ref, resolved_plan)
    tags = resolved_plan["fuzz"]["tags"]
    unresolved = []
    for rel_path, expected_bytes, entry in findings:
        local_file = repo_path / rel_path
        if local_file.is_symlink() or not local_file.is_file() or local_file.read_bytes() != expected_bytes:
            unresolved.append((rel_path, entry))
    receipt = {
        "branch": branch,
        "commit": commit,
        "open_count": len(findings),
        "unresolved_count": len(unresolved),
        "unresolved": [rel_path for rel_path, _ in unresolved],
    }
    if receipt_path:
        dest = Path(receipt_path)
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"fuzz-findings: checked {branch}@{commit} ({len(findings)} open, {len(unresolved)} unresolved)", file=out)
    for rel_path, entry in unresolved:
        print(format_unresolved(rel_path, entry, remote, branch, tags), file=out)
    return 1 if unresolved else 0, receipt


@contextlib.contextmanager
def _temp_worktree(repo, commit):
    tmp = Path(tempfile.mkdtemp(prefix="pw-fuzz-findings-"))
    wt = tmp / "wt"
    _git(repo, "worktree", "add", "--detach", str(wt), commit)
    try:
        yield wt
    finally:
        _git(repo, "worktree", "remove", "--force", str(wt), check=False)
        shutil.rmtree(tmp, ignore_errors=True)


def _commit_and_push_with_retry(repo, remote, branch, apply_fn):
    """Fetch branch, apply mutation in a scratch worktree, commit and push with non-FF retry."""
    for attempt in range(1, MAX_PUSH_ATTEMPTS + 1):
        tip = _fetch_branch(repo, remote, branch)
        with _temp_worktree(repo, tip) as wt:
            message = apply_fn(wt, tip)
            if message is None:
                return None
            _git(wt, "config", "user.name", "Painted Wolf CI")
            _git(wt, "config", "user.email", "ci@paintedwolf.local")
            _git(wt, "commit", "-m", message)
            new_commit = _git(wt, "rev-parse", "HEAD").stdout.strip()
            push = _git(wt, "push", remote, f"HEAD:refs/heads/{branch}", check=False)
            if push.returncode == 0:
                return new_commit
            if attempt == MAX_PUSH_ATTEMPTS:
                raise FindingsError(f"git push {remote} {branch} failed after {attempt} attempts: {push.stderr.strip()}")
    return None  # pragma: no cover


def _blob_on_ref(repo, ref, rel_path):
    exists = _git(repo, "cat-file", "-e", f"{ref}:{rel_path}", check=False)
    if exists.returncode != 0:
        return None
    return _git(repo, "cat-file", "blob", f"{ref}:{rel_path}", text=False).stdout


def file_findings(repo, artifact_dir, *, remote=DEFAULT_REMOTE, branch=DEFAULT_BRANCH,
                  main_ref="origin/main", plan=None, stream=None):
    """Validate artifact findings, skip ids already on branch or main, commit and push."""
    out = stream or sys.stderr
    repo_path = Path(repo)
    items = load_artifact(artifact_dir, plan)
    if not items:
        print("fuzz-findings: artifact has no findings", file=out)
        return 0

    def apply(wt, _tip):
        added = []
        for rel_path, data, entry in items:
            if (wt / rel_path).exists():
                continue
            if _blob_on_ref(repo_path, main_ref, rel_path) is not None:
                continue
            dest = wt / rel_path
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_bytes(data)
            sidecar = wt / sidecar_rel_path(rel_path)
            sidecar.write_text(json.dumps(entry, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            _git(wt, "add", "--", rel_path, sidecar_rel_path(rel_path))
            added.append(f"{entry['package']} {entry['target']} {entry['id']}")
        if not added:
            return None
        header = f"fuzz-findings: file {len(added)} finding(s)"
        return header + "\n\n" + "\n".join(f"- {line}" for line in added) + "\n"

    commit = _commit_and_push_with_retry(repo_path, remote, branch, apply)
    if commit:
        print(f"fuzz-findings: filed findings at {branch}@{commit}", file=out)
    else:
        print("fuzz-findings: all findings already filed or committed on main", file=out)
    return 0


def clear_findings(repo, *, remote=DEFAULT_REMOTE, branch=DEFAULT_BRANCH,
                   main_ref="HEAD", plan=None, stream=None):
    """Remove from fuzz-findings every finding whose input is committed byte-identically on main_ref."""
    out = stream or sys.stderr
    repo_path = Path(repo)
    resolved_plan = plan or catalog()

    def apply(wt, tip):
        _, existing = read_branch_findings(repo_path, tip, resolved_plan)
        cleared = []
        for rel_path, expected_bytes, entry in existing:
            main_bytes = _blob_on_ref(repo_path, main_ref, rel_path)
            if main_bytes == expected_bytes:
                _git(wt, "rm", "--", rel_path, sidecar_rel_path(rel_path))
                cleared.append(f"{entry['package']} {entry['target']} {entry['id']}")
        if not cleared:
            return None
        header = f"fuzz-findings: clear {len(cleared)} resolved finding(s)"
        return header + "\n\n" + "\n".join(f"- {line}" for line in cleared) + "\n"

    commit = _commit_and_push_with_retry(repo_path, remote, branch, apply)
    if commit:
        print(f"fuzz-findings: cleared resolved findings at {branch}@{commit}", file=out)
    else:
        print("fuzz-findings: no findings newly resolved on main", file=out)
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    p_check = sub.add_parser("check", help="Gate: fail if any open finding is not committed byte-identically")
    p_check.add_argument("--repo", default=str(GO_MODULE.parent))
    p_check.add_argument("--remote", default=os.environ.get("PW_FUZZ_FINDINGS_REMOTE", DEFAULT_REMOTE))
    p_check.add_argument("--branch", default=os.environ.get("PW_FUZZ_FINDINGS_BRANCH", DEFAULT_BRANCH))
    p_check.add_argument("--ref", default=os.environ.get("PW_FUZZ_FINDINGS_REF", ""))
    p_check.add_argument("--offline", action="store_true",
                         default=os.environ.get("PW_FUZZ_FINDINGS_OFFLINE") == "1")
    p_check.add_argument("--receipt", default=os.environ.get("PW_FUZZ_FINDINGS_RECEIPT", ""))

    p_file = sub.add_parser("file", help="Validate and push findings from a nightly artifact")
    p_file.add_argument("--repo", default=str(GO_MODULE.parent))
    p_file.add_argument("--artifact-dir", required=True)
    p_file.add_argument("--remote", default=DEFAULT_REMOTE)
    p_file.add_argument("--branch", default=DEFAULT_BRANCH)
    p_file.add_argument("--main-ref", default="origin/main")

    p_clear = sub.add_parser("clear", help="Remove findings committed byte-identically on main")
    p_clear.add_argument("--repo", default=str(GO_MODULE.parent))
    p_clear.add_argument("--remote", default=DEFAULT_REMOTE)
    p_clear.add_argument("--branch", default=DEFAULT_BRANCH)
    p_clear.add_argument("--main-ref", default="HEAD")

    args = parser.parse_args(argv)
    try:
        if args.command == "check":
            code, _ = check_findings(
                args.repo, remote=args.remote, branch=args.branch,
                ref=args.ref, offline=args.offline, receipt_path=args.receipt or None,
            )
            return code
        if args.command == "file":
            return file_findings(args.repo, args.artifact_dir, remote=args.remote,
                                 branch=args.branch, main_ref=args.main_ref)
        return clear_findings(args.repo, remote=args.remote, branch=args.branch, main_ref=args.main_ref)
    except FindingsError as error:
        print(f"fuzz-findings: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
