#!/usr/bin/env python3
"""Report tests whose verdict depends on how long something took.

A test that sleeps a fixed span and then asserts, compares a real-clock reading,
or gives a wait a deadline that raises passes on a host that is fast enough and
fails on one that is not. Tests wait on the event itself, pin or inject the
clock, or use the framework's fake time; the run's backstop ends a real hang.

Go tests are checked by scripts/testsleep. This covers Python, TypeScript and
Rust tests.
"""

from __future__ import annotations

import argparse
import ast
from pathlib import Path
import re
import subprocess
import sys

SKIPPED_DIRECTORIES = {"node_modules", "vendor", "third_party", "testdata", "target", "dist"}


def tracked(root):
    listing = subprocess.run(["git", "-C", str(root), "ls-files", "-z"], capture_output=True, check=True)
    for name in listing.stdout.decode().split("\0"):
        if name and not SKIPPED_DIRECTORIES & set(Path(name).parts[:-1]):
            yield Path(name)


# Python ------------------------------------------------------------------------------------------

PYTHON_TEST_DIRECTORIES = {"verification_tests", "tests"}
CLOCK_READS = {("time", "monotonic"), ("time", "time"), ("time", "perf_counter"), ("time", "monotonic_ns"),
               ("time", "time_ns"), ("time", "perf_counter_ns"), ("datetime", "now"), ("datetime", "utcnow")}
SLEEPS = {("time", "sleep"), ("asyncio", "sleep")}
ORDERING_ASSERTIONS = {"assertLess", "assertLessEqual", "assertGreater", "assertGreaterEqual",
                       "assertAlmostEqual", "assertNotAlmostEqual"}
# Waits that raise or report failure when their timeout runs out; for WAITS a numeric first argument is the timeout.
WAITS = {"wait", "join"}
TIMEOUT_KEYWORD_WAITS = WAITS | {"result", "communicate", "acquire", "get", "create_connection", "urlopen"}
SUBPROCESS_WAITS = {"run", "call", "check_call", "check_output"}


def is_python_test(path):
    return path.suffix == ".py" and (path.name.startswith("test_") or path.stem.endswith("_test")
                                     or path.name == "conftest.py"
                                     or bool(PYTHON_TEST_DIRECTORIES & set(path.parts[:-1])))


def dotted(node):
    """The last two names of a call target: time.sleep → ("time", "sleep"), sleep → (None, "sleep")."""
    if isinstance(node, ast.Attribute):
        owner = node.value
        owner_name = owner.attr if isinstance(owner, ast.Attribute) else owner.id if isinstance(owner, ast.Name) else None
        return owner_name, node.attr
    if isinstance(node, ast.Name):
        return None, node.id
    return None, None


