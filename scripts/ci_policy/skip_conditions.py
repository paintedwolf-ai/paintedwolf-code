"""When a Go test skips: its skip sites and those of the functions it calls, as expressions over the
facts a lane fixes (GOOS, GOARCH, -short, the environment) and judged in three values."""

import os

from ci_policy.go_source import BARE_RETURN, Walker, test_kind, parse_file

UNKNOWN = ("unknown",)
MAX_DEPTH = 6
STRING_FUNCTIONS = {"TrimSpace": lambda value: value.strip(), "ToLower": str.lower}


class Index:
    """Every Go file of one module, by directory and package, with the skip clauses of each function."""

    def __init__(self, module_root, module_path):
        self.root = module_root
        self.module = module_path
        self.packages = {}
        self.files = []
        self.clauses_memo = {}
        self._may_skip = None
        for directory, names, files in os.walk(module_root):
            relative = os.path.relpath(directory, module_root)
            names[:] = sorted(n for n in names if n != "testdata" and not n.startswith((".", "_"))
                              and not os.path.exists(os.path.join(directory, n, "go.mod")))
            for name in sorted(files):
                if name.endswith(".go"):
                    parsed = parse_file(module_root / relative / name, "." if relative == "." else relative)
                    self.files.append(parsed)
                    self.packages.setdefault((parsed.directory, parsed.package), []).append(parsed)

    def may_skip(self):
        """Names of functions that can reach a skip, by name alone; a superset that spares tokenizing the rest."""
        if self._may_skip is None:
            names = {f.name for file in self.files for f in file.functions if f.skips}
            while True:
                grown = names | {f.name for file in self.files for f in file.functions
                                 if f.name not in names and f.calls & names}
                if grown == names:
                    break
                names = grown
            self._may_skip = names
        return self._may_skip

    def tests(self):
        for file in self.files:
            for function in file.functions:
                kind = test_kind(function)
                if kind:
                    yield file, function

    def package_files(self, file):
        return self.packages.get((file.directory, file.package), [])

    def constant(self, file, name):
        for other in self.package_files(file):
            if name in other.constants:
                return other.constants[name]
        return None

    def import_directory(self, path):
        if path == self.module:
            return "."
        if path.startswith(self.module + "/"):
            return path[len(self.module) + 1:]
        return None

    def imported_files(self, file, alias):
        directory = self.import_directory(file.imports.get(alias, ""))
        if directory is None:
            return []
        return [f for (d, p), files in self.packages.items() if d == directory and not p.endswith("_test")
                for f in files]

    def definitions(self, file, qualifier, name):
        """Functions a call can reach: same-package names, imported module functions, and
        methods named once in the package."""
        if qualifier is None:
            candidates = self.package_files(file)
            return [f for c in candidates for f in c.functions if f.name == name and not f.receiver]
        if qualifier in file.imports:
            return [f for c in self.imported_files(file, qualifier) for f in c.functions
                    if f.name == name and not f.receiver]
        methods = [f for c in self.package_files(file) for f in c.functions if f.name == name and f.receiver]
        return methods if len({id(m.file) for m in methods}) == len(methods) else []

    def clauses(self, function, depth=0, test=False):
        """Lists of conditions, each list a conjunction under which the function skips."""
        key = (id(function), test)
        if key in self.clauses_memo:
            return self.clauses_memo[key]
        self.clauses_memo[key] = []
        if not (function.skips or function.calls & self.may_skip()
                or test and BARE_RETURN.search(function.text)):
            return []
        result = []
        try:
            sites = Walker(function).walk()
        except (ValueError, IndexError) as error:
            raise ValueError(f"{function.file.path}:{function.line} {function.name}: {error}") from error
        for site in sites:
            conditions = [(c, function.file) for c in site.conditions]
            if site.kind == "skip" or site.kind == "return" and test:
                result.append(conditions)
            elif site.kind == "call" and depth < MAX_DEPTH:
                for callee in self.definitions(function.file, site.qualifier, site.name):
                    for clause in self.clauses(callee, depth + 1):
                        result.append([*conditions, (("file", callee.file), None), *clause])
        self.clauses_memo[key] = result
        return result


