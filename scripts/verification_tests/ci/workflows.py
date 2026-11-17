"""Parsed workflows, composite actions and the Taskfile, for the structure laws in this package.

Laws read structure and evaluate expressions; none matches workflow text.
"""
from functools import cache
import json
from pathlib import Path
import re
import shlex

import yaml

ROOT = Path(__file__).resolve().parents[3]
SECRET = re.compile(r"\bsecrets\.([A-Za-z_][A-Za-z0-9_]*)")
PINNED = re.compile(r"[^@\s]+@[0-9a-f]{40}")


def load(path):
    data = yaml.safe_load((ROOT / path).read_text())
    # YAML 1.1 reads the bare key `on` as true.
    if True in data:
        data["on"] = data.pop(True)
    return data


SEPARATORS = {";", "&&", "||", "|", "&", "(", ")", "{", "}", "!", "if", "then", "else", "elif", "fi",
              "do", "done", "while", "until", "exec", "time"}
ASSIGNMENT = re.compile(r"[A-Za-z_][A-Za-z0-9_]*=")


def commands(script):
    """Split a shell script into simple commands; quoting follows POSIX, control words separate."""
    found = []
    for line in script.replace("\\\n", " ").splitlines():
        lexer = shlex.shlex(line, posix=True, punctuation_chars=";&|()")
        lexer.whitespace_split = True
        lexer.commenters = "#"
        try:
            tokens = list(lexer)
        except ValueError:
            continue
        current = []
        for token in [*tokens, ";"]:
            if token in SEPARATORS:
                if current:
                    found.append(current)
                current = []
            elif current or not ASSIGNMENT.match(token):
                current.append(token)
    return found


def invocation(argv):
    """The program a command runs and its arguments: interpreters and `./task` are looked through."""
    program, rest = argv[0], argv[1:]
    if program.startswith("python") and rest[:1] == ["-m"]:
        return (rest[1], rest[2:]) if len(rest) > 1 else (program, rest)
    if program.startswith(("python", "bash", "sh")) or program in {"./task", "task"}:
        rest = [token for token in rest if token != "--"]
        return (rest[0], rest[1:]) if rest else (program, rest)
    return program, rest


def shell_array(script, name):
    """Elements of a literal `name=(...)` array assignment, read with the shell's tokenization."""
    lexer = shlex.shlex(script, posix=True, punctuation_chars="();")
    lexer.whitespace_split = True
    lexer.commenters = "#"
    tokens = list(lexer)
    for index, token in enumerate(tokens[:-1]):
        if token == f"{name}=" and tokens[index + 1] == "(":
            return tokens[index + 2:tokens.index(")", index + 2)]
    raise AssertionError(f"no {name} array")


def scalar(value):
    """A YAML scalar as the string GitHub passes on: booleans lowercased, null empty."""
    if isinstance(value, bool):
        return "true" if value else "false"
    return "" if value is None else str(value)


def strings(value):
    """Every scalar a node holds, keys excluded."""
    if isinstance(value, dict):
        for item in value.values():
            yield from strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)
    elif value is not None:
        yield str(value)


class Step:
    def __init__(self, raw):
        self.raw = raw
        self.id = raw.get("id", "")
        self.name = raw.get("name", "")
        self.uses = raw.get("uses", "")
        self.run = raw.get("run", "")
        self.condition = str(raw.get("if", ""))
        self.env = {key: scalar(value) for key, value in (raw.get("env") or {}).items()}
        self.inputs = {key: scalar(value) for key, value in (raw.get("with") or {}).items()}
        self.continues = raw.get("continue-on-error", False) is True

    def action(self):
        """The action's repository path without its ref: `actions/checkout`, `./.github/actions/x`."""
        return self.uses.split("@", 1)[0]

    def commands(self):
        """Each simple command the step's shell runs, as argv without leading assignments."""
        return commands(self.run)

    def invokes(self, program, *arguments):
        """Whether the step runs `program` (a script, module or Task target) with `arguments` among its own."""
        return any(name == program and all(argument in rest for argument in arguments)
                   for name, rest in map(invocation, self.commands()))

    def tasks(self):
        """Task targets the step runs through the repository's `./task` wrapper."""
        return {argv[1] for argv in self.commands()
                if argv[0] in {"./task", "task"} and len(argv) > 1 and not argv[1].startswith("-")}

    def modules(self):
        """Python modules and scripts the step runs."""
        found = set()
        for argv in self.commands():
            if argv[0].startswith("python"):
                rest = argv[1:]
                if rest[:1] == ["-m"] and len(rest) > 1:
                    found.add(rest[1])
                elif rest:
                    found.add(rest[0])
        return found

    def secrets(self):
        return {match for text in strings(self.raw) for match in SECRET.findall(text)}