class PythonFile(ast.NodeVisitor):
    def __init__(self, path, tree):
        self.path = path
        self.findings = []
        self.imported = {}
        for node in ast.walk(tree):
            if isinstance(node, ast.ImportFrom) and node.module in ("time", "asyncio", "datetime"):
                for alias in node.names:
                    self.imported[alias.asname or alias.name] = node.module
        self.loops = [0]
        self.clocked = [set()]
        self.expiry = [0]

    def report(self, node, kind):
        self.findings.append(f"{self.path}:{node.lineno}: {kind}")

    def qualified(self, func):
        owner, name = dotted(func)
        if owner is None and name in self.imported:
            owner = self.imported[name]
        return owner, name

    def reads_clock(self, node):
        for child in ast.walk(node):
            if isinstance(child, ast.Call) and self.qualified(child.func) in CLOCK_READS:
                return True
            if isinstance(child, ast.Name) and child.id in self.clocked[-1]:
                return True
        return False

    def visit_function(self, node):
        self.loops.append(0)
        self.clocked.append(set())
        self.expiry.append(0)
        self.generic_visit(node)
        self.loops.pop()
        self.clocked.pop()
        self.expiry.pop()

    visit_FunctionDef = visit_AsyncFunctionDef = visit_Lambda = visit_function

    def visit_loop(self, node):
        self.loops[-1] += 1
        self.generic_visit(node)
        self.loops[-1] -= 1

    visit_While = visit_For = visit_AsyncFor = visit_loop

    def visit_Assign(self, node):
        if self.reads_clock(node.value):
            for target in node.targets:
                for name in ast.walk(target):
                    if isinstance(name, ast.Name):
                        self.clocked[-1].add(name.id)
        self.generic_visit(node)

    def visit_Compare(self, node):
        if self.reads_clock(node):
            self.report(node, "compares a real-clock reading: a deadline or an elapsed time")
        else:
            self.generic_visit(node)

    def visit_Expr(self, node):
        value = node.value.value if isinstance(node.value, ast.Await) else node.value
        if isinstance(value, ast.Call) and self.sleeps(value):
            if not self.loops[-1] and not (value.args and is_number(value.args[0]) and value.args[0].value == 0):
                self.report(node, "fixed sleep")
            for part in (*value.args, *value.keywords):
                self.visit(part)
            return
        self.generic_visit(node)

    def sleeps(self, call):
        """time.sleep, asyncio.sleep, or an Event wait whose answer is discarded."""
        return (self.qualified(call.func) in SLEEPS or isinstance(call.func, ast.Attribute)
                and call.func.attr == "wait" and len(call.args) == 1 and not call.keywords and is_number(call.args[0]))

    def visit_With(self, node):
        expects = any(self.expects_expiry(item.context_expr) for item in node.items)
        self.expiry[-1] += expects
        self.generic_visit(node)
        self.expiry[-1] -= expects

    visit_AsyncWith = visit_With

    def visit_Try(self, node):
        handles = any(handler.type is not None and "Timeout" in ast.unparse(handler.type) for handler in node.handlers)
        self.expiry[-1] += handles
        for statement in node.body:
            self.visit(statement)
        self.expiry[-1] -= handles
        for part in (*node.handlers, *node.orelse, *node.finalbody):
            self.visit(part)

    @staticmethod
    def expects_expiry(expression):
        return (isinstance(expression, ast.Call) and dotted(expression.func)[1] in ("assertRaises", "assertRaisesRegex")
                and any("Timeout" in ast.unparse(argument) for argument in expression.args))

    def visit_Call(self, node):
        owner, name = self.qualified(node.func)
        if name in ORDERING_ASSERTIONS and any(self.reads_clock(argument) for argument in node.args):
            self.report(node, "asserts on a real-clock reading")
        elif not self.expiry[-1] and self.raising_deadline(node, owner, name):
            self.report(node, "wait has a deadline")
        self.generic_visit(node)

    @staticmethod
    def raising_deadline(node, owner, name):
        keyword = next((k.value for k in node.keywords if k.arg == "timeout"), None)
        if keyword is None and name in WAITS and isinstance(node.func, ast.Attribute) and node.args and is_number(node.args[0]):
            keyword = node.args[0]
        if keyword is None or (isinstance(keyword, ast.Constant) and keyword.value is None):
            return False
        if owner == "subprocess":
            return name in SUBPROCESS_WAITS
        return isinstance(node.func, ast.Attribute) and name in TIMEOUT_KEYWORD_WAITS or name in ("urlopen", "create_connection")


def is_number(node):
    return isinstance(node, ast.Constant) and type(node.value) in (int, float)


def python_findings(path, text):
    try:
        tree = ast.parse(text, filename=str(path))
    except SyntaxError as error:
        return [f"{path}:{error.lineno}: does not parse: {error.msg}"]
    visitor = PythonFile(path, tree)
    visitor.visit(tree)
    return visitor.findings


# TypeScript and Rust -------------------------------------------------------------------------------

