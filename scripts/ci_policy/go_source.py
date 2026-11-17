"""A lexical reading of Go test sources: build constraints, functions, and the conditions under which a
test skips. It reads files only and runs no Go tooling, so the static job can judge tiers in seconds."""

from dataclasses import dataclass, field
from pathlib import Path
import re

GOOS = {"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl",
        "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}
GOARCH = {"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le", "mipsle", "ppc64", "ppc64le",
          "riscv64", "s390x", "wasm"}
UNIX = {"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "linux", "netbsd", "openbsd",
        "solaris"}
SKIP_METHODS = {"Skip", "Skipf", "SkipNow"}
UNKNOWN = ("unknown",)

TOKEN = re.compile(r"""
    (?P<space>[ \t\r]+)
  | (?P<newline>\n)
  | (?P<comment>//[^\n]*|/\*.*?\*/)
  | (?P<raw>`[^`]*`)
  | (?P<string>"(?:\\.|[^"\\\n])*")
  | (?P<rune>'(?:\\.|[^'\\\n])+')
  | (?P<number>\.?[0-9][0-9a-zA-Z_.]*(?:[eEpP][+-][0-9_]+)?)
  | (?P<ident>[A-Za-z_][A-Za-z0-9_]*)
  | (?P<op>\.\.\.|<<=|>>=|&\^=|&&|\|\||<-|\+\+|--|==|!=|<=|>=|:=|<<|>>|&\^|[-+*/%&|^]=|[-+*/%&|^<>=!(){}\[\],;:.~])
""", re.S | re.X)
# A newline ends a statement after these tokens (the Go specification's semicolon rule).
ENDS_STATEMENT = {"break", "continue", "fallthrough", "return", "++", "--", ")", "]", "}"}


@dataclass
class Token:
    kind: str
    text: str
    line: int
    value: str = None


def tokens(source):
    """Go tokens with the specification's implicit semicolons; a string token keeps its source text, so it
    never reads as punctuation, and carries its value."""
    result, line, position = [], 1, 0
    while position < len(source):
        match = TOKEN.match(source, position)
        if match is None:
            position += 1
            continue
        kind, text = match.lastgroup, match.group()
        position = match.end()
        newline = kind == "newline" or (kind == "comment" and "\n" in text)
        if newline and result and (result[-1].kind in {"ident", "string", "number", "rune"}
                                   or result[-1].text in ENDS_STATEMENT):
            result.append(Token("op", ";", line))
        line += text.count("\n")
        if kind in {"space", "newline", "comment"}:
            continue
        if kind == "raw":
            result.append(Token("string", text, line, text[1:-1]))
        elif kind == "string":
            result.append(Token("string", text, line, bytes(text[1:-1], "utf-8").decode("unicode_escape", "replace")))
        else:
            result.append(Token(kind, text, line))
    return result


def constraint(source):
    """The `//go:build` expression of a file, or None."""
    for line in source.splitlines():
        stripped = line.strip()
        if stripped.startswith("//go:build "):
            return stripped[len("//go:build "):]
        if stripped.startswith("package "):
            return None
    return None


def satisfied(expression, tags):
    """Evaluate a build expression against the set of satisfied tags."""
    parts = re.findall(r"!|&&|\|\||\(|\)|[\w.]+", expression)
    position = 0

    def term():
        nonlocal position
        token = parts[position]
        position += 1
        if token == "!":
            return not term()
        if token == "(":
            value = alternatives()
            position += 1
            return value
        return token in tags or re.fullmatch(r"go1\.\d+", token) is not None

    def conjunction():
        nonlocal position
        value = term()
        while position < len(parts) and parts[position] == "&&":
            position += 1
            value = term() and value
        return value

    def alternatives():
        nonlocal position
        value = conjunction()
        while position < len(parts) and parts[position] == "||":
            position += 1
            value = conjunction() or value
        return value

    return alternatives()


def name_matches(name, goos, goarch):
    """Go's file-name constraint: a trailing _GOOS, _GOARCH or _GOOS_GOARCH after the first element."""
    stem = name.split(".", 1)[0]
    if "_" not in stem:
        return True
    elements = stem.split("_")[1:]
    if elements and elements[-1] == "test":
        elements = elements[:-1]
    if len(elements) >= 2 and elements[-2] in GOOS and elements[-1] in GOARCH:
        return matches_os(elements[-2], goos) and elements[-1] == goarch
    if elements and elements[-1] in GOOS:
        return matches_os(elements[-1], goos)
    if elements and elements[-1] in GOARCH:
        return elements[-1] == goarch
    return True


def matches_os(declared, goos):
    return declared == goos or declared == "linux" and goos == "android" or declared == "darwin" and goos == "ios"


def build_tags(goos, goarch, tags):
    return {goos, goarch, "gc", "cgo", *tags, *(["unix"] if goos in UNIX else [])}


FUNCTION = re.compile(r"^func (\([^)]*\) )?(\w+)")
TEST = re.compile(r"^func (Test(?:[^a-z(]\w*)?|Fuzz(?:[^a-z(]\w*)?)\(\w+ \*testing\.([TF])\) \{")
IMPORT = re.compile(r'^\s*(?:import\s+)?(?:(\w+|\.)\s+)?"([^"]+)"')
CONSTANT = re.compile(r'^(?:const\s+|\t)(\w+)(?:\s+string)?\s*=\s*"((?:\\.|[^"\\])*)"\s*(?://.*)?$')
CALL = re.compile(r"(\w+)\(")
SKIP_TEXT = re.compile(r"\.Skip(?:f|Now)?\(")
BARE_RETURN = re.compile(r"^\s+return$", re.M)


class Function:
    """A top-level function; its body is tokenized only when a judgment needs it."""

    def __init__(self, name, receiver, signature, text, file, line):
        self.name, self.receiver, self.signature, self.text, self.file, self.line = \
            name, receiver, signature, text, file, line
        self._body = None
        self.calls = set(CALL.findall(text))
        self.skips = SKIP_TEXT.search(text) is not None

    @property
    def body(self):
        if self._body is None:
            self._body = tokens(self.text)
        return self._body


@dataclass
class GoFile:
    path: Path
    directory: str
    package: str
    build: str
    imports: dict
    constants: dict
    functions: list = field(default_factory=list)

    def builds(self, goos, goarch, tags):
        if not name_matches(self.path.name, goos, goarch):
            return False
        return self.build is None or satisfied(self.build, build_tags(goos, goarch, tags))


def matching(items, start, opening, closing):
    depth = 0
    for index in range(start, len(items)):
        if items[index].text == opening:
            depth += 1
        elif items[index].text == closing:
            depth -= 1
            if depth == 0:
                return index
    raise ValueError(f"unbalanced {opening} at line {items[start].line}")


def parse_file(path, directory):
    """Read declarations from gofmt layout, which the static checks enforce: a top-level declaration starts
    in column 0, and a multi-line body closes with `}` alone in column 0."""
    source = path.read_text(encoding="utf-8", errors="replace")
    lines = source.split("\n")
    package = next((line.split()[1] for line in lines if line.startswith("package ")), "")
    result = GoFile(path, directory, package, constraint(source), {}, {})
    block = None
    raw = False
    index = 0
    while index < len(lines):
        line = lines[index]
        if raw or "`" in line:
            raw = ends_in_raw(line, raw)
            if raw or not line.startswith("func "):
                index += 1
                continue
        if block:
            if line.startswith(")"):
                block = None
            elif block == "import" and (match := IMPORT.match(line)):
                result.imports[match.group(1) or match.group(2).rsplit("/", 1)[-1]] = match.group(2)
            elif block == "const" and (match := CONSTANT.match(line)):
                result.constants[match.group(1)] = decode(match.group(2))
        elif line in {"import (", "const ("}:
            block = line.split()[0]
        elif line.startswith("import ") and (match := IMPORT.match(line)):
            result.imports[match.group(1) or match.group(2).rsplit("/", 1)[-1]] = match.group(2)
        elif line.startswith("const ") and (match := CONSTANT.match(line)):
            result.constants[match.group(1)] = decode(match.group(2))
        elif line.startswith("func ") and (match := FUNCTION.match(line)):
            start = index
            while lines[index].endswith(("(", ",")):
                index += 1
            if not lines[index].endswith(("{", "}")):
                index += 1
                continue
            if lines[index].endswith("{"):
                stop, inside = index + 1, False
                while inside or lines[stop] != "}":
                    if inside or "`" in lines[stop]:
                        inside = ends_in_raw(lines[stop], inside)
                    stop += 1
                text = "\n".join(lines[index + 1:stop])
                signature = "\n".join(lines[start:index + 1])
                index = stop
            else:
                signature = "\n".join(lines[start:index + 1])
                text = one_line_body(signature)
            result.functions.append(Function(match.group(2), match.group(1) is not None, signature, text,
                                             result, start + 1))
        index += 1
    return result


def ends_in_raw(line, raw):
    """Whether a raw string literal is still open at the end of line."""
    position, quote = 0, None
    while position < len(line):
        character = line[position]
        if raw:
            raw = character != "`"
        elif quote:
            if character == "\\":
                position += 1
            elif character == quote:
                quote = None
        elif character in "\"'":
            quote = character
        elif character == "`":
            raw = True
        elif line.startswith("//", position):
            break
        position += 1
    return raw


def one_line_body(line):
    """The body of `func f() T { ... }` written on one line: from the brace that the last one closes."""
    depth = 0
    for position in range(len(line) - 1, -1, -1):
        depth += {"}": 1, "{": -1}.get(line[position], 0)
        if depth == 0:
            return line[position + 1:-1]
    return ""


def decode(literal):
    return bytes(literal, "utf-8").decode("unicode_escape", "replace")


def test_kind(function):
    """'test' or 'fuzz' when go test runs the function as a top-level test."""
    if function.receiver or not function.file.path.name.endswith("_test.go"):
        return None
    match = TEST.match(function.signature)
    if match is None or match.group(1) == "TestMain":
        return None
    return "test" if match.group(2) == "T" else "fuzz"


@dataclass
class Condition:
    """One enclosing branch condition, read against the bindings visible where it was written."""
    tokens: list
    negated: bool
    bindings: dict


@dataclass
class Site:
    """A skip, a bare return or a call, reached under the conjunction of its conditions."""
    kind: str
    conditions: list
    line: int
    qualifier: str = None
    name: str = None
    bindings: dict = None


OPENERS = {"(": ")", "[": "]", "{": "}"}
KEYWORDS = {"break", "case", "chan", "const", "continue", "default", "defer", "else", "fallthrough", "for", "func",
            "go", "goto", "if", "import", "interface", "map", "package", "range", "return", "select", "struct",
            "switch", "type", "var", "len", "cap", "make", "new", "append", "copy", "delete", "panic", "recover",
            "print", "println", "min", "max", "clear", "close", "complex", "real", "imag"}
ALWAYS_UNKNOWN = [Token("ident", "\u0000unknown", 0)]


def block_end(items, index):
    """Index of the first `{` at bracket depth zero from index; headers never hold composite literals."""
    depth = 0
    while index < len(items):
        text = items[index].text
        if text in {"(", "["}:
            depth += 1
        elif text in {")", "]"}:
            depth -= 1
        elif text == "{" and depth == 0:
            # `range []string{...} {`: slice and map literals may stand unparenthesized in a header.
            back = index - 1
            if items[back].kind != "ident":
                return index
            while back > 1 and items[back - 1].text == "." and items[back - 2].kind == "ident":
                back -= 2
            while back > 0 and items[back - 1].text == "*":
                back -= 1
            if items[back - 1].text != "]":
                return index
            index = matching(items, index, "{", "}")
        index += 1
    raise ValueError("block without a body")


def statement_end(items, index, end):
    """End (exclusive) of the statement starting at index, keeping if/else chains and loop headers whole."""
    if items[index].text in {"if", "switch", "for", "select"}:
        position = matching(items, block_end(items, index), "{", "}") + 1
        while position < end and items[position].text == "else":
            position += 1
            if items[position].text == "if":
                position = block_end(items, position)
            position = matching(items, position, "{", "}") + 1
        return position
    depth, position = 0, index
    while position < end:
        text = items[position].text
        if text in OPENERS:
            depth += 1
        elif text in {")", "]", "}"}:
            depth -= 1
        elif text == ";" and depth == 0:
            return position
        position += 1
    return end


def split_top(items, separator):
    parts, depth, start = [], 0, 0
    for index, token in enumerate(items):
        if token.text in OPENERS:
            depth += 1
        elif token.text in {")", "]", "}"}:
            depth -= 1
        elif token.text == separator and depth == 0:
            parts.append(items[start:index])
            start = index + 1
    parts.append(items[start:])
    return parts


def grouped(items):
    return [Token("op", "(", 0), *items, Token("op", ")", 0)]


def either(alternatives):
    joined = []
    for alternative in alternatives:
        joined += ([Token("op", "||", 0)] if joined else []) + grouped(alternative)
    return grouped(joined) if joined else ALWAYS_UNKNOWN


class Walker:
    """Collect the sites of one function body; function literals (subtests, closures) are not entered."""

    def __init__(self, function):
        self.function = function
        self.sites = []
        self.bindings = {}

    def walk(self):
        self.block(self.function.body, 0, len(self.function.body), [])
        return self.sites

    def block(self, items, start, end, conditions):
        index = start
        while index < end:
            if items[index].text == ";":
                index += 1
                continue
            stop = statement_end(items, index, end)
            self.statement(items, index, stop, conditions)
            index = stop

    def statement(self, items, start, end, conditions):
        head = items[start].text
        if head == "if":
            self.branch(items, start, end, conditions)
        elif head == "switch":
            self.switch(items, start, conditions)
        elif head in {"for", "select"}:
            body = block_end(items, start)
            self.block(items, body + 1, matching(items, body, "{", "}"),
                       conditions + [Condition(ALWAYS_UNKNOWN, False, {})])
        elif head == "{":
            self.block(items, start + 1, matching(items, start, "{", "}"), conditions)
        elif head in {"go", "defer"}:
            return
        elif head == "return":
            if end - start == 1 and conditions:
                self.sites.append(Site("return", conditions, items[start].line))
        else:
            self.simple(items[start:end], conditions)

    def branch(self, items, start, end, conditions):
        prior = []
        index = start
        while True:
            body = block_end(items, index + 1)
            header = split_top(items[index + 1:body], ";")
            if len(header) == 2:
                self.simple(header[0], conditions + prior)
            condition = header[-1]
            close = matching(items, body, "{", "}")
            self.block(items, body + 1, close, conditions + prior + [Condition(condition, False, dict(self.bindings))])
            prior = prior + [Condition(condition, True, dict(self.bindings))]
            index = close + 1
            if index >= end or items[index].text != "else":
                return
            if items[index + 1].text == "if":
                index += 1
                continue
            close = matching(items, index + 1, "{", "}")
            self.block(items, index + 2, close, conditions + prior)
            return

    def switch(self, items, start, conditions):
        body = block_end(items, start + 1)
        header = split_top(items[start + 1:body], ";")
        if len(header) == 2:
            self.simple(header[0], conditions)
        tag = header[-1]
        close = matching(items, body, "{", "}")
        typed = any(t.text == "type" for t in tag)
        clauses, depth, index = [], 0, body + 1
        while index < close:
            text = items[index].text
            if text in OPENERS:
                depth += 1
            elif text in {")", "]", "}"}:
                depth -= 1
            elif depth == 0 and text in {"case", "default"}:
                colon = index + 1
                inner = 0
                while not (items[colon].text == ":" and inner == 0):
                    inner += items[colon].text in OPENERS
                    inner -= items[colon].text in {")", "]", "}"}
                    colon += 1
                clauses.append([text, items[index + 1:colon], colon + 1, None])
                if len(clauses) > 1:
                    clauses[-2][3] = index
                index = colon
            index += 1
        if clauses:
            clauses[-1][3] = close
        cases = []
        for kind, expressions, _, _ in clauses:
            if kind == "case" and not typed:
                values = split_top(expressions, ",")
                cases.append(either([grouped(tag) + [Token("op", "==", 0)] + grouped(v) if tag else v
                                     for v in values]))
        for kind, expressions, first, last in clauses:
            if typed:
                condition = Condition(ALWAYS_UNKNOWN, False, {})
            elif kind == "case":
                condition = Condition(cases.pop(0), False, dict(self.bindings))
                cases.append(condition.tokens)
            else:
                condition = Condition(either(cases), True, dict(self.bindings))
            self.block(items, first, last, conditions + [condition])

    def simple(self, items, conditions):
        self.bind(items)
        index = 0
        while index < len(items):
            token = items[index]
            if token.text == "func" and index + 1 < len(items) and items[index + 1].text == "(":
                close = matching(items, index + 1, "(", ")")
                brace = close + 1
                while brace < len(items) and items[brace].text != "{":
                    brace += 1
                index = matching(items, brace, "{", "}") + 1 if brace < len(items) else brace
                continue
            following = items[index + 1].text if index + 1 < len(items) else ""
            if token.kind == "ident" and following == "." and index + 3 < len(items) \
                    and items[index + 3].text == "(" and items[index + 2].kind == "ident":
                name = items[index + 2].text
                if name in SKIP_METHODS:
                    self.sites.append(Site("skip", conditions, token.line))
                else:
                    self.sites.append(Site("call", conditions, token.line, token.text, name, dict(self.bindings)))
                index += 3
                continue
            if token.kind == "ident" and following == "(" and token.text not in KEYWORDS \
                    and (index == 0 or items[index - 1].text != "."):
                self.sites.append(Site("call", conditions, token.line, None, token.text, dict(self.bindings)))
            index += 1

    def bind(self, items):
        if items and items[0].text == "var":
            items = items[1:]
            equals = next((i for i, t in enumerate(items) if t.text == "="), None)
            if equals is None:
                return
            names = [t.text for t in items[:equals] if t.kind == "ident"][:1]
            value = items[equals + 1:]
        else:
            operator = next((i for i, t in enumerate(items) if t.text in {":=", "="}), None)
            if operator is None or any(t.text in OPENERS for t in items[:operator]):
                return
            names = [t.text for t in split_top(items[:operator], ",") for t in t[:1]]
            value = items[operator + 1:]
        values = split_top(value, ",")
        for position, name in enumerate(names):
            if len(values) == len(names):
                self.bindings[name] = (None, values[position])
            else:
                self.bindings[name] = (position, value)