class Expression:
    """Read a condition's tokens into an expression tree, resolving bindings, constants and pure helpers."""

    def __init__(self, index, file, bindings, depth=0):
        self.index, self.file, self.bindings, self.depth = index, file, bindings, depth
        self.items, self.position = [], 0

    def parse(self, items):
        if self.depth > MAX_DEPTH:
            return UNKNOWN
        self.items, self.position = list(items), 0
        try:
            tree = self.disjunction()
        except (IndexError, ValueError):
            return UNKNOWN
        return tree if self.position == len(self.items) else UNKNOWN

    def peek(self):
        return self.items[self.position].text if self.position < len(self.items) else None

    def take(self):
        token = self.items[self.position]
        self.position += 1
        return token

    def disjunction(self):
        tree = self.conjunction()
        while self.peek() == "||":
            self.take()
            tree = ("or", tree, self.conjunction())
        return tree

    def conjunction(self):
        tree = self.comparison()
        while self.peek() == "&&":
            self.take()
            tree = ("and", tree, self.comparison())
        return tree

    def comparison(self):
        tree = self.arithmetic()
        while self.peek() in {"==", "!=", "<", "<=", ">", ">="}:
            operator = self.take().text
            right = self.arithmetic()
            tree = ("cmp", operator, tree, right) if operator in {"==", "!="} else UNKNOWN
        return tree

    def arithmetic(self):
        tree = self.unary()
        while self.peek() in {"+", "-", "|", "^", "*", "/", "%", "<<", ">>", "&", "&^"}:
            self.take()
            self.unary()
            tree = UNKNOWN
        return tree

    def unary(self):
        if self.peek() == "!":
            self.take()
            return ("not", self.unary())
        if self.peek() in {"-", "+", "^", "*", "&", "<-"}:
            self.take()
            self.unary()
            return UNKNOWN
        return self.primary()

    def arguments(self):
        self.take()
        arguments, start, depth = [], self.position, 0
        while True:
            text = self.peek()
            if text in {"(", "[", "{"}:
                depth += 1
            elif text in {")", "]", "}"}:
                if depth == 0:
                    break
                depth -= 1
            elif text == "," and depth == 0:
                arguments.append(self.items[start:self.position])
                start = self.position + 1
            self.position += 1
        if self.position > start:
            arguments.append(self.items[start:self.position])
        self.take()
        return arguments

    def nested(self, items, file=None, bindings=None):
        return Expression(self.index, file or self.file, self.bindings if bindings is None else bindings,
                          self.depth + 1).parse(items)

    def primary(self):
        token = self.take()
        if token.text == "(":
            tree = self.disjunction()
            if self.take().text != ")":
                raise ValueError("unbalanced parenthesis")
            return self.postfix(tree, None)
        if token.kind == "string":
            return ("str", token.value)
        if token.kind != "ident":
            return self.postfix(UNKNOWN, None)
        if token.text in {"true", "false"}:
            return ("bool", token.text == "true")
        if self.peek() == "." and token.text in self.file.imports and self.position + 1 < len(self.items):
            self.take()
            return self.qualified(self.file.imports[token.text], token.text, self.take().text)
        if self.peek() == "(":
            arguments = self.arguments()
            return self.postfix(self.helper(None, token.text, arguments), None)
        if token.text in self.bindings:
            position, value = self.bindings[token.text]
            bound = self.nested(value)
            if position is not None:
                bound = ("index", position, bound)
            return self.postfix(bound, None)
        constant = self.index.constant(self.file, token.text)
        return self.postfix(("str", constant) if constant is not None else UNKNOWN, None)

    def qualified(self, path, alias, name):
        if path == "runtime" and name in {"GOOS", "GOARCH"}:
            return self.postfix((name.lower(),), None)
        if self.peek() != "(":
            for file in self.index.imported_files(self.file, alias):
                if name in file.constants:
                    return self.postfix(("str", file.constants[name]), None)
            return self.postfix(UNKNOWN, None)
        arguments = self.arguments()
        if path == "testing" and name == "Short":
            return ("short",)
        if path == "os" and name in {"Getenv", "LookupEnv"} and len(arguments) == 1:
            key = self.nested(arguments[0])
            if key[0] != "str":
                return UNKNOWN
            return ("env", key[1]) if name == "Getenv" else ("lookup", key[1])
        if path == "strings" and name in STRING_FUNCTIONS and len(arguments) == 1:
            return ("apply", name, self.nested(arguments[0]))
        return self.postfix(self.helper(alias, name, arguments), None)

    def helper(self, qualifier, name, arguments):
        """A module function whose body is bindings then one return is read as that expression."""
        definitions = self.index.definitions(self.file, qualifier, name)
        if len(definitions) != 1 or arguments:
            return UNKNOWN
        body = definitions[0].body
        walker = Walker(definitions[0])
        statements = [s for s in split_statements(body) if s]
        if not statements or statements[-1][0].text != "return":
            return UNKNOWN
        for statement in statements[:-1]:
            if any(t.text in {"if", "for", "switch", "select", "return"} for t in statement):
                return UNKNOWN
            walker.bind(statement)
        return self.nested(statements[-1][1:], definitions[0].file, walker.bindings)

    def postfix(self, tree, _):
        while self.peek() in {".", "(", "["}:
            if self.peek() == "(":
                self.arguments()
            elif self.peek() == "[":
                self.take()
                depth = 0
                while not (self.peek() == "]" and depth == 0):
                    depth += self.peek() == "["
                    depth -= self.peek() == "]"
                    self.position += 1
                self.take()
            else:
                self.take()
                self.take()
            tree = UNKNOWN
        return tree


