"""Conservative change scope shared by pull requests and merge groups.

Only ordinary source files are narrowed. Embedded data, generated contracts,
configuration, deleted packages, and unknown paths widen the gate. Test imports
participate in reverse reachability; contracts and smoke tests run unconditionally.
"""

import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[2]
SHARED = (".github/", "scripts/", "dependencies/", "schemas/", "docs/openapi",
          "lycaon/config/", "lycaon/pkg/", "lycaon/test/contract/", "lycaon/vulndb")


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def event_base(event, name):
    if name == "pull_request":
        return git("merge-base", "HEAD", event["pull_request"]["base"]["sha"])
    if name == "merge_group":
        return event["merge_group"]["base_sha"]
    if name == "push":
        return event["before"]
    return ""


def change():
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text()) if os.environ.get("GITHUB_EVENT_PATH") else {}
    base = os.environ.get("PW_CHANGE_BASE") or event_base(event, os.environ.get("GITHUB_EVENT_NAME", ""))
    head = git("rev-parse", "HEAD")
    if not base or set(base) == {"0"}:
        return {"base": "", "head": head, "paths": [], "full": True, "reasons": ["no comparable base"]}
    # Resolve before diff: a missing base must fail planning, never yield an empty gate.
    base = git("rev-parse", "--verify", base + "^{commit}")
    paths = git("diff", "--name-only", "--no-renames", "-z", base, head).split("\0")
    paths = sorted(p for p in paths if p)
    full, reasons = broad_scope(paths)
    return {"base": base, "head": head, "paths": paths, "full": full, "reasons": reasons}


def broad_scope(paths):
    reasons = []
    for path in paths:
        ordinary = (path.startswith("lycaon/internal/") and path.endswith(".go")
                    or path.startswith("lycaon-den/src/") and path.endswith((".ts", ".tsx", ".css")))
        if path.startswith(SHARED) or not ordinary:
            reasons.append(path)
    return bool(reasons), reasons


def selected_lanes(scope, lanes):
    if scope["full"]:
        return set(lanes)
    paths = scope["paths"]
    areas = {"go" if p.startswith("lycaon/") else "den" for p in paths}
    # Repository contracts read source outside the package/import graph.
    chosen = {"limits", "contracts", "integration-build", "behavior"}
    if "go" in areas:
        chosen |= {"behavior", "lint", "vulnerabilities"}
    if "den" in areas:
        chosen.add("frontend")
    return chosen & set(lanes)


def reverse_closure(graph, seeds):
    reached = set(seeds)
    while True:
        next_set = reached | {name for name, edges in graph.items() if set(edges) & reached}
        if next_set == reached:
            return reached
        reached = next_set


def go_scope(scope):
    """Go's resolved graph, including integration and test edges; uncertainty means the full recipe."""
    if scope["full"]:
        return None
    from verification_execute import json_stream
    command = ["go", "list", "-json", "-tags=integration", "./..."]
    result = subprocess.run(command, cwd=ROOT / "lycaon", capture_output=True, text=True,
                            env={**os.environ, "GOPROXY": "off", "GOTOOLCHAIN": "local"})
    if result.returncode:
        return None
    records = list(json_stream(result.stdout))
    if not records or any(r.get("Error") or r.get("DepsErrors") for r in records):
        return None
    graph = {r["ImportPath"]: r.get("Imports", []) + r.get("TestImports", []) + r.get("XTestImports", [])
             for r in records}
    directories = {Path(r["Dir"]).relative_to(ROOT).as_posix(): r["ImportPath"] for r in records}
    seeds = set()
    for path in scope["paths"]:
        if not path.startswith("lycaon/"):
            continue
        package = directories.get(str(Path(path).parent))
        if package is None:
            return None
        seeds.add(package)
    reached = reverse_closure(graph, seeds)
    reached |= {name for directory, name in directories.items()
                if directory.startswith(("lycaon/test/contract/", "lycaon/test/smoke"))}
    return sorted("./" + directory.removeprefix("lycaon/") for directory, name in directories.items()
                  if name in reached)