class Job:
    def __init__(self, workflow, name, raw):
        self.workflow, self.name, self.raw = workflow, name, raw
        needs = raw.get("needs", [])
        self.needs = [needs] if isinstance(needs, str) else list(needs)
        self.condition = str(raw.get("if", ""))
        self.uses = raw.get("uses", "")
        environment = raw.get("environment")
        self.environment = environment.get("name") if isinstance(environment, dict) else environment
        self.permissions = raw.get("permissions")
        self.steps = [Step(step) for step in raw.get("steps", [])]
        self.continues = raw.get("continue-on-error", False) is True

    def ancestors(self):
        seen, pending = set(), list(self.needs)
        while pending:
            name = pending.pop()
            if name not in seen:
                seen.add(name)
                pending.extend(self.workflow.jobs[name].needs)
        return seen

    def secrets(self):
        return {match for text in strings(self.raw) for match in SECRET.findall(text)}

    def checkouts(self):
        return [step for step in self.steps if step.action() == "actions/checkout"]

    def artifacts(self, action):
        """Artifact names a job's `upload-artifact` or `download-artifact` steps name."""
        return [step.inputs["name"] for step in self.steps
                if step.action() == f"actions/{action}" and step.inputs.get("name")]

    def called(self):
        """The reusable workflow this job calls, if any."""
        return workflow(self.uses.removeprefix("./")) if self.uses.startswith("./.github/workflows/") else None


class Workflow:
    def __init__(self, path):
        self.path = path
        self.raw = load(path)
        self.name = self.raw.get("name", "")
        self.on = self.raw.get("on") or {}
        if isinstance(self.on, (str, list)):
            self.on = {event: {} for event in ([self.on] if isinstance(self.on, str) else self.on)}
        self.permissions = self.raw.get("permissions")
        self.jobs = {name: Job(self, name, raw) for name, raw in (self.raw.get("jobs") or {}).items()}

    def trigger(self, event):
        return self.on.get(event) or {}

    def dispatch_inputs(self):
        return self.trigger("workflow_dispatch").get("inputs") or {}


@cache
def workflow(path):
    return Workflow(path if "/" in path else f".github/workflows/{path}.yml")


def workflows():
    return [workflow(path.relative_to(ROOT).as_posix())
            for path in sorted((ROOT / ".github/workflows").glob("*.y*ml"))]


@cache
def action(name):
    raw = load(f".github/actions/{name}/action.yml")
    return [Step(step) for step in raw.get("runs", {}).get("steps", [])]


def actions():
    return sorted(path.parent.name for path in (ROOT / ".github/actions").glob("*/action.yml"))


@cache
def taskfile():
    return load("Taskfile.yml")["tasks"]


@cache
def plan():
    return json.loads((ROOT / "scripts/verification-plan.json").read_text())


TOKEN = re.compile(r"\s*(?:(?P<string>'(?:[^']|'')*')|(?P<number>-?\d+(?:\.\d+)?)"
                   r"|(?P<op>&&|\|\||==|!=|<=|>=|[!<>(),\[\]])"
                   r"|(?P<name>[A-Za-z_][A-Za-z0-9_-]*(?:\.(?:[A-Za-z_*][A-Za-z0-9_-]*|\*))*))")


TEMPLATE = re.compile(r"\$\{\{(.*?)\}\}", re.S)


def render(value, context, status="success"):
    """A workflow value with `${{ }}` segments: one whole segment keeps its type, mixed text becomes a string."""
    text = scalar(value)
    whole = TEMPLATE.fullmatch(text.strip())
    if whole:
        return evaluate(whole.group(1), context, status)
    return TEMPLATE.sub(lambda match: scalar(evaluate(match.group(1), context, status)), text)


def evaluate(expression, context, status="success"):
    """Evaluate a GitHub Actions expression; `status` answers success(), failure() and cancelled()."""
    text = expression.strip()
    whole = TEMPLATE.fullmatch(text)
    if whole:
        text = whole.group(1)
    tokens, position = [], 0
    while position < len(text):
        match = TOKEN.match(text, position)
        if not match or match.end() == position:
            if text[position:].strip():
                raise ValueError(f"unparsed expression at {text[position:]!r}")
            break
        kind = match.lastgroup
        tokens.append((kind, match.group(kind)))
        position = match.end()
    parser = _Parser(tokens, context, status)
    value = parser.disjunction()
    if parser.index != len(tokens):
        raise ValueError(f"trailing tokens in {expression!r}")
    return value


