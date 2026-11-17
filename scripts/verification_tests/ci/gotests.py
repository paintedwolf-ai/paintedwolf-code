"""Go test inventories and the Go test scopes that tasks, workflows and scripts declare.

Go sources are gofmt-formatted, so a top-level function starts at a column-zero `func` and ends at
the next column-zero `}`.
"""
from dataclasses import dataclass, field
from functools import cache
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile

from verification_tests.ci.workflows import ROOT, commands, plan, taskfile

MODULE = ROOT / "lycaon"
FUNCTION = re.compile(r"^func (?:\([^)]*\)\s*)?([A-Za-z_]\w*)", re.M)
SKIPPED = {"testdata", "vendor", "node_modules"}
TEST_NAME = re.compile(r"Test[A-Za-z0-9_]+")


def functions(source):
    """Top-level function names and bodies of one gofmt-formatted file."""
    found = {}
    for match in FUNCTION.finditer(source):
        end = source.find("\n}", match.end())
        found[match.group(1)] = source[match.end():len(source) if end < 0 else end]
    return found


@cache
def test_functions():
    """Test-file function bodies by package (`./internal/x`); nested modules and fixtures are excluded."""
    packages = {}
    for directory, children, files in os.walk(MODULE):
        path = Path(directory)
        children[:] = [child for child in children
                       if child not in SKIPPED and not (path / child / "go.mod").exists()]
        sources = [name for name in files if name.endswith("_test.go")]
        if sources:
            package = "./" + path.relative_to(MODULE).as_posix()
            bodies = packages.setdefault(package, {})
            for name in sorted(sources):
                bodies.update(functions((path / name).read_text()))
    if not packages:
        raise AssertionError("found no Go test functions")
    return packages


def file_tests(package, prefix):
    """Top-level Test functions in a package's test files whose names start with prefix."""
    directory = MODULE / package.removeprefix("./")
    return sorted(name for path in directory.glob(f"{prefix}*_test.go")
                  for name in functions(path.read_text()) if name.startswith("Test")
                  and not re.search(rf"^func \([^)]*\)\s*{name}\b", path.read_text(), re.M))


def live_tests():
    return {name for bodies in test_functions().values() for name in bodies if name.startswith("Test")}


@cache
def call_pattern(name):
    return re.compile(rf"(?<![\w.]){re.escape(name)}\s*\(")


def calls(body, name):
    return name in body and call_pattern(name).search(body) is not None


def reaching(roots):
    """Test functions per package that call any of `roots`, directly or through a package-local helper."""
    found = {}
    for package, bodies in test_functions().items():
        reached = {name for name, body in bodies.items() if any(calls(body, root) for root in roots)}
        grew = True
        while grew:
            grew = False
            for name, body in bodies.items():
                if name not in reached and any(calls(body, helper) for helper in reached):
                    reached.add(name)
                    grew = True
        tests = sorted(name for name in reached if name.startswith("Test"))
        if tests:
            found[package] = tests
    return found


def exported_functions(package):
    """Exported package-level functions of a package's non-test files, every build variant included."""
    directory = MODULE / package.removeprefix("./")
    return {name for path in directory.glob("*.go") if not path.name.endswith("_test.go")
            for name in functions(path.read_text())
            if name[0].isupper() and not re.search(rf"^func \([^)]*\)\s*{name}\b", path.read_text(), re.M)}


@dataclass
class Scope:
    """One Go test run: its package patterns, options and `-run` filter."""
    source: str
    packages: list
    options: list = field(default_factory=list)
    run: str = ""
    environment: dict = field(default_factory=dict)

    def covers_package(self, package):
        for pattern in self.packages:
            root = pattern.removesuffix("/...")
            if package == pattern or pattern.endswith("/...") and (package == root or package.startswith(root + "/")):
                return True
        return False

    def covers(self, package, test):
        return self.covers_package(package) and (not self.run or re.search(self.run, test) is not None)

    def named(self):
        return set(TEST_NAME.findall(self.run))

    def option(self, name):
        """The value of `--name value` or `-name=value`, or True for a bare flag."""
        for index, token in enumerate(self.options):
            if token == name:
                following = self.options[index + 1:index + 2]
                return following[0] if following and not following[0].startswith("-") else True
            if token.startswith(name + "="):
                return token.split("=", 1)[1]
        return None


