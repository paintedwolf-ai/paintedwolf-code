"""Conservative change scope shared by pull requests and merge groups.

Files in a Go package's tree, CI tooling, and documentation prose are narrowed.
Generated contracts, configuration, dependencies, build inputs, and unknown paths
widen the gate. Test imports participate in reverse reachability; repository contracts
and smoke tests run unconditionally. The machinery's Go tests run when what they import
changes or when CI tooling changes.
"""

import json
import os
from pathlib import Path
import subprocess

from ci_policy.native_impact import dependencies as native_dependencies

ROOT = Path(__file__).resolve().parents[2]
SHARED = (".github/", "scripts/", "dependencies/", "schemas/", "docs/openapi",
          "lycaon/config/", "lycaon/pkg/", "lycaon/vulndb")
# CI's own logic and tests: no product test reads them, and the runner lane and machinery tests check them.
TOOLING = (".github/workflows/", "scripts/ci_policy/", "scripts/verification_tests/")
# Go tests of the verification and release machinery: they execute its scripts and read its workflows.
MACHINERY = ("lycaon/test/contract/runner", "lycaon/test/contract/hosted", "lycaon/test/contract/release")
# These decide how the lanes themselves run, so a change must exercise every lane.
TOOLING_EXCEPTIONS = {".github/workflows/ci.yml", ".github/workflows/verification.yml",
                      "scripts/ci_policy/hosted_execution.py", "scripts/ci_policy/hosted_plan.py"}
# Prose that no build, fixture, or prompt reads; the repository contracts every scope runs check it.
# CHANGELOG.md is not among them: Den embeds it.
PROSE = {"AGENTS.md", "CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "README.md", "REPORTING.md", "SECURITY.md",
         "lycaon/AGENTS.md", "lycaon-den/AGENTS.md"}
# What the script suites read: their own sources, the workflows and actions, packaging, and the task runner.
SCRIPT_INPUTS = ("scripts/", ".github/", "packaging/")
SCRIPT_FILES = {"Taskfile.yml", "task"}
# What the Den suite builds from: its tree, the wire contract it generates types from, the changelog it
# embeds, and its runtime pins.
FRONTEND_INPUTS = ("lycaon-den/", "docs/openapi/")
FRONTEND_FILES = {"CHANGELOG.md", ".bun-version", ".node-version"}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def event_base(event, name):
    if name == "pull_request":
        # A pull request runs on its merge with the current base branch. The
        # event's base sha is not updated when that branch moves, so the merge's
        # first parent is what the change is applied onto.
        parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()[1:]
        if len(parents) == 2 and parents[1] == event["pull_request"]["head"]["sha"]:
            return parents[0]
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


def prose(path):
    return path in PROSE or path.startswith("docs/") and path.endswith(".md") and not path.startswith(SHARED)


def machinery(directory):
    return directory in MACHINERY or directory.startswith(tuple(root + "/" for root in MACHINERY))


def tooling(path):
    return path.startswith(TOOLING) and path not in TOOLING_EXCEPTIONS


def script_change(paths):
    return any(path.startswith(SCRIPT_INPUTS) or path in SCRIPT_FILES for path in paths)


def frontend_change(paths):
    return any(path.startswith(FRONTEND_INPUTS) or path in FRONTEND_FILES for path in paths)


def docs_only(paths):
    return bool(paths) and all(prose(path) for path in paths)


def module_tree(path):
    """A file inside the Go module's package trees: source, embedded data, or test fixtures.

    Nested modules, such as the harness projects, are read by other lanes rather than compiled here;
    native trees declare their own dependencies.
    """
    if not path.startswith(("lycaon/internal/", "lycaon/cmd/", "lycaon/test/")):
        return False
    if Path(path).name in {"go.mod", "go.sum"}:
        return False
    directories = Path(path).parent.relative_to("lycaon").parts
    return not any((ROOT / "lycaon" / Path(*directories[:depth]) / manifest).exists()
                   for depth in range(1, len(directories) + 1) for manifest in ("go.mod", "Cargo.toml"))


