#!/usr/bin/env python3
"""Label test failures from their error-producing assignments."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

TESTUTIL_IMPORT = "github.com/lycaon/lycaon/internal/testutil"
CONTRACT_IMPORT = "github.com/lycaon/lycaon/test/contract/internal/check"
CONTRACT_DIR = "lycaon/test/contract"

# Failure helpers cannot call themselves.
SKIP_FILES = {
    "lycaon/internal/testutil/failerr.go",
    "lycaon/internal/testutil/failerr_test.go",
    "lycaon/test/contract/internal/check/fail_helpers.go",
    "lycaon/test/contract/internal/check/fail_helpers_test.go",
}

STEP_BY_FUNC = {
    "discoverAPIStringEnums": "discover API string enums in pkg/api",
    "loadOpenAPIEnums": "load OpenAPI enum schemas from docs/openapi.yaml",
    "loadOpenAPIRoutes": "load OpenAPI routes from docs/openapi.yaml",
    "loadServerRoutes": "load server routes from internal/api/server.go",
    "loadOpenAPIOperationIDs": "load OpenAPI operationIds",
    "parseTSClientMethods": "parse LycaonClient methods from lycaon-den/src/api/client.ts",
    "parseTSEnumUnions": "parse TS enum unions in lycaon-den/src/api/types.ts",
    "loadOpenAPIObjectSchemas": "load OpenAPI object schemas",
    "syncDTOFields": "sync wire DTO fields across Go, OpenAPI, and TS",
    "validateOpenAPISchemaRefs": "validate OpenAPI schema $ref targets",
    "scanGuidanceEmission": "scan guidance emission sites in internal/",
    "scanAPIErrorCodes": "scan API error codes in internal/api",
    "walkFiles": "walk repository files",
    "guidance.LoadHintConfig": "load hint registry",
    "rules.LoadRulesConfig": "load rules config YAML",
    "rules.RegisterRuleConditions": "register rule conditions",
    "conditions.NewDefaultRegistry": "build conditions registry",
    "sandbox.LoadToolProfiles": "load tool profiles",
    "sandbox.LoadPathScopes": "load path-scopes.yaml",
    "sandbox.LoadConfig": "load sandbox config",
    "toolpolicy.LoadProfileRuntimeRules": "load agent-tool-profiles runtime rules",
    "workflow.LoadTemplatesFromDir": "load workflow templates",
    "guidance.FormatCoordinatorNudge": "render coordinator nudge template",
    "codesReferencedInRulesYAML": "scan hint codes referenced in rules YAML",
    "review.PutProject": "write project review.yaml",
    "parser.ParseFile": "parse Go source file",
    "yaml.Unmarshal": "unmarshal YAML document",
    "json.Unmarshal": "unmarshal JSON document",
    "os.ReadDir": "read directory entries",
    "os.ReadFile": "read file",
    "os.WriteFile": "write file",
    "os.MkdirAll": "create directory",
    "os.Getwd": "get working directory",
    "os.Stat": "stat path",
    "composer.Compose": "compose workflow manifest",
    "store.Create": "create session in store",
    "h.Store.Create": "create session in store",
    "h.CheckpointMgr.ListPending": "list pending human checkpoints",
    "engine.Render": "render prompt template",
    "manifest.Load": "load workflow manifest",
    "app.Build": "build app wiring harness",
}

# Allow any number of LHS idents before err (e.g. `allow, reason, err := ...`).
CALL_RE = re.compile(r"^\s*(?:[\w.*]+\s*,\s*)*err\s*:?=\s*(.+)$")
INLINE_IF_RE = re.compile(r"^\s*if\s+err\s*:?=\s*(.+);\s*err\s*!=\s*nil\s*\{\s*$")
BLOCK_IF_RE = re.compile(r"^(\s*)if err != nil \{\s*$")
# Init-if header, allowing extra LHS before err (e.g. `if _, err := ...`).
INIT_IF_RE = re.compile(r"^(\s*)if (?:[\w.*]+\s*,\s*)*err :?= (.+); err != nil \{\s*$")
FATAL_RE = re.compile(r"^(\s*)t\.Fatal\(err\)\s*$")
CLOSE_RE = re.compile(r"^(\s*)\}\s*$")

# Uninferable steps leave the original call unchanged.
UNKNOWN_STEP = "operation failed"


def step_for_call(call: str) -> str:
    call = call.strip()
    for func, step in STEP_BY_FUNC.items():
        if call.startswith(func + "(") or call == func:
            return step
    m = re.match(r"([\w.]+)\(", call)
    if m:
        return f"{m.group(1)} failed"
    return UNKNOWN_STEP


def infer_step(lines: list[str], fatal_idx: int) -> str:
    # Track delimiters while searching upward for the err assignment.
    depth = 0
    for j in range(fatal_idx - 1, max(fatal_idx - 16, -1), -1):
        line = lines[j]
        stripped = line.strip()
        if not stripped or stripped.startswith("//"):
            continue
        depth += line.count(")") + line.count("}") + line.count("]")
        depth -= line.count("(") + line.count("{") + line.count("[")
        if depth > 0:
            # Inside a multi-line construct; keep climbing to its first line.
            continue
        depth = 0
        m = CALL_RE.match(line)
        if m:
            return step_for_call(m.group(1))
        m = INLINE_IF_RE.match(line)
        if m:
            return step_for_call(m.group(1))
        if "err" in stripped and ("(" in stripped or stripped.startswith("return err")):
            return step_for_call(stripped.split("err", 1)[-1].lstrip("=:").strip())
        break
    return UNKNOWN_STEP


def transform(content: str, call: str) -> tuple[str, int]:
    """Replace labeled failures with calls to the selected helper."""
    lines = content.splitlines(keepends=True)
    out: list[str] = []
    i = 0
    changes = 0
    while i < len(lines):
        # Multi-line: if err != nil { t.Fatal(err) }
        if (
            i + 2 < len(lines)
            and BLOCK_IF_RE.match(lines[i])
            and FATAL_RE.match(lines[i + 1])
            and CLOSE_RE.match(lines[i + 2])
        ):
            step = infer_step(lines, i)
            if step == UNKNOWN_STEP:
                # An unknown assignment cannot supply a useful label.
                out.append(lines[i])
                i += 1
                continue
            indent = BLOCK_IF_RE.match(lines[i]).group(1)
            out.append(f'{indent}{call}(t, "{step}", err)\n')
            i += 3
            changes += 1
            continue

        # Multi-line: if err := foo(); err != nil { t.Fatal(err) }
        # Preserving the initializer keeps err scoped to this branch.
        m_init = INIT_IF_RE.match(lines[i])
        if (
            i + 2 < len(lines)
            and m_init
            and FATAL_RE.match(lines[i + 1])
            and CLOSE_RE.match(lines[i + 2])
        ):
            indent, callsite = m_init.group(1), m_init.group(2)
            step = step_for_call(callsite)
            if step == UNKNOWN_STEP:
                out.append(lines[i])
                i += 1
                continue
            out.append(lines[i])  # keep: if err := foo(); err != nil {
            out.append(f'{indent}\t{call}(t, "{step}", err)\n')
            out.append(lines[i + 2])  # keep: }
            i += 3
            changes += 1
            continue

        # else-if: } else if !os.IsNotExist(err) { t.Fatal(err) }
        m = re.match(
            r"^(\s*)\} else if (.+) \{\s*t\.Fatal\(err\)\s*\}\s*$",
            lines[i],
        )
        if m:
            indent, cond = m.group(1), m.group(2)
            out.append(
                f'{indent}}} else if {cond} {{\n'
                f'{indent}\t{call}(t, "unexpected stat error", err)\n'
                f'{indent}}}\n'
            )
            i += 1
            changes += 1
            continue

        out.append(lines[i])
        i += 1
    return "".join(out), changes


RELABEL_RE = re.compile(
    r'^(\s*)((?:(?:testutil|contractcheck)\.)?FailErr)\(t, "operation failed", err\)\s*$'
)


def relabel(content: str, call: str) -> tuple[str, int]:
    """Replace generic failure labels when the surrounding assignment identifies a step."""
    lines = content.splitlines(keepends=True)
    changes = 0
    for i, line in enumerate(lines):
        m = RELABEL_RE.match(line)
        if not m:
            continue
        indent, callee = m.group(1), m.group(2)
        # Body of an `if <lhs> := f(); err != nil {` block — read the header.
        step = UNKNOWN_STEP
        if i > 0:
            hdr = INIT_IF_RE.match(lines[i - 1])
            if hdr:
                step = step_for_call(hdr.group(2))
        if step == UNKNOWN_STEP:
            step = infer_step(lines, i)
        if step == UNKNOWN_STEP:
            continue
        lines[i] = f'{indent}{callee}(t, "{step}", err)\n'
        changes += 1
    return "".join(lines), changes


def ensure_import(content: str, import_path: str, alias: str = "") -> str:
    """Add the failure helper import to the project import group."""
    if import_path in content:
        return content
    new_line = f'\t{alias + " " if alias else ""}"{import_path}"'

    # Case 1: existing parenthesised import block.
    m = re.search(r"^import \(\n(.*?)\n\)\n", content, re.MULTILINE | re.DOTALL)
    if m:
        body = m.group(1)
        lines = body.split("\n")
        for i, ln in enumerate(lines):
            if "github.com/lycaon/lycaon" in ln:
                start = i
                end = i
                while end + 1 < len(lines) and lines[end + 1].strip() != "":
                    end += 1
                group = lines[start : end + 1] + [new_line]
                group.sort(key=lambda s: s.strip())
                lines[start : end + 1] = group
                break
        else:
            if lines and lines[-1].strip() != "":
                lines.append("")
            lines.append(new_line)
        new_body = "\n".join(lines)
        return content[: m.start()] + f"import (\n{new_body}\n)\n" + content[m.end() :]

    # Case 2: single-line `import "x"` — convert to a block.
    m = re.search(r'^import "([^"]+)"\n', content, re.MULTILINE)
    if m:
        other = m.group(1)
        block = (
            f'import (\n'
            f'\t"{other}"\n\n'
            f'{new_line}\n'
            f')\n'
        )
        return content[: m.start()] + block + content[m.end() :]

    # Case 3: no import block at all — insert after the package clause.
    m = re.search(r"^package [^\n]+\n", content, re.MULTILINE)
    if m:
        block = f'\nimport (\n{new_line}\n)\n'
        return content[: m.end()] + block + content[m.end() :]

    return content


def is_test_file(path: Path) -> bool:
    return path.name.endswith("_test.go")


def failure_helper(path: Path, repo_root: Path) -> tuple[str, str, str]:
    rel = path.relative_to(repo_root).as_posix()
    if rel.startswith(CONTRACT_DIR + "/internal/check/"):
        return "FailErr", "", ""
    if rel.startswith(CONTRACT_DIR + "/"):
        return "contractcheck.FailErr", CONTRACT_IMPORT, "contractcheck"
    return "testutil.FailErr", TESTUTIL_IMPORT, ""


def rewrite_file(path: Path, repo_root: Path) -> int:
    rel = path.relative_to(repo_root).as_posix()
    if rel in SKIP_FILES:
        return 0

    call, import_path, alias = failure_helper(path, repo_root)

    original = path.read_text()
    updated, n = transform(original, call)
    updated, r = relabel(updated, call)
    n += r
    if n == 0:
        return 0
    if import_path:
        updated = ensure_import(updated, import_path, alias)
    path.write_text(updated)
    return n


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="exit non-zero if any rewrite would happen")
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args(argv)

    repo_root = args.root.resolve()
    lycaon_dir = repo_root / "lycaon"
    if not lycaon_dir.is_dir():
        print(f"error: {lycaon_dir} not found", file=sys.stderr)
        return 2

    total = 0
    touched: list[str] = []
    # All _test.go plus contract package's helper .go files.
    candidates = sorted(lycaon_dir.rglob("*.go"))
    for path in candidates:
        rel = path.relative_to(repo_root).as_posix()
        if rel in SKIP_FILES:
            continue
        if not is_test_file(path):
            # Contract helpers also contain fatal calls.
            if not rel.startswith(CONTRACT_DIR + "/"):
                continue

        if args.check:
            call, _, _ = failure_helper(path, repo_root)
            text, n = transform(path.read_text(), call)
            _, r = relabel(text, call)
            n += r
            if n:
                total += n
                touched.append(f"{rel}: {n}")
            continue

        n = rewrite_file(path, repo_root)
        if n:
            total += n
            touched.append(f"{rel}: {n}")

    if touched:
        for line in touched:
            print(line)
    print(f"total failErr rewrites: {total}")
    if args.check and total:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