def strip_comments_and_strings(text, *, rust):
    """Blank comments and string bodies, keeping line structure, so braces and calls are read from code alone."""
    out = []
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        two = text[i:i + 2]
        if two == "//":
            j = text.find("\n", i)
            j = n if j < 0 else j
            out.append(" " * (j - i))
            i = j
        elif two == "/*":
            j = text.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append(re.sub(r"[^\n]", " ", text[i:j]))
            i = j
        elif c in "\"'`" and not (rust and c == "'" and not re.match(r"'(\\.|[^\\'])'", text[i:i + 4])):
            j = i + 1
            while j < n and text[j] != c:
                j += 2 if text[j] == "\\" else 1
            j = min(j + 1, n)
            out.append(c + re.sub(r"[^\n]", " ", text[i + 1:j - 1]) + c)
            i = j
        else:
            out.append(c)
            i += 1
    return "".join(out)


CLOSURE_HEAD = re.compile(r"(=>|\|[^|{};]*\||\basync(\s+move)?)\s*$")


def blocks(code, loop_words):
    """For each offset, whether it sits inside a loop body of its own function.

    A loop keyword owns the next brace opened at its own parenthesis depth, so closures
    and object literals inside a loop condition are not its body.
    """
    keywords = {m.start() for m in re.finditer(r"\b(" + "|".join(loop_words) + r")\b", code)}
    functions = {m.start() for m in re.finditer(r"\b(fn|function)\b", code)}
    inside = bytearray(len(code))
    stack, loops, bodies, parens = [], [], [], 0
    for index, char in enumerate(code):
        if index in keywords:
            loops.append(parens)
        elif index in functions:
            bodies.append(parens)
        if char in "([":
            parens += 1
        elif char in ")]":
            parens = max(0, parens - 1)
        elif char == "{":
            loop = bool(loops) and loops[-1] == parens
            function = bool(bodies) and bodies[-1] == parens or bool(CLOSURE_HEAD.search(code[max(0, index - 120):index]))
            if loop:
                loops.pop()
            if bool(bodies) and bodies[-1] == parens:
                bodies.pop()
            stack.append(loop or (bool(stack) and stack[-1] and not function))
        elif char == "}" and stack:
            stack.pop()
        elif char == ";":
            loops = [depth for depth in loops if depth < parens]
            bodies = [depth for depth in bodies if depth < parens]
        inside[index] = bool(stack) and stack[-1]
    return inside


def line_of(code, offset):
    return code.count("\n", 0, offset) + 1


TS_TEST = re.compile(r"\.(test|spec)\.(ts|tsx|mts|js|mjs)$")
TS_TEST_DIRECTORIES = {"e2e", "test", "tests"}
TS_SOURCE = re.compile(r"\.(ts|tsx|mts|js|mjs)$")
TS_LOOP = ("for", "while", "do")
TS_SLEEPS = re.compile(r"\bsetTimeout\s*\(\s*\w+\s*,\s*(?!0\s*\))[\w.]+|\bwaitForTimeout\s*\(|\bbrowser\.pause\s*\(|"
                       r"\b(sleep|delay|wait)\s*\(\s*(?!0\s*\))\d")
TS_CLOCK = re.compile(r"\b(Date\.now|performance\.now)\s*\(\s*\)")
# Upper bounds and tolerances: a slow host can push an elapsed time past them. A lower bound only
# fails when the code under test is early.
TS_ORDERING = re.compile(r"expect\s*\(((?:(?!expect\s*\()[^;])*?)\)\s*\.\s*(not\s*\.\s*)?"
                         r"(toBeCloseTo|toBeLessThan|toBeLessThanOrEqual)\b")


TS_RACE = re.compile(r"\bPromise\.race\s*\(\s*\[[^\]]*\bsetTimeout\s*\(")
TS_HYBRID = re.compile(r"\bshouldAdvanceTime\s*:\s*true\b")
TS_DEADLINE = re.compile(r"\b(Date|performance)\.now\s*\(\s*\)\s*\+")