def truthy(value):
    return value not in (None, False, 0, "") and value == value


def _loose(value):
    return value.lower() if isinstance(value, str) else value


def _number(value):
    if value is None or isinstance(value, bool):
        return float(bool(value))
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        try:
            return float(value.strip() or 0)
        except ValueError:
            return float("nan")
    return float("nan")


def compare(left, right, operator):
    """GitHub's loose comparison: strings ignore case, differing types compare as numbers."""
    if type(left) is not type(right) or not isinstance(left, (str, int, float, bool)):
        left, right = _number(left), _number(right)
    else:
        left, right = _loose(left), _loose(right)
    return {"==": left == right, "!=": left != right, "<": left < right,
            "<=": left <= right, ">": left > right, ">=": left >= right}[operator]


class _Parser:
    def __init__(self, tokens, context, status):
        self.tokens, self.context, self.status, self.index = tokens, context, status, 0

    def peek(self):
        return self.tokens[self.index] if self.index < len(self.tokens) else (None, None)

    def take(self, value=None):
        token = self.peek()
        if value is not None and token[1] != value:
            raise ValueError(f"expected {value!r}, got {token[1]!r}")
        self.index += 1
        return token

    def disjunction(self):
        value = self.conjunction()
        while self.peek()[1] == "||":
            self.take()
            right = self.conjunction()
            value = value if truthy(value) else right
        return value

    def conjunction(self):
        value = self.comparison()
        while self.peek()[1] == "&&":
            self.take()
            right = self.comparison()
            value = right if truthy(value) else value
        return value

    def comparison(self):
        value = self.unary()
        while self.peek()[1] in {"==", "!=", "<", "<=", ">", ">="}:
            operator = self.take()[1]
            value = compare(value, self.unary(), operator)
        return value

    def unary(self):
        if self.peek()[1] == "!":
            self.take()
            return not truthy(self.unary())
        return self.primary()

    def primary(self):
        kind, value = self.take()
        if value == "(":
            inner = self.disjunction()
            self.take(")")
            return inner
        if kind == "string":
            return value[1:-1].replace("''", "'")
        if kind == "number":
            return float(value) if "." in value else int(value)
        if kind != "name":
            raise ValueError(f"unexpected token {value!r}")
        if self.peek()[1] == "(":
            self.take("(")
            arguments = []
            while self.peek()[1] != ")":
                arguments.append(self.disjunction())
                if self.peek()[1] == ",":
                    self.take(",")
            self.take(")")
            return self.call(value, arguments)
        if value in {"true", "false", "null"}:
            return {"true": True, "false": False, "null": None}[value]
        result = self.lookup(value)
        while self.peek()[1] == "[":
            self.take("[")
            key = self.disjunction()
            self.take("]")
            result = result.get(key) if isinstance(result, dict) else None
        return result

    def lookup(self, path):
        parts = path.split(".")
        values = [self.context]
        for part in parts:
            if part == "*":
                values = [item for value in values for item in
                          (value.values() if isinstance(value, dict) else value if isinstance(value, list) else [])]
                continue
            values = [value.get(part) if isinstance(value, dict) else None for value in values]
        return values if "*" in parts else values[0]

    def call(self, name, arguments):
        if name in {"success", "failure", "cancelled", "always"}:
            return name == "always" or self.status == name
        if name == "contains":
            haystack, needle = arguments
            if isinstance(haystack, list):
                return any(compare(item, needle, "==") for item in haystack)
            return str(_loose(needle)) in str(_loose(haystack))
        if name == "startsWith":
            return str(_loose(arguments[0])).startswith(str(_loose(arguments[1])))
        if name == "endsWith":
            return str(_loose(arguments[0])).endswith(str(_loose(arguments[1])))
        if name == "format":
            template, *values = arguments
            return re.sub(r"\{(\d+)\}", lambda match: str(values[int(match.group(1))]), template)
        if name == "fromJSON":
            return json.loads(arguments[0])
        if name == "toJSON":
            return json.dumps(arguments[0])
        raise ValueError(f"unsupported expression function {name}")