def split_statements(body):
    from ci_policy.go_source import statement_end
    result, index = [], 0
    while index < len(body):
        if body[index].text == ";":
            index += 1
            continue
        end = statement_end(body, index, len(body))
        result.append(body[index:end])
        index = end
    return result


def evaluate(tree, lane):
    """True, False, or None when the lane's facts do not decide it."""
    kind = tree[0]
    if kind in {"str", "bool"}:
        return tree[1]
    if kind == "goos":
        return lane.goos
    if kind == "goarch":
        return lane.goarch
    if kind == "short":
        return lane.short
    if kind == "env":
        value = lane.environment.get(tree[1], "")
        return value
    if kind == "lookup":
        return tree[1] in lane.environment
    if kind == "index":
        inner = tree[2]
        if inner[0] == "lookup":
            return evaluate(("env", inner[1]), lane) if tree[1] == 0 else tree[1] == 1 and inner[1] in lane.environment
        return None
    if kind == "apply":
        value = evaluate(tree[2], lane)
        return STRING_FUNCTIONS[tree[1]](value) if isinstance(value, str) else None
    if kind == "not":
        value = evaluate(tree[1], lane)
        return None if value is None else not value
    if kind == "and":
        left, right = evaluate(tree[1], lane), evaluate(tree[2], lane)
        if left is False or right is False:
            return False
        return True if left is True and right is True else None
    if kind == "or":
        left, right = evaluate(tree[1], lane), evaluate(tree[2], lane)
        if left is True or right is True:
            return True
        return False if left is False and right is False else None
    if kind == "cmp":
        left, right = evaluate(tree[2], lane), evaluate(tree[3], lane)
        if not isinstance(left, (str, bool)) or not isinstance(right, (str, bool)) or type(left) is not type(right):
            return None
        return (left == right) == (tree[1] == "==")
    return None