def is_typescript_test(path):
    return bool(TS_TEST.search(path.name)) or (TS_SOURCE.search(path.name) is not None
                                              and not path.name.endswith(".d.ts")
                                              and bool(TS_TEST_DIRECTORIES & set(path.parts[:-1])))


TS_FAKE_CLOCK = re.compile(r"\b(vi|jest)\.(useFakeTimers|setSystemTime)\s*\(|\bvi\.spyOn\s*\(\s*Date\b|\bpage\.clock\.")
# A fake's response delay that no verdict waits on carries this marker on its line or the line above.
LATENCY_MARKER = "// injected latency:"


def typescript_findings(path, text):
    code = strip_comments_and_strings(text, rust=False)
    fake = bool(TS_FAKE_CLOCK.search(code))
    findings = [f"{path}:{line_of(code, m.start())}: {kind}" for pattern, kind in (
        (TS_RACE, "races a real timer"), (TS_HYBRID, "fake clock follows real time"),
        (None if fake else TS_DEADLINE, "deadline on the real clock")) if pattern for m in pattern.finditer(code)]
    return findings + timer_findings(path, text, code, fake)


def marked_latency(lines, number):
    return any(LATENCY_MARKER in line for line in lines[max(0, number - 2):number])


def timer_findings(path, text, code, fake):
    if fake:
        sleeps = []
    else:
        loops = blocks(code, TS_LOOP)
        lines = text.splitlines()
        sleeps = [m.start() for m in TS_SLEEPS.finditer(code)
                  if not loops[m.start()] and not marked_latency(lines, line_of(code, m.start()))]
    findings = [f"{path}:{line_of(code, offset)}: fixed sleep" for offset in sleeps]
    clocked = set(re.findall(r"\b(\w+)\s*=(?!=)[^;,\n]*?\b(?:Date\.now|performance\.now)\s*\(", code))
    clock_names = re.compile(TS_CLOCK.pattern + ("|\\b(" + "|".join(clocked) + ")\\b" if clocked else ""))
    for match in TS_ORDERING.finditer(code):
        if clock_names.search(match.group(1)):
            findings.append(f"{path}:{line_of(code, match.start())}: asserts on a real-clock reading")
    return findings


RUST_LOOP = ("loop", "while", "for")
RUST_SLEEPS = re.compile(r"\b(thread::sleep|time::sleep)\s*\(|(?<![\w:.])sleep\s*\(\s*Duration")
RUST_DEADLINES = re.compile(r"\b(recv_timeout|wait_timeout|future_with_timeout)\s*\("
                            r"|\btime::timeout\s*\(|\bInstant::now\s*\(\s*\)\s*\+")
RUST_CLOCK = re.compile(r"\.elapsed\s*\(\s*\)|\bInstant::now\s*\(\s*\)")
RUST_ORDERING = re.compile(r"\bassert(_eq|_ne)?!\s*\(|\bif\b|\bwhile\b|[<>]=?")
RUST_PAUSED = re.compile(r"start_paused\s*=\s*true")
RUST_TEST_ATTRIBUTE = r"#\[cfg\((?:test|all\(test\b[^\]]*)\)\]\s*((?:#\[[^\]]*\]\s*)*)(?:pub(?:\([^)]*\))?\s+)?mod\s+(\w+)\s*"
RUST_INLINE_TESTS = re.compile(RUST_TEST_ATTRIBUTE + r"\{")
RUST_TEST_MODULES = re.compile(RUST_TEST_ATTRIBUTE + r";")


def is_rust_test_file(path):
    return ("tests" in path.parts[:-1] or "test_support" in path.parts[:-1] or path.stem in ("tests", "test_support")
            or path.stem.startswith("tests_") or path.stem.endswith(("_test", "_tests")))


