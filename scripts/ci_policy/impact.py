"""Conservative change scope shared by pull requests and merge groups.

Files in a Go package's tree, CI tooling, and documentation prose are narrowed.
Generated contracts, configuration, dependencies, build inputs, and unknown paths
widen the gate. Test imports participate in reverse reachability; contracts and
smoke tests run unconditionally.
"""

import json
import os
from pathlib import Path
import subprocess

from ci_policy.native_impact import dependencies as native_dependencies

ROOT = Path(__file__).resolve().parents[2]
SHARED = (".github/", "scripts/", "dependencies/", "schemas/", "docs/openapi",
          "lycaon/config/", "lycaon/pkg/", "lycaon/vulndb")
# CI's own logic and tests: no product test reads them, and the repository contracts and runner lane check them.
TOOLING = (".github/workflows/", "scripts/ci_policy/", "scripts/verification_tests/")
# These decide how the lanes themselves run, so a change must exercise every lane.
TOOLING_EXCEPTIONS = {".github/workflows/ci.yml", ".github/workflows/verification.yml",
                      "scripts/ci_policy/hosted_execution.py", "scripts/ci_policy/hosted_plan.py"}
# Prose that no build, fixture, or prompt reads; the repository contracts every scope runs check it.
# CHANGELOG.md is not among them: Den embeds it.
PROSE = {"AGENTS.md", "CODE_OF_CONDUCT.md", "CONTRIBUTING.md", "README.md", "REPORTING.md", "SECURITY.md",
         "lycaon/AGENTS.md", "lycaon-den/AGENTS.md"}


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


def prose(path):
    return path in PROSE or path.startswith("docs/") and path.endswith(".md") and not path.startswith(SHARED)


def tooling(path):
    return path.startswith(TOOLING) and path not in TOOLING_EXCEPTIONS


def module_tree(path):
    """A file inside the Go module's package trees: source, embedded data, or test fixtures.

    Nested modules, such as the harness projects, are read by other lanes rather than compiled here;
    native trees declare their own dependencies.
    """
    if not path.startswith(("lycaon/internal/", "lycaon/cmd/", "lycaon/test/")) or "/native/" in path:
        return False
    if Path(path).name in {"go.mod", "go.sum"}:
        return False
    directories = Path(path).parent.relative_to("lycaon").parts
    return not any((ROOT / "lycaon" / Path(*directories[:depth]) / "go.mod").exists()
                   for depth in range(1, len(directories) + 1))


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


def selected_lanes(scope, lanes):
    if scope["full"]:
        return set(lanes)
    paths = [path for path in scope["paths"] if not prose(path) and not tooling(path)]
    native = [native_dependencies(path) for path in paths if native_dependencies(path) is not None]
    ordinary = [path for path in paths if native_dependencies(path) is None]
    areas = {"go" if p.startswith("lycaon/") else "den" for p in ordinary}
    # Repository contracts read source outside the package/import graph.
    chosen = {"limits", "contracts", "integration-build", "behavior"}
    for dependency in native:
        chosen |= dependency["lanes"]
    if any(tooling(path) for path in scope["paths"]):
        chosen.add("runner")
    if "go" in areas:
        chosen |= {"behavior", "lint", "vulnerabilities", "command-scale"}
    if "den" in areas:
        # Any Den source can change the DOM that WebKitGTK answers screen reader queries about.
        chosen |= {"frontend", "webkit-gtk"}
    if any(confinement_source(p) for p in paths):
        # Seccomp filters, syscall numbers and argument layouts differ by architecture.
        chosen.add("confinement-arm64")
    if any(command_language_source(p) for p in paths):
        # The live differential compares the command language with the reference userlands.
        chosen.add("reference-userlands")
    return chosen & set(lanes)


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
                if directory.startswith(("lycaon/test/contract/", "lycaon/test/smoke"))}
    return sorted("./" + directory.removeprefix("lycaon/") for directory, name in directories.items()
                  if name in reached)
