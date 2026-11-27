"""Deterministic query, lookup, symbol corpus, and replacement generators (S01, S04-S06, R01-R03)."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
import random

from .generators import DEFAULT_TEXT_TOKENS
from .seeds import ReplaySelector, seed_rng

S01_QUERY_FORMS = (
    "literal",
    "case_sensitive_literal",
    "whole_word",
    "regex",
    "glob_filtered",
    "path_filtered",
)

S05_LANGUAGES = ("go", "typescript", "python", "rust", "java", "c")

S06_TEXT_KINDS = (
    "plain_text",
    "url",
    "stray_quotes",
    "stray_brackets",
    "bare_punctuation",
    "lowercase_operator",
    "unknown_field",
    "invalid_closed_field",
)


@dataclass(frozen=True)
class CodeQuerySpec:
    """One S01 code-search query specification."""

    form: str
    pattern: str
    case_sensitive: bool = False
    whole_word: bool = False
    regex: bool = False
    include_globs: tuple[str, ...] = ()
    exclude_globs: tuple[str, ...] = ()
    path_filters: tuple[str, ...] = ()

    def to_dict(self) -> dict[str, object]:
        return {
            "form": self.form,
            "pattern": self.pattern,
            "case_sensitive": self.case_sensitive,
            "whole_word": self.whole_word,
            "regex": self.regex,
            "include_globs": list(self.include_globs),
            "exclude_globs": list(self.exclude_globs),
            "path_filters": list(self.path_filters),
        }

    @classmethod
    def from_dict(cls, data: dict[str, object]) -> "CodeQuerySpec":
        return cls(
            form=str(data["form"]),
            pattern=str(data["pattern"]),
            case_sensitive=bool(data.get("case_sensitive", False)),
            whole_word=bool(data.get("whole_word", False)),
            regex=bool(data.get("regex", False)),
            include_globs=tuple(str(x) for x in data.get("include_globs", ())),  # type: ignore[union-attr]
            exclude_globs=tuple(str(x) for x in data.get("exclude_globs", ())),  # type: ignore[union-attr]
            path_filters=tuple(str(x) for x in data.get("path_filters", ())),  # type: ignore[union-attr]
        )


@dataclass(frozen=True)
class LookupQuerySpec:
    """One S04 file-lookup query specification."""

    query_text: str
    expected_kind: str  # basename_prefix | basename_substring | path_window | full_path


@dataclass(frozen=True)
class DeclarationSpec:
    """Ground-truth symbol declaration inside an S05 generated source file."""

    language: str
    path: str
    name: str
    kind: str  # function | type | method | class | struct
    line_number: int


@dataclass(frozen=True)
class QueryTextCase:
    """One S06 query-language input case."""

    kind: str
    text: str
    expect_parse_error: bool


@dataclass(frozen=True)
class ReplaceSpec:
    """One R01-R03 replacement expression specification."""

    pattern: str
    replacement: str
    regex: bool = False
    case_sensitive: bool = False
    whole_word: bool = False

    def to_dict(self) -> dict[str, object]:
        return {
            "pattern": self.pattern,
            "replacement": self.replacement,
            "regex": self.regex,
            "case_sensitive": self.case_sensitive,
            "whole_word": self.whole_word,
        }


def generate_shared_regex(rng: random.Random, tokens: tuple[str, ...] = DEFAULT_TEXT_TOKENS) -> str:
    """Generate a D3 shared-syntax regex supported by both ripgrep and Go RE2.

    Uses only literals, character classes, alternation, anchors, bounded
    repetition, and numbered capture groups.
    """
    a = rng.choice(tokens)
    b = rng.choice(tokens)
    template = rng.choice(
        (
            f"({a}|{b})_[0-9]{{1,2}}",
            f"^line_[0-9]{{1,2}}:.*({a})",
            f"({a})[[:space:]]+({b})",
            f"([a-z]+)_([0-9]{{1,3}})",
            f"({a}|{b})",
        )
    )
    return template


def generate_s01_query(
    base_seed: str,
    family_id: str,
    instance_index: int,
    form: str,
    corpus_tokens: tuple[str, ...] = DEFAULT_TEXT_TOKENS,
    paths: list[str] | None = None,
) -> CodeQuerySpec:
    """Generate one deterministic S01 query of the requested form."""
    if form not in S01_QUERY_FORMS:
        raise ValueError(f"unsupported S01 query form: {form}")
    rng = seed_rng(base_seed, f"{family_id}:query:{form}", instance_index)
    token = rng.choice(corpus_tokens)

    if form == "literal":
        return CodeQuerySpec(form=form, pattern=token, case_sensitive=False)
    if form == "case_sensitive_literal":
        return CodeQuerySpec(form=form, pattern=token, case_sensitive=True)
    if form == "whole_word":
        return CodeQuerySpec(form=form, pattern=token, case_sensitive=False, whole_word=True)
    if form == "regex":
        return CodeQuerySpec(form=form, pattern=generate_shared_regex(rng, corpus_tokens), regex=True)
    if form == "glob_filtered":
        include_ext = rng.choice(("*.txt", "*.go", "*.py", "*.ts", "*.md"))
        exclude_ext = rng.choice(("*.bin", "*.ignored"))
        return CodeQuerySpec(
            form=form,
            pattern=token,
            include_globs=(include_ext,),
            exclude_globs=(exclude_ext,),
        )
    # path_filtered
    prefix = "src/"
    if paths:
        with_dirs = [p for p in paths if "/" in p]
        if with_dirs:
            chosen = rng.choice(with_dirs)
            prefix = f"{chosen.split('/', 1)[0]}/"
    return CodeQuerySpec(form=form, pattern=token, path_filters=(prefix,))


def generate_s04_lookup(
    base_seed: str,
    family_id: str,
    instance_index: int,
    paths: list[str],
) -> LookupQuerySpec:
    """Generate a deterministic S04 file-lookup query from candidate paths."""
    rng = seed_rng(base_seed, f"{family_id}:s04", instance_index)
    if not paths:
        return LookupQuerySpec(query_text="file", expected_kind="basename_prefix")
    chosen = rng.choice(sorted(paths))
    name = Path(chosen).name
    stem = Path(chosen).stem or name
    mode = rng.choice(("basename_prefix", "basename_substring", "path_window", "full_path"))

    if mode == "basename_prefix":
        return LookupQuerySpec(query_text=stem[: max(2, len(stem) // 2)], expected_kind=mode)
    if mode == "basename_substring" and len(stem) >= 4:
        return LookupQuerySpec(query_text=stem[1 : max(3, len(stem) - 1)], expected_kind=mode)
    if mode == "path_window" and "/" in chosen:
        parts = chosen.split("/")
        return LookupQuerySpec(query_text=f"{parts[0][:3]}{parts[-1][:3]}", expected_kind=mode)
    return LookupQuerySpec(query_text=chosen, expected_kind="full_path")


def generate_s05_corpus(
    base_seed: str,
    family_id: str,
    instance_index: int,
    languages: tuple[str, ...] = S05_LANGUAGES,
    declarations_per_language: int = 3,
) -> tuple[dict[str, str], list[DeclarationSpec]]:
    """Generate multi-language source files with known declarations and non-declaration mentions."""
    rng = seed_rng(base_seed, f"{family_id}:s05", instance_index)
    files: dict[str, str] = {}
    declarations: list[DeclarationSpec] = []

    for lang in languages:
        if lang not in S05_LANGUAGES:
            raise ValueError(f"unsupported S05 language: {lang}")
        lines: list[str] = []
        path = _s05_path(lang)
        lines.append(_s05_header(lang))
        for idx in range(max(1, declarations_per_language)):
            suffix = rng.randint(10, 99)
            sym = f"Handle{lang.capitalize()}{idx}_{suffix}"
            # Add a comment mention (must NOT be returned as a declaration).
            lines.append(_s05_comment(lang, f"mention of {sym} in comment"))
            decl_line = len(lines) + 1
            decl_text, kind = _s05_declaration(lang, sym)
            lines.extend(decl_text)
            declarations.append(
                DeclarationSpec(
                    language=lang,
                    path=path,
                    name=sym,
                    kind=kind,
                    line_number=decl_line,
                )
            )
        files[path] = "\n".join(lines) + "\n"

    return files, declarations


def _s05_path(lang: str) -> str:
    ext_map = {
        "go": "pkg/symbols.go",
        "typescript": "src/symbols.ts",
        "python": "src/symbols.py",
        "rust": "src/symbols.rs",
        "java": "src/Symbols.java",
        "c": "src/symbols.c",
    }
    return ext_map[lang]


def _s05_header(lang: str) -> str:
    if lang == "go":
        return "package symbols"
    if lang == "java":
        return "// Symbols fixture"
    if lang == "c":
        return "#include <stdint.h>"
    if lang == "python":
        return "# Symbols fixture"
    return "// Symbols fixture"


def _s05_comment(lang: str, text: str) -> str:
    if lang == "python":
        return f"# {text}"
    return f"// {text}"


def _s05_declaration(lang: str, sym: str) -> tuple[list[str], str]:
    if lang == "go":
        return ([f"func {sym}(v int) int {{", "    return v + 1", "}"], "function")
    if lang == "typescript":
        return ([f"export function {sym}(v: number): number {{", "  return v + 1;", "}"], "function")
    if lang == "python":
        return ([f"def {sym}(v: int) -> int:", "    return v + 1"], "function")
    if lang == "rust":
        return ([f"pub fn {sym}(v: i32) -> i32 {{", "    v + 1", "}"], "function")
    if lang == "java":
        return ([f"public class {sym} {{", "  public int run(int v) { return v + 1; }", "}"], "class")
    return ([f"int {sym}(int v) {{", "    return v + 1;", "}"], "function")


def generate_s06_query_text(
    base_seed: str,
    family_id: str,
    instance_index: int,
    kind: str,
) -> QueryTextCase:
    """Generate one deterministic S06 query-language test case."""
    if kind not in S06_TEXT_KINDS:
        raise ValueError(f"unsupported S06 kind: {kind}")
    rng = seed_rng(base_seed, f"{family_id}:s06:{kind}", instance_index)
    tok = rng.choice(DEFAULT_TEXT_TOKENS)

    cases = {
        "plain_text": QueryTextCase(kind, f"find {tok} in workspace", False),
        "url": QueryTextCase(kind, f"https://example.com/api/v1/{tok}?q=1", False),
        "stray_quotes": QueryTextCase(kind, f'"{tok} unclosed phrase', False),
        "stray_brackets": QueryTextCase(kind, f"({tok}[0] + {tok}", False),
        "bare_punctuation": QueryTextCase(kind, rng.choice(("//", "->", "::", "==")), False),
        "lowercase_operator": QueryTextCase(kind, f"{tok} and or not alpha", False),
        "unknown_field": QueryTextCase(kind, f"Error:{tok} foo:bar", False),
        "invalid_closed_field": QueryTextCase(kind, "kind:bogus_value", True),
    }
    return cases[kind]


def generate_replace_spec(
    base_seed: str,
    family_id: str,
    instance_index: int,
    corpus_tokens: tuple[str, ...] = DEFAULT_TEXT_TOKENS,
    with_groups: bool = True,
) -> ReplaceSpec:
    """Generate a deterministic R01-R03 replacement spec (optionally with numbered groups)."""
    rng = seed_rng(base_seed, f"{family_id}:replace", instance_index)
    tok = rng.choice(corpus_tokens)
    if with_groups:
        return ReplaceSpec(
            pattern=f"({tok})_([0-9]{{1,2}})",
            replacement="rewritten_${2}_$1",
            regex=True,
            case_sensitive=False,
            whole_word=False,
        )
    return ReplaceSpec(
        pattern=tok,
        replacement=f"{tok}_replaced",
        regex=False,
        case_sensitive=rng.choice((False, True)),
        whole_word=rng.choice((False, True)),
    )


def selector_for(base_seed: str, family_id: str, instance_index: int) -> ReplaySelector:
    return ReplaySelector(base_seed=base_seed, family_id=family_id, instance_index=instance_index)
