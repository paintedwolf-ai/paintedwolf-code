"""When a Go test skips: its skip sites and those of the functions it calls, as expressions over the
facts a lane fixes (GOOS, GOARCH, -short, the environment) and judged in three values."""

import os
import re

from ci_policy.tier_reach.source import BARE_RETURN, parse_file, test_kind
from ci_policy.tier_reach.expression import MAX_DEPTH
from ci_policy.tier_reach.walk import Walker

REEXEC = re.compile(r"""-test\.run(?:=|\s|"\s*,\s*")['"]?\^?(\w+)""")


class Index:
    """Every Go file of one module, by directory and package, with the skip clauses of each function."""

    def __init__(self, module_root, module_path):
        self.root = module_root
        self.module = module_path
        self.packages = {}
        self.files = []
        self.clauses_memo = {}
        self._may_skip = None
        self.reexecuted_memo = {}
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

    def reexecuted(self, directory):
        """Tests a package's own tests start as helper processes (`-test.run=^TestX$`): fixtures of the test
        that starts them, reached with it whatever their own gates say."""
        if directory not in self.reexecuted_memo:
            files = [f for (d, _), group in self.packages.items() if d == directory for f in group]
            texts = [f.text for file in files for f in file.functions]
            texts += [value for file in files for value in file.constants.values()]
            self.reexecuted_memo[directory] = {name for text in texts for name in REEXEC.findall(text)}
        return self.reexecuted_memo[directory]

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