def broad_scope(paths):
    reasons = []
    for path in paths:
        if tooling(path):
            continue
        ordinary = (module_tree(path)
                    or path.startswith("lycaon-den/src/") and path.endswith((".ts", ".tsx", ".css"))
                    or prose(path) or native_dependencies(path) is not None)
        if path.startswith(SHARED) or not ordinary:
            reasons.append(path)
    return bool(reasons), reasons


def fast_lanes(scope, lanes):
    """A pull request's lanes. Prose alone leaves the static job, which checks its links, the only check;
    the Den suite runs when its inputs change. A scope without a base runs every lane."""
    from ci_policy.hosted_plan import lanes_of
    paths = scope.get("paths", [])
    if docs_only(paths):
        return set()
    if paths and not frontend_change(paths):
        return set(lanes) - (lanes_of(lanes, {"den"}) - lanes_of(lanes, {"always"}))
    return set(lanes)


def selected_lanes(scope, lanes):
    """The merge queue's lanes for a change: those declaring an area the change touches."""
    from ci_policy.hosted_plan import lanes_of
    return lanes_of(lanes, change_areas(scope))


def change_areas(scope):
    """The change areas, as lanes declare them in `changes`, that a scope touches. A broad change
    touches every area, and the scripts only when its paths reach them."""
    from ci_policy.hosted_plan import AREAS
    if scope["full"]:
        paths = scope.get("paths", [])
        return set(AREAS) if not paths or script_change(paths) else AREAS - {"scripts"}
    paths = [path for path in scope["paths"] if not prose(path) and not tooling(path)]
    native = [native_dependencies(path) for path in paths if native_dependencies(path) is not None]
    ordinary = [path for path in paths if native_dependencies(path) is None]
    # Repository contracts read source outside the package/import graph, so some lanes always run.
    areas = {"always", *(dependency["area"] for dependency in native),
             *("go" if p.startswith("lycaon/") else "den" for p in ordinary)}
    if script_change(scope["paths"]):
        areas.add("scripts")
    # Seccomp filters, syscall numbers and argument layouts differ by architecture.
    if any(confinement_source(p) for p in paths):
        areas.add("confinement")
    # The live differential compares the command language with the reference userlands.
    if any(command_language_source(p) for p in paths):
        areas.add("command-language")
    return areas


def confinement_source(path):
    """The sandbox, its helper, and the command supervisor's Linux process handling."""
    if path.startswith("lycaon/internal/confine/"):
        return True
    name = path.removeprefix("lycaon/internal/exec/")
    return name != path and "/" not in name and (name.startswith("supervisor") or "_linux" in name)


COMMAND_LANGUAGE_SOURCES = (
    "lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/",
    "lycaon/internal/commandsurface/",
    "lycaon/internal/commandprogram/",
    "lycaon/internal/tools/native/",
    "lycaon/test/commandconformance/",
    "lycaon/test/wiring/conformance/",
)


def command_language_source(path):
    """The command catalog, its parser and evaluator, its native utilities, and the conformance corpus."""
    return path.startswith(COMMAND_LANGUAGE_SOURCES)


def reverse_closure(graph, seeds):
    reached = set(seeds)
    while True:
        next_set = reached | {name for name, edges in graph.items() if set(edges) & reached}
        if next_set == reached:
            return reached
        reached = next_set


def owning_package(path, directories):
    """The nearest Go package at or above a file, which embeds it or reads it as a fixture."""
    for directory in Path(path).parents:
        if directory.as_posix() in directories:
            return directories[directory.as_posix()]
    return None


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
        if tooling(path):
            seeds |= {name for directory, name in directories.items() if machinery(directory)}
        if not path.startswith("lycaon/") or prose(path):
            continue
        dependency = native_dependencies(path)
        if dependency is not None:
            for prefix in dependency["go"]:
                matches = {name for directory, name in directories.items()
                           if directory == prefix or directory.startswith(prefix + "/")}
                if not matches:
                    return None
                seeds |= matches
            continue
        package = owning_package(path, directories)
        if package is None:
            return None
        seeds.add(package)
    reached = reverse_closure(graph, seeds)
    reached |= {name for directory, name in directories.items()
                if directory.startswith(("lycaon/test/contract/", "lycaon/test/smoke"))
                and not machinery(directory)}
    return sorted("./" + directory.removeprefix("lycaon/") for directory, name in directories.items()
                  if name in reached)