def rust_test_modules(path, text):
    """The files that a source file's out-of-line `#[cfg(test)] mod name;` declarations load."""
    code = strip_comments_and_strings(text, rust=True)
    directory = path.parent if path.stem in ("mod", "lib", "main") else path.parent / path.stem
    for match in RUST_TEST_MODULES.finditer(code):
        attribute = re.search(r'#\[path\s*=\s*"([^"]+)"\]', text[match.start(1):match.end(1)])
        if attribute:
            yield path.parent / attribute.group(1)
        else:
            yield directory / f"{match.group(2)}.rs"
            yield directory / match.group(2) / "mod.rs"


def closing_brace(code, opening):
    depth = 0
    for index in range(opening, len(code)):
        depth += {"{": 1, "}": -1}.get(code[index], 0)
        if depth == 0:
            return index
    return len(code)


def rust_test_regions(path, code, whole):
    if whole or is_rust_test_file(path):
        return [(0, len(code))]
    return [(m.end() - 1, closing_brace(code, m.end() - 1)) for m in RUST_INLINE_TESTS.finditer(code)]


def paused(code, offset):
    """Whether the test function around an offset runs on tokio's paused clock."""
    function = code.rfind("fn ", 0, offset)
    return function >= 0 and bool(RUST_PAUSED.search(code, max(0, function - 200), function)) \
        or "time::pause(" in code[function:offset]


def paused_helper(code, offset):
    """Whether the helper around a tokio sleep is called only from tests on the paused clock."""
    definition = list(re.finditer(r"\bfn\s+(\w+)", code[:offset]))
    if not definition:
        return False
    name = definition[-1].group(1)
    calls = [m.start() for m in re.finditer(r"\b" + name + r"\s*\(", code) if m.start() != definition[-1].start(1)]
    return bool(calls) and all(paused(code, call) for call in calls)


def rust_findings(path, text, whole=False):
    code = strip_comments_and_strings(text, rust=True)
    regions = rust_test_regions(path, code, whole)
    if not regions:
        return []
    test = bytearray(len(code))
    for start, end in regions:
        test[start:end] = b"\1" * (end - start)
    loops = blocks(code, RUST_LOOP)
    findings = []
    for match in RUST_SLEEPS.finditer(code):
        if test[match.start()] and not loops[match.start()] and not paused(code, match.start()) \
                and not (match.group(1) == "time::sleep" and paused_helper(code, match.start())):
            findings.append((match.start(), "fixed sleep"))
    for match in RUST_DEADLINES.finditer(code):
        if test[match.start()] and not paused(code, match.start()):
            findings.append((match.start(), "wait has a deadline"))
    offset = 0
    for line in code.splitlines(keepends=True):
        if test[offset] and RUST_CLOCK.search(line) and RUST_ORDERING.search(RUST_CLOCK.sub("", line)) \
                and not RUST_DEADLINES.search(line):
            findings.append((offset, "compares a real-clock reading: a deadline or an elapsed time"))
        offset += len(line)
    return [f"{path}:{line_of(code, at)}: {kind}" for at, kind in sorted(findings)]


# ---------------------------------------------------------------------------------------------------

def findings_for(path, text, rust_test_module=False):
    if is_python_test(path):
        return python_findings(path, text)
    if is_typescript_test(path):
        return typescript_findings(path, text)
    if path.suffix == ".rs":
        return rust_findings(path, text, rust_test_module)
    return []


def scan(root, paths=None):
    root = Path(root)
    paths = sorted(path for path in (paths if paths is not None else tracked(root)) if (root / path).is_file())
    texts = {path: (root / path).read_text(errors="replace") for path in paths}
    modules = {module for path in paths if path.suffix == ".rs" for module in rust_test_modules(path, texts[path])}
    found = []
    for path in paths:
        found += findings_for(path, texts[path], path in modules)
    return found


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=Path(__file__).resolve().parents[2], type=Path)
    parser.add_argument("paths", nargs="*", type=Path)
    args = parser.parse_args(argv)
    found = scan(args.root, args.paths or None)
    for finding in found:
        print(finding)
    if found:
        print(f"{len(found)} timing dependence(s) in tests: wait on the event, pin or inject the clock, "
              "or use fake time", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
