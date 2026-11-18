"""Where a Go function skips: each skip, bare return and call with the branch conditions that reach it."""

from dataclasses import dataclass

from ci_policy.tier_reach.source import SKIP_METHODS, Token, matching


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


def terminates(body):
    """Whether a block's last statement leaves the function: return, panic, os.Exit, or a fatal or skip call."""
    statements = [part for part in split_top(body, ";") if part]
    if not statements:
        return False
    last = statements[-1]
    if last[0].text in {"return", "panic"}:
        return True
    return len(last) > 3 and last[1].text == "." and last[3].text == "(" and (
        last[2].text in {"Fatal", "Fatalf", "FailNow", *SKIP_METHODS} or (last[0].text, last[2].text) == ("os", "Exit"))


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
        """Walk statements in order; one after `if c { return }` (or a fatal call) runs only under !c."""
        index = start
        while index < end:
            if items[index].text == ";":
                index += 1
                continue
            stop = statement_end(items, index, end)
            conditions = conditions + self.statement(items, index, stop, conditions)
            index = stop

    def statement(self, items, start, end, conditions):
        """Walk one statement; return the conditions it adds for the statements after it."""
        head = items[start].text
        if head == "if":
            return self.branch(items, start, end, conditions)
        elif head == "switch":
            self.switch(items, start, conditions)
        elif head in {"for", "select"}:
            body = block_end(items, start)
            self.block(items, body + 1, matching(items, body, "{", "}"),
                       conditions + [Condition(ALWAYS_UNKNOWN, False, {})])
        elif head == "{":
            self.block(items, start + 1, matching(items, start, "{", "}"), conditions)
        elif head in {"go", "defer"}:
            pass
        elif head == "return":
            if end - start == 1 and conditions:
                self.sites.append(Site("return", conditions, items[start].line))
        else:
            self.simple(items[start:end], conditions)
        return []

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
            negation = Condition(condition, True, dict(self.bindings))
            index = close + 1
            if index >= end or items[index].text != "else":
                return [*prior, negation] if not prior and terminates(items[body + 1:close]) else []
            prior = prior + [negation]
            if items[index + 1].text == "if":
                index += 1
                continue
            close = matching(items, index + 1, "{", "}")
            self.block(items, index + 2, close, conditions + prior)
            return []

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