def scope_from_argv(source, argv, environment=None):
    """A Go test scope from a `go-test-digest.sh` or `test:digest` command line."""
    arguments = argv[argv.index("--") + 1:] if "--" in argv else argv[1:]
    options = argv[:argv.index("--")] if "--" in argv else []
    packages = [token.rstrip("/") for token in arguments if token.startswith("./")]
    rest = [token for token in arguments if not token.startswith("./")]
    run = ""
    for index, token in enumerate(rest):
        if token == "-run" and index + 1 < len(rest):
            run = rest[index + 1]
        elif token.startswith("-run="):
            run = token.split("=", 1)[1]
    return Scope(source, packages, options + rest, run, environment or {})


def script_scopes(source, script):
    """Go test scopes in a shell script or task command."""
    found = []
    for argv in commands(script):
        digest = next((index for index, token in enumerate(argv)
                       if token.endswith("go-test-digest.sh") or token == "test:digest"), None)
        if digest is not None:
            found.append(scope_from_argv(source, argv[digest:]))
    return found


def task_scopes(name):
    task = taskfile()[name]
    script = "\n".join(command if isinstance(command, str) else "" for command in task.get("cmds", []))
    return script_scopes(f"Taskfile {name}", script)


def catalog_scopes(group):
    """Scopes of a plan group's Go recipes."""
    scopes = []
    for name in plan()["groups"][group]:
        recipe = plan()["go"][name]
        flags = recipe.get("flags", [])
        run = next((flag.split("=", 1)[1] for flag in flags if flag.startswith("-run=")), "")
        scopes.append(Scope(f"plan {name}", list(recipe["packages"]), [*recipe["options"], *flags], run))
    return scopes


def case_labels(script):
    """Patterns of every shell `case` arm, read with the shell's own tokenization."""
    lexer = shlex.shlex(script, posix=True, punctuation_chars=";&|()")
    lexer.whitespace_split = True
    lexer.commenters = "#"
    tokens = list(lexer)
    return {tokens[index] for index in range(1, len(tokens) - 1)
            if tokens[index + 1] == ")" and tokens[index - 1] in {"in", ";;"}}


def confine_legs():
    return case_labels((ROOT / "scripts/test-confine-linux.sh").read_text())


RECORDER = """import json, os, sys
with open(os.environ["CONFINE_RECORD"], "a") as record:
    record.write(json.dumps({"argv": sys.argv[1:], "environment": {
        key: value for key, value in os.environ.items()
        if key.startswith(("PW_", "LYCAON_"))}}) + "\\n")
"""


@cache
def confine_scopes(*legs):
    """The Go runs `scripts/test-confine-linux.sh` performs for `legs` (its defaults when empty),
    observed by running it on a stub Linux host."""
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        scripts, tools = root / "scripts", root / "tools"
        scripts.mkdir()
        tools.mkdir()
        shutil.copy2(ROOT / "scripts/test-confine-linux.sh", scripts)
        (scripts / "artifact-paths.sh").write_text(f'PW_BIN_DIR="{root / "bin"}"\n')
        (scripts / "record.py").write_text(RECORDER)
        (scripts / "go-test-digest.sh").write_text('exec python3 "$(dirname "$0")/record.py" "$@"\n')
        (root / "lycaon").mkdir()
        browser = root / "browser"
        browser.write_text("#!/bin/sh\n")
        browser.chmod(0o755)
        artifact = root / "desktop-artifact"
        artifact.write_text("fixture")
        stubs = {"uname": "echo Linux",
                 "go": 'out="$3"; printf \'#!/bin/sh\\necho "browser path=%s"\\n\' "' + str(browser)
                       + '" > "$out"; chmod +x "$out"'}
        for name, body in stubs.items():
            (tools / name).write_text(f"#!/bin/sh\n{body}\n")
            (tools / name).chmod(0o755)
        record = root / "record.jsonl"
        record.touch()
        environment = {**os.environ, "PATH": f"{tools}{os.pathsep}{os.environ['PATH']}",
                       "CONFINE_RECORD": str(record), "PW_LAUNCH_DESKTOP_ARTIFACT": str(artifact)}
        result = subprocess.run(["bash", str(scripts / "test-confine-linux.sh"), *legs],
                                capture_output=True, text=True, env=environment)
        if result.returncode:
            raise AssertionError(f"test-confine-linux.sh {legs}: {result.returncode}\n{result.stderr}")
        runs = [json.loads(line) for line in record.read_text().splitlines()]
    scopes = []
    for entry in runs:
        argv = ["go-test-digest.sh", *entry["argv"]]
        # Only what the script sets for the run, not what it inherited.
        assigned = {key: value for key, value in entry["environment"].items()
                    if environment.get(key) != value and key != "PW_BIN_DIR"}
        scope = scope_from_argv("scripts/test-confine-linux.sh", argv, assigned)
        scope.source = f"test-confine-linux.sh {scope.option('--name')}"
        scopes.append(scope)
    return tuple(scopes)
