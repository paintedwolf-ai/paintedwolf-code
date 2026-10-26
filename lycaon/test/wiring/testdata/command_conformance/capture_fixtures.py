#!/usr/bin/env python3
"""Capture GNU (Linux container) and BSD (macOS) reference outputs for the v1 command conformance corpus.

The resulting fixtures.json is checked into testdata so the Go conformance suite
never depends on host tools or containers at test time.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

DOCKER_IMAGE = "floryn90/hugo:0.162.1-ext"
PNG_SAMPLE = bytes([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02, 0x03])
BINARY_TODO = b"TODO\x00binary\n"

SEED_TEXT_FILES: dict[str, str] = {
    "notes.txt": "alpha line 1\nbeta line 2\n\nalpha line 4\ngamma\twith\ttabs\n",
    "numbers.txt": "10\n2\n30\n2\n",
    "table.tsv": "id\tname\tcount\n1\talpha\t10\n2\tbeta\t20\n3\tgamma\t30\n",
    "config.json": '{"enabled":false,"version":1,"items":["a","b"]}\n',
    "data.yaml": "enabled: false\nversion: 1\n",
    "code.go": "package sample\n\n// Alpha returns 1.\nfunc Alpha() int {\n\treturn 1\n}\n",
    "patterns.txt": "alpha\ngamma\n",
    "sub/a.txt": "first\nsecond\n",
    "sub/b.txt": "first\nthird\n",
    "sub/nested/deep.go": "package deep\n\n// TODO: fix OldName\nfunc OldName() {}\n",
    "sub/script.sh": "EXPORTED_FROM_SOURCE=sourced_ok\nprintf 'from-source:%s\\n' \"$EXPORTED_FROM_SOURCE\"\n",
    "sub/bad.sh": "if true; then\n",
    ".hidden.txt": "secret-dotfile\n",
    "empty.txt": "",
}


def corpus_cases() -> list[dict[str, Any]]:
    return [
        {'id': 'run3-prefix-ifs-read', 'category': 'live-run-3',
         'description': 'A temporary IFS assignment applies to read without changing later commands.',
         'commands': ['read', 'printf'], 'forms': ['read:-r'],
         'program': "printf '  first  second  \\n' | while IFS= read -r line; do printf '<%s>\\n' \"$line\"; done",
         'tracked_files': []},
        {'id': 'run3-eval-admitted-program', 'category': 'live-run-3',
         'description': 'Eval runs a parsed program with the ordinary shell status.',
         'commands': ['eval', 'printf'], 'forms': [],
         'program': "eval \"printf 'inner:%s\\\\n' value\"",
         'tracked_files': []},
        {'id': 'run3-sed-bsd-next-line-append', 'category': 'live-run-3',
         'description': 'Sed appends text with the BSD next-line spelling.',
         'commands': ['sed'], 'forms': ['sed:-e'],
         'program': "sed -e '/second/a\\\nend' sub/a.txt",
         'tracked_files': []},
        {'id': 'run3-sed-gnu-one-line-append', 'category': 'live-run-3',
         'description': 'Sed appends text with the GNU one-line spelling.',
         'commands': ['sed'], 'forms': ['sed:-e'],
         'program': "sed -e '/second/a end' sub/a.txt",
         'tracked_files': []},
        {'id': 'run3-sed-deleted-last-line', 'category': 'live-run-3',
         'description': 'A deleted last line does not trigger the last-line append address.',
         'commands': ['sed'], 'forms': ['sed:-e'],
         'program': "sed -e '/second/d' -e '$a\\\nend' sub/a.txt",
         'tracked_files': []},
        {'id': 'run3-binary-grep-walk', 'category': 'live-run-3',
         'description': 'Recursive grep reports matching binary files and continues.',
         'commands': ['grep', 'sort'], 'forms': ['grep:-r', 'grep:-n'],
         'program': "grep -rn TODO sub | sort",
         'seed_binary_files': {'sub/nested/todo.bin': base64.b64encode(BINARY_TODO).decode('ascii')},
         'tracked_files': []},
        {'id': 'run3-binary-rg-explicit', 'category': 'live-run-3',
         'description': 'Ripgrep uses its own binary behavior for an explicit file.',
         'commands': ['rg'], 'forms': ['rg:-n'],
         'program': "rg -n TODO sub/nested/todo.bin",
         'seed_binary_files': {'sub/nested/todo.bin': base64.b64encode(BINARY_TODO).decode('ascii')},
         'tracked_files': []},
        {'id': 'run3-pipestatus-sigpipe', 'category': 'live-run-3',
         'description': 'An early reader gives the producer a SIGPIPE status.',
         'commands': ['yes', 'head', 'printf'], 'forms': ['head:-n'],
         'program': "yes | head -n 1; printf 'pipe:%s\\n' \"${PIPESTATUS[*]}\"",
         'tracked_files': []},
        {'id': 'run3-gnu-errors', 'category': 'live-run-3',
         'description': 'Common utility errors retain native exit statuses and stderr.',
         'commands': ['grep', 'cat', 'printf'], 'forms': ['grep:-E'],
         'program': "grep -E '(' notes.txt 2>&1; printf 'grep:%s\\n' \"$?\"; cat missing.txt 2>&1; printf 'cat:%s\\n' \"$?\"",
         'tracked_files': []},
        {'id': 'run3-command-not-found', 'category': 'live-run-3',
         'description': 'An unavailable external program returns 127 with shell stderr.',
         'commands': ['printf'], 'forms': [],
         'program': "pw-missing-program-039; printf 'missing:%s\\n' \"$?\"",
         'tracked_files': []},
        {'id': 'run3-native-discovery', 'category': 'live-run-3',
         'description': 'Discovery describes a native command as native.',
         'commands': ['type', 'command'], 'forms': [],
         'program': "type jq; command -v jq",
         'tracked_files': []},
        {'id': 'run3-sed-hold-reversal', 'category': 'live-run-3',
         'description': 'Sed hold space supports the reversal program used live.',
         'commands': ['sed'], 'forms': [],
         'program': "sed '1!G;h;$!d' sub/a.txt", 'tracked_files': []},
        {'id': 'run3-sort-key-separated', 'category': 'live-run-3',
         'description': 'Sort accepts a separated key as well as the attached spelling.',
         'commands': ['sort'], 'forms': ['sort:-k', 'sort:-n', 'sort:-r'],
         'program': "sort -k 3,3 -n -r table.tsv", 'tracked_files': []},
        {'id': 'run3-paths-and-scratch', 'category': 'live-run-3',
         'description': 'PWD and scratch variables are usable absolute paths.',
         'commands': ['test', 'printf', 'pwd'], 'forms': [],
         'program': "test \"$PWD\" = \"$(pwd)\" && test -d \"$TMPDIR\" && test -d \"$SCRATCH_DIR\" && printf 'paths-ok\\n'",
         'tracked_files': []},
        {'id': 'run3-pipeline-hash', 'category': 'live-run-3',
         'description': 'Pipeline hashing matches a direct file hash.',
         'commands': ['cat', 'sha256sum', 'cut'], 'forms': [],
         'program': "cat notes.txt | sha256sum | cut -d ' ' -f 1; sha256sum notes.txt | cut -d ' ' -f 1",
         'tracked_files': []},
        {'id': 'run3-git-initial-commit', 'category': 'live-run-3',
         'description': 'A new repository can make its first commit.',
         'commands': ['mkdir', 'cd', 'git init', 'git config', 'printf', 'git add', 'git commit', 'git log'], 'forms': ['git commit:-m'],
         'program': "mkdir fresh && cd fresh && git init -q && git config user.name Test && git config user.email test@example.invalid && printf 'new\\n' > README.txt && git add README.txt && git commit -qm first && git log -1 --format=%s",
         'tracked_files': ['fresh/README.txt']},
        {'id': 'run3-scratch-atomic-move', 'category': 'live-run-3',
         'description': 'A temporary file can be moved into the project.',
         'commands': ['mktemp', 'printf', 'mv', 'cat'], 'forms': [],
         'program': "p=$(mktemp) && printf 'saved\\n' > \"$p\" && mv \"$p\" saved.txt && cat saved.txt",
         'tracked_files': ['saved.txt']},
        # Natural programs from the live-run findings.
        {'id': 'natural-find-xargs',
         'category': 'natural-programs',
         'description': 'Find files with zero-delimited names and feed them to xargs.',
         'commands': ['find', 'xargs', 'cat', 'sort'],
         'forms': ['find:-type', 'find:-name', 'find:-print0', 'xargs:-0'],
         'program': "find sub -type f -name '*.txt' -print0 | xargs -0 cat | sort",
         'tracked_files': []},
        {'id': 'natural-while-read',
         'category': 'natural-programs',
         'description': 'Read each complete line from redirected input.',
         'commands': ['read', 'printf'],
         'forms': ['read:-r', 'read:-d'],
         'program': 'while read -r line; do printf \'<%s>\\n\' "$line"; done < sub/a.txt',
         'tracked_files': []},
        {'id': 'natural-sed-hold-space',
         'category': 'natural-programs',
         'description': 'Accumulate lines in sed hold space and print the result.',
         'commands': ['sed'],
         'forms': ['sed:-n'],
         'program': "sed -n '1h;2H;${g;p;}' sub/a.txt",
         'tracked_files': []},
        {'id': 'natural-sort-key',
         'category': 'natural-programs',
         'description': 'Order records by a numeric field using attached key spelling.',
         'commands': ['sort'],
         'forms': ['sort:-k', 'sort:-n', 'sort:-r'],
         'program': 'sort -k3,3nr table.tsv',
         'tracked_files': []},
        {'id': 'natural-awk-field-sum',
         'category': 'natural-programs',
         'description': 'Sum a numeric column while omitting its header.',
         'commands': ['awk'],
         'forms': ['awk:-F'],
         'program': "awk -F '\\t' 'NR > 1 { sum += $3 } END { print sum }' table.tsv",
         'tracked_files': []},
        {'id': 'natural-jq-pipeline',
         'category': 'natural-programs',
         'description': 'Read JSON from a pipe and apply a filter pipeline.',
         'commands': ['cat', 'jq'],
         'forms': ['jq:-r'],
         'program': 'cat config.json | jq -r \'.items | map(ascii_upcase) | join(",")\'',
         'tracked_files': []},
        {'id': 'natural-git-log-oneline',
         'category': 'natural-programs',
         'description': 'Show the checked-in fixture commit on one line.',
         'commands': ['git log'],
         'forms': ['git log:--oneline'],
         'program': 'git log --oneline',
         'tracked_files': [],
         'needs_git': True},
        # --- Language: quoting, splitting, globbing, substitutions, statuses, pipelines, loops, functions, heredocs, redirects ---
        {
            "id": "lang-quoting-single-and-double",
            "category": "language",
            "description": "Single quotes preserve metacharacters; double quotes expand variables and escapes without splitting",
            "commands": ["printf"],
            "forms": [],
            "program": r"""x='world  wide'; printf '<%s>\n' 'a; rm -rf / | $x *' "hello $x \"quoted\" \$lit" escaped\ space""",
            "tracked_files": [],
        },
        {
            "id": "lang-word-splitting-and-empty",
            "category": "language",
            "description": "Unquoted expansions split on default IFS whitespace and drop empty words; quoted expansions stay intact",
            "commands": ["printf"],
            "forms": [],
            "program": r"""x='  one   two	three  '; empty=''; printf '<%s>\n' $empty "$empty" $x "$x" """,
            "tracked_files": [],
        },
        {
            "id": "lang-glob-sorting-and-brackets",
            "category": "language",
            "description": "Globs expand in deterministic byte order, support ? and [...], and skip dotfiles unless prefixed",
            "commands": ["printf"],
            "forms": [],
            "program": r"""printf '%s\n' sub/*.txt sub/[a-b].txt sub/?.txt .hidden* 'sub/*.txt' sub/*.nonexistent""",
            "tracked_files": [],
        },
        {
            "id": "lang-globstar-recursive",
            "category": "language",
            "description": "Recursive ** globstar matches nested files in deterministic order",
            "commands": ["printf"],
            "forms": [],
            "program": r"""printf '%s\n' **/*.go""",
            "tracked_files": [],
        },
        {
            "id": "lang-command-substitutions",
            "category": "language",
            "description": "Nested $(...) and backtick command substitutions strip trailing newlines without reparsing output",
            "commands": ["printf", "cat"],
            "forms": [],
            "program": r"""a=$(printf 'hello\n\n'); b=`cat sub/a.txt | head -n 1`; printf '%s:%s\n' "$a" "$b" """,
            "tracked_files": [],
        },
        {
            "id": "lang-parameter-operators",
            "category": "language",
            "description": "Parameter expansion default, alternative, length, and prefix/suffix trim operators",
            "commands": ["printf"],
            "forms": [],
            "program": r"""v=dir/sub/file.tar.gz; e=''; printf '%s|%s|%s|%s|%s|%s|%s|%s|%s\n' "${unset_var:-def}" "${e:-def2}" "${e-empty_ok}" "${v:+present}" "${#v}" "${v#*/}" "${v##*/}" "${v%.*}" "${v%%.*}" """,
            "tracked_files": [],
        },
        {
            "id": "lang-statuses-and-short-circuit",
            "category": "language",
            "description": "Boolean built-ins, negation, &&, ||, and sequential status propagation",
            "commands": ["true", "false", ":", "printf"],
            "forms": [],
            "program": r"""true && printf 'and:0\n'; false || printf 'or:%d\n' "$?"; false && printf 'never\n'; ! false && : && printf 'neg:%d\n' "$?" """,
            "tracked_files": [],
        },
        {
            "id": "lang-pipeline-and-early-consumer",
            "category": "language",
            "description": "Multi-stage pipeline and early consumer stop settle with the rightmost stage status",
            "commands": ["cat", "head", "tr", "printf"],
            "forms": ["head:-n", "tr:-c", "tr:-t"],
            "program": r"""cat notes.txt | tr 'a-z' 'A-Z' | head -n 2; false | true; printf 'pipe=%d\n' "$?" """,
            "tracked_files": [],
        },
        {
            "id": "lang-loops-for-while-break-continue",
            "category": "language",
            "description": "For and while loops with break and continue",
            "commands": ["printf", "[", "test", "break", "continue"],
            "forms": [],
            "program": r"""for w in one two three four; do if [ "$w" = two ]; then continue; fi; if [ "$w" = four ]; then break; fi; printf 'for:%s\n' "$w"; done; acc=x; while test "$acc" != xxxx; do printf 'while:%s\n' "$acc"; acc="${acc}x"; done""",
            "tracked_files": [],
        },
        {
            "id": "lang-conditionals-and-predicates",
            "category": "language",
            "description": "if/elif/else with test, [, and [[ string/glob/regex/file predicates",
            "commands": ["test", "[", "[["],
            "forms": [],
            "program": r"""if test -f missing.txt; then printf 'bad\n'; elif [ -f notes.txt ] && [ -d sub ] && [[ "alpha_1" == alpha_* ]] && [[ "beta42" =~ ^beta[0-9]+$ ]]; then printf 'cond-ok\n'; else printf 'else\n'; fi""",
            "tracked_files": [],
        },
        {
            "id": "lang-test-nonempty-file-predicate",
            "category": "language",
            "description": "POSIX test/[ -s non-empty file predicate",
            "commands": ["test", "["],
            "forms": [],
            "program": r"""[ -s notes.txt ] && test ! -s empty.txt && printf 'size-ok\n'""",
            "tracked_files": [],
        },
        {
            "id": "lang-functions-and-frames",
            "category": "language",
            "description": "Shell functions, positional parameters, return statuses, brace groups vs subshells",
            "commands": ["printf", "export", "unset", "return", "exit", "set"],
            "forms": [],
            "program": r"""fn() { printf 'fn:%s:%d\n' "$1" "$#"; return 4; }; fn 'arg one' two || printf 'ret=%d\n' "$?"; v=outer; { v=brace; }; ( v=subshell ); export EXP=1; unset EXP; printf 'v=%s exp=%s\n' "$v" "${EXP:-unset}" """,
            "tracked_files": [],
        },
        {
            "id": "lang-heredocs-literal-expanded-tab",
            "category": "language",
            "description": "Literal <<'EOF', expanded <<EOF, and tab-stripped <<-EOF here-documents",
            "commands": ["cat"],
            "forms": [],
            "program": "v=val\ncat <<'EOF'\nlit:$v\nEOF\ncat <<EOF\nexp:$v\nEOF\ncat <<-'EOF'\n\tstripped\n\tEOF\n",
            "tracked_files": [],
        },
        {
            "id": "lang-redirects-and-streams",
            "category": "language",
            "description": "Redirects (>, >>, <, 2>&1, 2>/dev/null) and pipeline output redirect",
            "commands": ["printf", "cat", "tr", "ls"],
            "forms": [
                "ls:-p", "ls:-i", "ls:-s", "ls:-n", "ls:-L", "ls:-U",
                "ls:-c", "ls:-u", "ls:--group-directories-first", "ls:--color",
                "ls:-G", "ls:-o", "ls:-g", "ls:-f", "ls:-k", "ls:-m",
                "ls:-H", "ls:-P",
            ],
            "program": r"""printf 'first-out\n' > out.txt; printf 'second-out\n' >> out.txt; cat < out.txt; ls missing_target 2>/dev/null || printf 'masked=%d\n' "$?"; ( printf 'out-line\n'; printf 'err-line\n' >&2 ) > merged.txt 2>&1; cat sub/a.txt | tr 'a-z' 'A-Z' > sub/a_upper.txt && cat sub/a_upper.txt""",
            "tracked_files": ["out.txt", "merged.txt", "sub/a_upper.txt"],
        },
        {
            "id": "lang-same-file-redirect-publish",
            "category": "language",
            "description": "Same-file pipeline redirect (atomic stage-on-clean-exit in v1 vs pre-truncation in POSIX shells)",
            "commands": ["cat", "tr"],
            "forms": [],
            "program": r"""cat sub/a.txt | tr 'a-z' 'A-Z' > sub/a.txt && cat sub/a.txt""",
            "tracked_files": ["sub/a.txt"],
        },
        # --- Inspect utilities (inspect.json) ---
        {
            "id": "inspect-cat-options",
            "category": "inspect",
            "description": "cat with -n, -b, -s, -E, -T, -v, -A",
            "commands": ["cat"],
            "forms": ["cat:-n", "cat:-b", "cat:-s", "cat:-E", "cat:-T", "cat:-v", "cat:-A"],
            "program": r"""cat -n -s notes.txt; cat -b -E -T sub/a.txt; cat -A sub/a.txt; cat -v sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "inspect-head-and-tail",
            "category": "inspect",
            "description": "head (-n, -c, -q, -v, -N shorthand) and tail (-n, -n +N, -c, -q, -v)",
            "commands": ["head", "tail"],
            "forms": ["head:-n", "head:-c", "head:-q", "head:-v", "tail:-n", "tail:-c", "tail:-q", "tail:-v"],
            "program": r"""head -n 2 notes.txt; head -c 5 notes.txt; printf '\n'; head -q -n 1 sub/a.txt sub/b.txt; head -v -n 1 sub/a.txt; tail -n 2 notes.txt; tail -n +4 notes.txt; tail -c 7 sub/a.txt; tail -q -n 1 sub/a.txt sub/b.txt; tail -v -n 1 sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "inspect-nl-formatting",
            "category": "inspect",
            "description": "nl line numbering with -b, -v, -w, -s",
            "commands": ["nl"],
            "forms": ["nl:-b", "nl:-v", "nl:-w", "nl:-s"],
            "program": r"""nl -b a -v 10 -w 4 -s ': ' sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "inspect-file-signatures",
            "category": "inspect",
            "description": "file signature inspection with -b and -i",
            "commands": ["file"],
            "forms": ["file:-b", "file:-i"],
            "program": r"""file -b notes.txt binary.bin; file -b -i notes.txt binary.bin""",
            "tracked_files": [],
        },
        {
            "id": "inspect-xxd-and-hexdump",
            "category": "inspect",
            "description": "xxd (-l, -s, -g, -p) and hexdump (-C, -n, -s)",
            "commands": ["xxd", "hexdump"],
            "forms": ["xxd:-l", "xxd:-s", "xxd:-g", "xxd:-p", "hexdump:-C", "hexdump:-n", "hexdump:-s"],
            "program": r"""xxd -l 8 -s 2 -g 2 binary.bin; xxd -p -l 6 binary.bin; hexdump -C -n 8 -s 2 binary.bin""",
            "tracked_files": [],
        },
        {
            "id": "inspect-checksums",
            "category": "inspect",
            "description": "shasum (-a, -c), sha256sum (-c), sha1sum (-c), sha512sum (-c), md5sum (-c)",
            "commands": ["shasum", "sha256sum", "sha1sum", "sha512sum", "md5sum"],
            "forms": ["shasum:-a", "shasum:-c", "sha256sum:-c", "sha1sum:-c", "sha512sum:-c", "md5sum:-c"],
            "program": r"""shasum -a 256 sub/a.txt > sha.txt && shasum -a 256 -c sha.txt; sha256sum sub/a.txt > s256.txt && sha256sum -c s256.txt; sha1sum sub/a.txt > s1.txt && sha1sum -c s1.txt; sha512sum sub/a.txt > s512.txt && sha512sum -c s512.txt; md5sum sub/a.txt > m5.txt && md5sum -c m5.txt""",
            "tracked_files": ["sha.txt", "s256.txt", "s1.txt", "s512.txt", "m5.txt"],
        },
        {
            "id": "inspect-base64-gnu",
            "category": "inspect",
            "description": "base64 GNU encode (-w 0) and decode (-d) with positional file operand",
            "commands": ["base64"],
            "forms": ["base64:-w", "base64:-d"],
            "program": r"""printf 'painted-wolf' | base64 -w 0 > b64.txt && base64 -d b64.txt && printf '\n'""",
            "tracked_files": ["b64.txt"],
        },
        {
            "id": "inspect-base64-bsd",
            "category": "inspect",
            "description": "base64 BSD wrap (-b) and decode (-D) flags",
            "commands": ["base64"],
            "forms": ["base64:-b", "base64:-d"],
            "program": r"""printf 'wraptest' | base64 -b 4 > b64_bsd.txt && cat b64_bsd.txt && base64 -D < b64_bsd.txt && printf '\n'""",
            "tracked_files": ["b64_bsd.txt"],
        },
        {
            "id": "inspect-domain-builtins",
            "category": "inspect",
            "description": "Native domain built-ins outline, repo-map, survey-repo alias, and summarize",
            "commands": ["outline", "repo-map", "survey-repo", "summarize"],
            "forms": ["outline:--path", "repo-map:--path", "repo-map:--depth", "summarize:--path", "summarize:--focus"],
            "program": r"""outline --path code.go; repo-map --path . --depth 2; survey-repo --path . --depth 1; summarize --path code.go --focus Alpha""",
            "tracked_files": [],
        },
        # --- Metadata utilities (metadata.json) ---
        {
            "id": "metadata-ls-options",
            "category": "metadata",
            "description": "ls with -1, -a, -A, -d, -F, -R, -S, -r",
            "commands": ["ls"],
            "forms": ["ls:-1", "ls:-a", "ls:-A", "ls:-d", "ls:-F", "ls:-R", "ls:-S", "ls:-r", "ls:-t", "ls:-l", "ls:-h"],
            "program": r"""ls -1 sub; ls -1 -a sub; ls -1 -A .; ls -d -F sub empty.txt; ls -1 -R sub; ls -1 -S -r sub""",
            "tracked_files": [],
        },
        {
            "id": "metadata-stat-gnu-format",
            "category": "metadata",
            "description": "stat GNU -c format string and -L dereference",
            "commands": ["stat"],
            "forms": ["stat:-c", "stat:-L", "stat:--file-system"],
            "program": r"""stat -c '%s:%F' sub/a.txt; stat -L -c '%s' sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "metadata-stat-bsd-format",
            "category": "metadata",
            "description": "stat BSD -f format string",
            "commands": ["stat"],
            "forms": ["stat:-f"],
            "program": r"""stat -f '%z:%HT' sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "metadata-wc-options",
            "category": "metadata",
            "description": "wc line, word, byte, char, and max-line-length counts on files and pipelines",
            "commands": ["wc"],
            "forms": ["wc:-l", "wc:-w", "wc:-c", "wc:-m", "wc:-L"],
            "program": r"""wc -l -w -c notes.txt; wc -m -L notes.txt; printf 'alpha\nbeta' | wc -l""",
            "tracked_files": [],
        },
        {
            "id": "metadata-diff-options",
            "category": "metadata",
            "description": "diff unified (-u, -U), brief (-q), whitespace (-w, -b, -B), and recursive (-r) with status 1 on differences",
            "commands": ["diff"],
            "forms": ["diff:-u", "diff:-U", "diff:-q", "diff:-w", "diff:-b", "diff:-B", "diff:-r"],
            "program": r"""diff -q sub/a.txt sub/b.txt; printf 'q=%d\n' "$?"; diff -u -U 1 sub/a.txt sub/b.txt > diff.patch; printf 'u=%d\n' "$?"; diff -w -b -B sub/a.txt sub/a.txt; diff -r sub sub""",
            "tracked_files": ["diff.patch"],
        },
        # --- Search utilities (search.json) ---
        {
            "id": "search-grep-core-and-aliases",
            "category": "search",
            "description": "grep (-n, -i, -E, -F, -w, -x, -v, -c, -m, -o, -b, -f) and egrep/fgrep aliases",
            "commands": ["grep", "egrep", "fgrep"],
            "forms": [
                "grep:-n", "grep:-i", "grep:-E", "grep:-F", "grep:-w", "grep:-x",
                "grep:-v", "grep:-c", "grep:-m", "grep:-o", "grep:-b", "grep:-f",
                "grep:-e", "grep:-P", "grep:-q", "grep:-s", "grep:-a", "grep:-I",
                "grep:--binary-files", "grep:-G", "grep:-d", "grep:-D",
                "grep:--include-dir", "grep:-O", "grep:-p", "grep:-S", "grep:-u",
                "grep:-U", "grep:--label", "grep:--mmap",
            ],
            "program": r"""grep -n -i -e 'ALPHA' -e 'beta' notes.txt; grep -F -w -v 'beta' sub/a.txt; grep -x 'first' sub/a.txt; grep -c -m 1 'alpha' notes.txt; grep -o -b 'alpha' notes.txt; grep -f patterns.txt notes.txt; grep -P 'alpha\s+line' notes.txt; egrep 'first|third' sub/b.txt; fgrep 'first' sub/a.txt; grep -q -s 'first' sub/a.txt && printf 'quiet-ok\n'""",
            "tracked_files": [],
        },
        {
            "id": "search-grep-context-and-walk",
            "category": "search",
            "description": "grep context (-A, -B, -C) and recursive walk (-r, -R, -l, -L, -h, -H, --include, --exclude, --exclude-dir, -Z, -z, -T)",
            "commands": ["grep"],
            "forms": [
                "grep:-A", "grep:-B", "grep:-C", "grep:-r", "grep:-R", "grep:-l",
                "grep:-L", "grep:-h", "grep:-H", "grep:--include", "grep:--exclude",
                "grep:--exclude-dir", "grep:-Z", "grep:-z", "grep:-T", "grep:--line-buffered", "grep:--color",
            ],
            "program": r"""grep -A 1 -B 1 -C 1 'beta' notes.txt; grep -r -l --include='*.txt' --exclude='b.txt' --exclude-dir='nested' 'first' sub; grep -L 'third' sub/a.txt sub/b.txt; grep -H -h --line-buffered --color=never 'first' sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "search-rg-options",
            "category": "search",
            "description": "rg search and file listing options",
            "commands": ["rg"],
            "forms": [
                "rg:-n", "rg:-i", "rg:-F", "rg:-E", "rg:-P", "rg:-w", "rg:-v",
                "rg:-l", "rg:--files-without-match", "rg:-c", "rg:-o", "rg:-q",
                "rg:-H", "rg:-h", "rg:--no-filename", "rg:-s", "rg:--no-messages", "rg:-m",
                "rg:-A", "rg:-B", "rg:-C", "rg:-e", "rg:--include", "rg:--exclude",
                "rg:--exclude-dir", "rg:-g", "rg:-t", "rg:-T", "rg:-S", "rg:-u",
                "rg:--hidden", "rg:--no-ignore", "rg:--color", "rg:--files",
                "rg:-r", "rg:-L", "rg:--json",
            ],
            "program": r"""rg -n -i -e 'alpha' notes.txt; rg -F -w -c 'first' sub/a.txt; rg -l -g '*.go' 'OldName' sub; rg --files-without-match 'third' sub/a.txt sub/b.txt; rg --files sub | sort; rg -o -r 'FIRST' 'first' sub/a.txt; rg -u --hidden --no-ignore -S --color=never 'secret-dotfile' .""",
            "tracked_files": [],
        },
        {
            "id": "search-find-predicates-and-actions",
            "category": "search",
            "description": "find predicates (-name, -iname, -path, -ipath, -regex, -iregex, -type, -maxdepth, -mindepth, -size, -empty, -not, -a, -o, -prune, -print, -print0, -printf, -exec, -delete)",
            "commands": ["find", "xargs"],
            "forms": [
                "find:-name", "find:-iname", "find:-path", "find:-ipath", "find:-regex",
                "find:-iregex", "find:-type", "find:-maxdepth", "find:-mindepth",
                "find:-size", "find:-empty", "find:-mtime", "find:-newer", "find:-mmin",
                "find:-perm", "find:-user", "find:-not", "find:-a", "find:-o",
                "find:-prune", "find:-print", "find:-print0", "find:-printf",
                "find:-exec", "find:-delete", "xargs:-L", "xargs:-P", "xargs:-d",
                "xargs:-r", "xargs:-t", "xargs:-J", "xargs:-E", "xargs:-e",
                "xargs:-i", "xargs:-l", "xargs:-s", "xargs:-x", "xargs:-o",
                "find:-E", "find:-s", "find:-x", "find:-xdev", "find:-H", "find:-L",
                "find:-P", "find:-d", "find:-depth", "find:-amin", "find:-cmin",
                "find:-atime", "find:-ctime", "find:-uid", "find:-gid",
                "find:-group", "find:-true", "find:-false",
            ],
            "program": r"""find sub -mindepth 1 -maxdepth 2 -type f \( -name '*.txt' -o -iname '*.GO' \) -print | sort; find . -type f -empty -print; find sub -path '*/nested' -prune -o -type f -print | sort; find sub -type f -name 'a.txt' -printf '%f:%s\n'; find sub -type f -name '*.txt' -print0 | sort -z | xargs -0 head -n 1; find sub -type f -name 'a.txt' -exec head -n 1 {} +; cp sub/a.txt sub/del.tmp && find sub -name 'del.tmp' -delete && test ! -e sub/del.tmp && printf 'deleted-ok\n'""",
            "tracked_files": [],
        },
        # --- Pure / text transforms / environment (pure.json) ---
        {
            "id": "pure-sed-stream-and-gnu-inplace",
            "category": "pure",
            "description": "sed stream (-n, -E, -r, -e) and GNU -i in-place edit without backup argument",
            "commands": ["sed"],
            "forms": ["sed:-n", "sed:-E", "sed:-e", "sed:-i", "sed:-f", "sed:-s", "sed:-z", "sed:-I", "sed:-u"],
            "program": r"""sed -n -E -e 's/alpha/ALPHA/p' notes.txt; cp sub/a.txt sub/sed_gnu.txt && sed -i 's/first/GNU_FIRST/' sub/sed_gnu.txt && cat sub/sed_gnu.txt""",
            "tracked_files": ["sub/sed_gnu.txt"],
        },
        {
            "id": "pure-sed-bsd-inplace",
            "category": "pure",
            "description": "sed BSD -i '' in-place edit with explicit empty backup suffix",
            "commands": ["sed"],
            "forms": ["sed:-i"],
            "program": r"""cp sub/a.txt sub/sed_bsd.txt && sed -i '' 's/first/BSD_FIRST/' sub/sed_bsd.txt && cat sub/sed_bsd.txt""",
            "tracked_files": ["sub/sed_bsd.txt"],
        },
        {
            "id": "pure-awk-fields-and-vars",
            "category": "pure",
            "description": "awk with -F field separator and -v variable assignment",
            "commands": ["awk"],
            "forms": ["awk:-F", "awk:-v", "awk:-f"],
            "program": r"""awk -F '\t' -v tag=ITEM 'NR > 1 { printf "%s:%s=%d\n", tag, $2, $3 * 2 }' table.tsv""",
            "tracked_files": [],
        },
        {
            "id": "pure-echo-and-printf-leading-options",
            "category": "pure",
            "description": "echo (-n, -e, -E) and printf with dash-prefixed data arguments",
            "commands": ["echo", "printf"],
            "forms": ["echo:-n", "echo:-e", "echo:-E"],
            "program": r"""echo -n 'head:'; echo -e 'a\tb'; echo -E 'a\tb'; echo -z literal; printf '%s:%02d\n' -v 5""",
            "tracked_files": [],
        },
        {
            "id": "pure-sort-uniq-cut-tr-xargs",
            "category": "pure",
            "description": "sort (-r, -n, -u, -k, -t, -z), uniq (-c, -d, -u), cut (-f, -d, -c, -b), tr (-d, -s), xargs (-0, -n, -I)",
            "commands": ["sort", "uniq", "cut", "tr", "xargs"],
            "forms": [
                "sort:-r", "sort:-n", "sort:-u", "sort:-k", "sort:-t", "sort:-z",
                "uniq:-c", "uniq:-d", "uniq:-u", "cut:-f", "cut:-d", "cut:-c", "cut:-b",
                "tr:-d", "tr:-s", "xargs:-0", "xargs:-n", "xargs:-I",
                "sort:-h", "sort:-V", "sort:-f", "sort:-b", "sort:-s", "sort:-c", "sort:-o",
                "uniq:-i", "uniq:-f", "uniq:-s", "uniq:-w", "uniq:-z",
                "cut:-s", "cut:--complement", "cut:--output-delimiter", "cut:-n", "cut:-z", "cut:-w",
            ],
            "program": r"""sort -n numbers.txt; sort -n -r -u numbers.txt; sort -t '	' -k 3,3nr table.tsv; sort numbers.txt | uniq -c | tr -s ' '; sort numbers.txt | uniq -d; sort numbers.txt | uniq -u; cut -f 2,3 -d '	' table.tsv; cut -c 1-5 sub/a.txt; cut -b 1-5 sub/a.txt; printf 'aaabbbccc123\n' | tr -d '0-9' | tr -s 'abc' 'ABC'; printf 'sub/a.txt\0sub/b.txt\0' | xargs -0 -n 1 head -n 1; printf 'a.txt\nb.txt\n' | xargs -I {} head -n 1 sub/{}""",
            "tracked_files": [],
        },
        {
            "id": "pure-env-and-discovery",
            "category": "pure",
            "description": "env (-i, -u), printenv, which, command (-v, -V), type (-a, -t), sleep, pwd, uname, whoami, id, hostname, date",
            "commands": [
                "env", "printenv", "which", "command", "type", "sleep",
                "pwd", "uname", "whoami", "id", "hostname", "date",
            ],
            "forms": [
                "env:-i", "env:-u", "command:-v", "command:-V", "type:-a", "type:-t",
                "uname:-a", "uname:-s", "uname:-n", "uname:-r", "uname:-v", "uname:-m",
                "id:-u", "id:-g", "id:-G", "id:-n", "date:-u",
            ],
            "program": r"""export A=1 B=2; printenv A; env -u A printenv B; env -i CLEAN=yes printenv CLEAN; command -v cat >/dev/null && command -V cat >/dev/null && type -t cat >/dev/null && type -a cat >/dev/null && which cat >/dev/null && sleep 0.01 && test -n "$(pwd)" && test -n "$(uname -s)" && test -n "$(uname -m)" && test -n "$(whoami)" && test -n "$(id -u)" && test -n "$(id -g)" && test -n "$(hostname)" && test -n "$(date -u)" && printf 'env-host-ok\n'""",
            "tracked_files": [],
        },
        {
            "id": "pure-text-utilities-extended",
            "category": "pure",
            "description": "seq, tac, rev, paste, comm, join, fold, column, yes, and nproc",
            "commands": ["seq", "tac", "rev", "paste", "comm", "join", "fold", "column", "yes", "nproc"],
            "forms": [
                "seq:-w", "seq:-s", "seq:-f", "seq:-t", "tac:-s", "paste:-s", "paste:-d",
                "comm:-1", "comm:-2", "comm:-3", "comm:-i", "comm:--check-order", "comm:--nocheck-order",
                "join:-t", "join:-1", "join:-2", "join:-j", "join:-a", "join:-v", "join:-e", "join:-o", "join:-i",
                "fold:-b", "fold:-s", "fold:-w", "column:-t", "column:-s", "column:-o", "column:-c", "column:-x",
                "nproc:--all", "nproc:--ignore",
            ],
            "program": r"""seq 1 3 | tac; printf 'abc\n' | rev; paste sub/a.txt sub/b.txt; comm -12 sub/a.txt sub/b.txt; join sub/a.txt sub/b.txt; printf '123456\n' | fold -w 3; printf 'a b\nc d\n' | column -t; yes ok | head -n 2; test "$(nproc)" -ge 1 && printf 'nproc-ok\n'""",
            "tracked_files": [],
        },
        # --- Path utilities (paths.json) ---
        {
            "id": "paths-cd-basename-dirname-realpath-readlink-source",
            "category": "paths",
            "description": "cd, basename, dirname, realpath, readlink -f, native_path -0, and source",
            "commands": ["cd", "basename", "dirname", "realpath", "readlink", "native_path", "source"],
            "forms": ["readlink:-f", "native_path:-0", "cd:-L", "cd:-P", "pwd:-L", "pwd:-P"],
            "program": r"""basename sub/nested/deep.go .go; dirname sub/nested/deep.go; cd sub && test "$(basename "$(pwd)")" = sub && test "$(basename "$(realpath a.txt)")" = a.txt && test "$(basename "$(readlink -f a.txt)")" = a.txt && cd .. && source sub/script.sh""",
            "tracked_files": [],
        },
        {
            "id": "paths-native-path-conversion",
            "category": "paths",
            "description": "native_path explicit conversion with and without -0",
            "commands": ["native_path"],
            "forms": ["native_path:-0"],
            "program": r"""p=$(native_path -- sub/a.txt) && test -f "$p" && native_path -0 -- sub/a.txt >/dev/null && printf 'native-path-ok\n'""",
            "tracked_files": [],
        },
        # --- Mutation & Archive utilities (mutation.json & archive.json) ---
        {
            "id": "mutation-cp-mv-rm-rmdir-mkdir-touch-tee-mktemp-chmod-chown",
            "category": "mutation",
            "description": "mkdir (-p, -m), cp (-r, -f, -n, -p), mv (-f, -n), rm (-r, -f), rmdir, touch (-c, -m, -a), tee (-a), mktemp (-d, -t), chmod, chown",
            "commands": ["mkdir", "cp", "mv", "rm", "rmdir", "touch", "tee", "mktemp", "chmod", "chown"],
            "forms": [
                "mkdir:-p", "mkdir:-m", "cp:-r", "cp:-f", "cp:-n", "cp:-p",
                "mv:-f", "mv:-n", "rm:-r", "rm:-f", "touch:-c", "touch:-m", "touch:-a",
                "tee:-a", "mktemp:-d", "mktemp:-t", "cp:-a", "cp:-v", "cp:-c", "cp:-X",
                "mv:-v", "rm:-v", "rm:-d", "mkdir:-v", "touch:-t", "touch:-d", "touch:-r", "touch:-f",
            ],
            "program": r"""mkdir -p -m 755 work/empty_dir && cp -r -f -p sub work/sub_copy && (cp -n sub/b.txt work/sub_copy/a.txt || true) && mv -f work/sub_copy/a.txt work/moved.txt && (mv -n sub/b.txt work/moved.txt || true) && rm -f work/sub_copy/b.txt && rm -rf work/sub_copy && rmdir work/empty_dir && touch -m -a work/touched.txt && touch -c work/missing_no_create.txt && printf 't1\n' | tee work/tee.txt >/dev/null && printf 't2\n' | tee -a work/tee.txt >/dev/null && chmod 644 work/moved.txt && tmpf=$(mktemp -t cnf.XXXXXX) && tmpd=$(mktemp -d -t cnfd.XXXXXX) && test -f "$tmpf" && test -d "$tmpd" && rm -rf "$tmpf" "$tmpd" && cat work/moved.txt work/tee.txt""",
            "tracked_files": ["work/moved.txt", "work/tee.txt", "work/touched.txt", "work/missing_no_create.txt"],
        },
        {
            "id": "mutation-chown-explicit",
            "category": "mutation",
            "description": "chown explicit platform capability",
            "commands": ["chown"],
            "forms": [],
            "program": r"""chown "$(id -u)" sub/a.txt""",
            "tracked_files": [],
        },
        {
            "id": "mutation-domain-builtins",
            "category": "mutation",
            "description": "edit, replace-lines, code-rewrite, and jq-edit command built-ins",
            "commands": ["edit", "replace-lines", "code-rewrite", "jq-edit"],
            "forms": [
                "edit:--path", "edit:--old-string", "edit:--new-string", "edit:--replace-all",
                "replace-lines:--path", "replace-lines:--start", "replace-lines:--end", "replace-lines:--content",
                "code-rewrite:--pattern", "code-rewrite:--rewrite", "code-rewrite:--path", "code-rewrite:--recursive", "code-rewrite:--dry-run",
                "jq-edit:--path", "jq-edit:--query", "jq-edit:--format",
            ],
            "program": r"""edit --path sub/a.txt --old-string first --new-string EDITED --replace-all && replace-lines --path sub/b.txt --start 1 --end 1 --content REPLACED && code-rewrite --pattern 'OldName' --rewrite 'NewName' --path sub/nested/deep.go --dry-run && jq-edit --path config.json --query '.enabled = true' --format json""",
            "tracked_files": ["sub/a.txt", "sub/b.txt", "config.json"],
        },
        {
            "id": "archive-tar-gzip-bzip2-zip-unzip",
            "category": "archive",
            "description": "tar (-c, -x, -t, -z, -j, -f, -C, --strip-components), gzip, gunzip, zcat, zip (-r, -q), unzip (-l, -p, -d, -o, -q)",
            "commands": ["tar", "gzip", "gunzip", "zcat", "zip", "unzip"],
            "forms": [
                "tar:-c", "tar:-x", "tar:-t", "tar:-z", "tar:-j", "tar:-f", "tar:-C", "tar:--strip-components",
                "zip:-r", "zip:-q", "unzip:-l", "unzip:-p", "unzip:-d", "unzip:-o", "unzip:-q",
                "tar:-v", "gzip:-c", "gzip:-d", "gzip:-f", "gzip:-k", "gzip:-l", "gzip:-1",
                "gzip:--fast", "gzip:--best", "gzip:-n", "gzip:-N", "gzip:-q", "gzip:-t",
                "gunzip:-c", "gunzip:-f", "gunzip:-k", "gunzip:-l", "gunzip:-q", "gunzip:-t",
            ],
            "program": r"""tar -cf plain.tar sub/a.txt sub/b.txt && tar -tf plain.tar | sort && mkdir out_tar && tar -xf plain.tar -C out_tar && cat out_tar/sub/a.txt && tar -czf comp.tgz sub/a.txt && mkdir out_tgz && tar -xzf comp.tgz -C out_tgz --strip-components 1 && cat out_tgz/a.txt && tar -cjf comp.tbz2 sub/b.txt && mkdir out_tbz && tar -xjf comp.tbz2 -C out_tbz --strip-components 1 && cat out_tbz/b.txt && cp sub/a.txt sample_gz.txt && gzip sample_gz.txt && (zcat sample_gz.txt.gz || true) && gunzip sample_gz.txt.gz && cat sample_gz.txt && zip -q -r arch.zip sub/a.txt && unzip -l arch.zip >/dev/null && unzip -p arch.zip sub/a.txt && mkdir out_zip && unzip -q -o arch.zip -d out_zip && cat out_zip/sub/a.txt""",
            "tracked_files": ["out_tar/sub/a.txt", "out_tgz/a.txt", "out_tbz/b.txt", "sample_gz.txt", "out_zip/sub/a.txt"],
        },
        # --- Process utilities (process.json) ---
        {
            "id": "process-jobs-wait-kill-ps-lsof",
            "category": "process",
            "description": "Background &, jobs, ps, wait, kill -s, and lsof -i -s",
            "commands": ["jobs", "wait", "kill", "ps", "lsof", "pgrep", "pkill"],
            "forms": [
                "kill:-s", "lsof:-i", "lsof:-s",
                "ps:-e", "ps:-f", "ps:-a", "ps:-u", "ps:-x", "ps:-o",
                "pgrep:-f", "pgrep:-l", "pgrep:-a", "pgrep:-x", "pgrep:-n", "pgrep:-o",
                "pkill:-s", "pkill:-f", "pkill:-x", "pkill:-n", "pkill:-o",
            ],
            "program": r"""sleep 0.05 & h1=$!; jobs >/dev/null; ps >/dev/null; wait "$h1"; printf 'w1=%d\n' "$?"; sleep 10 & h2=$!; kill -s TERM "$h2"; wait "$h2"; printf 'w2=%d\n' "$(test "$?" -ne 0 && echo 1 || echo 0)"; lsof -i :65532 -sTCP:LISTEN >/dev/null 2>&1; printf 'lsof=%d\n' "$?" """,
            "tracked_files": [],
        },
        {
            "id": "process-external-escape-hatch",
            "category": "process",
            "description": "external -- explicit escape hatch",
            "commands": ["external"],
            "forms": [],
            "program": r"""external -- printf 'ext-ok\n'""",
            "tracked_files": [],
        },
        # --- Query utilities (query.json) ---
        {
            "id": "query-jq-options-and-transform",
            "category": "query",
            "description": "jq (-r, -c, -e, -s, -n, --arg, --argjson) and same-file redirect transform",
            "commands": ["jq"],
            "forms": [
                "jq:-r", "jq:-c", "jq:-e", "jq:-s", "jq:-n", "jq:--arg", "jq:--argjson",
                "jq:-R", "jq:-j", "jq:-a", "jq:-S", "jq:-M", "jq:--tab", "jq:--indent",
                "jq:--slurpfile", "jq:--rawfile", "jq:--args", "jq:--jsonargs", "jq:-f",
                "jq:--raw-output0", "jq:--stream", "jq:--yaml-input", "jq:--yaml-output",
            ],
            "program": r"""jq -r '.items[]' config.json; jq -n -c --arg k greeting --argjson v 7 '{($k): $v}'; printf '2\n3\n' | jq -s -c 'add'; printf 'false\n' | jq -e . >/dev/null; printf 'jq_e=%d\n' "$?"; cat config.json | jq -c '.enabled = true' > config.json && cat config.json""",
            "tracked_files": ["config.json"],
        },
        {
            "id": "query-yq-options",
            "category": "query",
            "description": "yq (-r, -c, -e, -s, -n, --arg, --argjson) over YAML input",
            "commands": ["yq"],
            "forms": [
                "yq:-r", "yq:-c", "yq:-e", "yq:-s", "yq:-n", "yq:--arg", "yq:--argjson",
                "yq:-i", "yq:-P", "yq:-N", "yq:-M", "yq:-0", "yq:-j", "yq:-I", "yq:-o",
                "yq:-p", "yq:--expression", "yq:-y", "yq:-Y", "yq:--slurpfile",
                "yq:--rawfile", "yq:--args", "yq:--jsonargs", "yq:-S", "yq:--tab",
                "yq:-a", "yq:-R", "yq:--raw-output0", "yq:--stream",
            ],
            "program": r"""yq -r '.version' data.yaml""",
            "tracked_files": [],
        },
        # --- Git utilities (git.json) ---
        {
            "id": "git-read-and-mutation-subcommands",
            "category": "git",
            "description": "git status, diff, log, show, blame, branch, tag, rev-parse, rev-list, merge-base, remote, config, stash, worktree, add, commit, restore, checkout, switch, merge, reset, clean",
            "commands": [
                "git status", "git diff", "git log", "git show", "git blame",
                "git branch", "git tag", "git rev-parse", "git rev-list",
                "git merge-base", "git remote", "git config", "git stash list",
                "git worktree list", "git add", "git commit", "git restore",
                "git checkout", "git switch", "git merge", "git stash",
                "git reset", "git clean", "git worktree", "git fetch",
                "git pull", "git push", "git ls-remote", "git",
                "git ls-files", "git cherry-pick", "git revert", "git rebase", "git clone",
            ],
            "forms": [
                "git:-C", "git:-c",
                "git status:-C", "git status:-c", "git status:-s", "git status:--porcelain", "git status:--untracked-files",
                "git diff:-C", "git diff:-c", "git diff:--cached", "git diff:--stat", "git diff:-U",
                "git log:-C", "git log:-c", "git log:-n", "git log:--oneline",
                "git show:-C", "git show:-c", "git show:--stat",
                "git blame:-C", "git blame:-c", "git blame:-L",
                "git branch:-C", "git branch:-c", "git branch:--list", "git branch:-a",
                "git tag:-C", "git tag:-c", "git tag:--list",
                "git rev-parse:-C", "git rev-parse:-c", "git rev-parse:--verify",
                "git rev-list:-C", "git rev-list:-c", "git rev-list:--count", "git rev-list:-n",
                "git merge-base:-C", "git merge-base:-c",
                "git remote:-C", "git remote:-c", "git remote:-v",
                "git config:-C", "git config:-c", "git config:--get", "git config:--list",
                "git stash list:-C", "git stash list:-c",
                "git worktree list:-C", "git worktree list:-c", "git worktree list:--porcelain",
                "git add:-C", "git add:-c",
                "git commit:-C", "git commit:-c", "git commit:-m", "git commit:--amend",
                "git restore:-C", "git restore:-c", "git restore:--staged", "git restore:--worktree", "git restore:--source",
                "git checkout:-C", "git checkout:-c", "git checkout:-b",
                "git switch:-C", "git switch:-c",
                "git merge:-C", "git merge:-c", "git merge:--ff-only", "git merge:--abort",
                "git stash:-C", "git stash:-c", "git stash:-m",
                "git reset:-C", "git reset:-c", "git reset:--soft", "git reset:--mixed", "git reset:--hard",
                "git clean:-C", "git clean:-c", "git clean:-f", "git clean:-d",
                "git worktree:-C", "git worktree:-c",
                "git fetch:-C", "git fetch:-c",
                "git pull:-C", "git pull:-c", "git pull:--ff-only",
                "git push:-C", "git push:-c",
                "git ls-remote:-C", "git ls-remote:-c",
                "git status:--no-pager", "git status:-b", "git status:-z", "git status:-u", "git status:--ignored",
                "git diff:--no-pager", "git diff:--name-only", "git diff:--name-status", "git diff:--numstat",
                "git diff:--shortstat", "git diff:--unified", "git diff:--exit-code", "git diff:--quiet",
                "git diff:-w", "git diff:--no-color", "git diff:-z",
                "git log:--no-pager", "git log:--stat", "git log:--name-only", "git log:--name-status",
                "git log:-p", "git log:--format", "git log:--graph", "git log:--all", "git log:--reverse",
                "git log:--first-parent", "git log:--no-merges", "git log:--decorate", "git log:--since",
                "git log:--until", "git log:--author", "git log:--grep", "git log:--abbrev-commit", "git log:--no-color",
                "git show:--no-pager", "git show:--name-only", "git show:--name-status", "git show:-s",
                "git show:--format", "git show:--oneline", "git show:--no-color",
                "git blame:--no-pager", "git blame:-s", "git blame:-e", "git blame:--porcelain",
                "git branch:--no-pager", "git branch:-r", "git branch:-v", "git branch:--show-current",
                "git branch:--contains", "git branch:--merged", "git branch:--no-color",
                "git tag:--no-pager", "git tag:-l",
                "git rev-parse:--no-pager", "git rev-parse:--short", "git rev-parse:--abbrev-ref",
                "git rev-parse:--show-toplevel", "git rev-parse:--git-dir", "git rev-parse:--is-inside-work-tree",
                "git rev-parse:--show-prefix", "git rev-parse:--symbolic-full-name", "git rev-parse:-q",
                "git rev-list:--no-pager", "git rev-list:--max-count", "git rev-list:--reverse",
                "git rev-list:--first-parent", "git rev-list:--no-merges",
                "git merge-base:--no-pager", "git merge-base:--is-ancestor",
                "git remote:--no-pager",
                "git config:--no-pager", "git config:--get-regexp", "git config:-l",
                "git stash list:--no-pager",
                "git worktree list:--no-pager",
                "git ls-files:-C", "git ls-files:-c", "git ls-files:--no-pager", "git ls-files:-m",
                "git ls-files:-o", "git ls-files:-d", "git ls-files:-s", "git ls-files:--exclude-standard", "git ls-files:-z",
                "git add:--no-pager", "git add:-A", "git add:-u", "git add:-q",
                "git commit:--no-pager", "git commit:-a", "git commit:--no-edit", "git commit:-q",
                "git restore:--no-pager", "git restore:-S", "git restore:-W", "git restore:-s",
                "git checkout:--no-pager", "git checkout:--detach", "git checkout:-q",
                "git switch:--no-pager", "git switch:--detach", "git switch:-q",
                "git merge:--no-pager", "git merge:--no-ff", "git merge:--ff", "git merge:--no-commit",
                "git merge:--continue", "git merge:-m", "git merge:--no-edit", "git merge:-q",
                "git cherry-pick:-C", "git cherry-pick:-c", "git cherry-pick:--no-pager",
                "git cherry-pick:--continue", "git cherry-pick:--abort", "git cherry-pick:--skip", "git cherry-pick:-n",
                "git revert:-C", "git revert:-c", "git revert:--no-pager", "git revert:--continue",
                "git revert:--abort", "git revert:--skip", "git revert:-n", "git revert:--no-edit",
                "git rebase:-C", "git rebase:-c", "git rebase:--no-pager", "git rebase:--continue",
                "git rebase:--abort", "git rebase:--skip",
                "git stash:--no-pager", "git stash:-u", "git stash:--index", "git stash:-q",
                "git reset:--no-pager", "git reset:--keep", "git reset:--merge", "git reset:-q",
                "git clean:--no-pager", "git clean:-n", "git clean:-q",
                "git worktree:--no-pager", "git worktree:-b",
                "git fetch:--no-pager", "git fetch:--all", "git fetch:-p", "git fetch:-t", "git fetch:-q",
                "git clone:-C", "git clone:-c", "git clone:--no-pager", "git clone:-b", "git clone:--depth", "git clone:-q",
                "git pull:--no-pager", "git pull:--rebase", "git pull:--no-rebase", "git pull:-q",
                "git push:--no-pager", "git push:-u", "git push:--tags", "git push:-q",
                "git ls-remote:--no-pager", "git ls-remote:--heads", "git ls-remote:--tags", "git ls-remote:-q",
                "git:--no-pager",
            ],
            "program": r"""git status -s --untracked-files=no; git log -n 1 --oneline; git rev-parse --verify HEAD >/dev/null && git rev-list --count -n 1 HEAD; git branch --list; git tag --list; git config --get user.email; git stash list; printf 'modified\n' >> sub/a.txt && git diff -U1 sub/a.txt | grep '^+modified' && git add sub/a.txt && git diff --cached --stat | grep 'a.txt' >/dev/null && git restore --staged --worktree sub/a.txt && git status --porcelain --untracked-files=no""",
            "tracked_files": ["sub/a.txt"],
            "needs_git": True,
        },
        {
            "id": "git-diff-separated-u-flag",
            "category": "git",
            "description": "git diff with separated -U 1 flag as documented in git.json",
            "commands": ["git diff"],
            "forms": ["git diff:-U"],
            "program": r"""printf 'modified\n' >> sub/a.txt && git diff -U 1 sub/a.txt""",
            "tracked_files": ["sub/a.txt"],
            "needs_git": True,
        },
        {
            "id": "git-global-flag-before-subcommand",
            "category": "git",
            "description": "git with global -C and -c options before subcommand",
            "commands": ["git status"],
            "forms": ["git status:-C", "git status:-c", "git status:-s"],
            "program": r"""git -C . -c color.ui=false status -s --untracked-files=no""",
            "tracked_files": [],
            "needs_git": True,
        },
        # --- HTTP utility (http.json) ---
        {
            "id": "http-curl-options",
            "capability_request": {"loopback_connect": {"ports": [1]}},
            "category": "http",
            "description": "curl catalogued options over unreachable loopback port",
            "commands": ["curl"],
            "forms": [
                "curl:-s", "curl:-S", "curl:-i", "curl:-I", "curl:-L", "curl:-X",
                "curl:-H", "curl:-d", "curl:--data-raw", "curl:--data-binary",
                "curl:--data-urlencode", "curl:-F", "curl:-u", "curl:-o", "curl:-D",
                "curl:-w", "curl:-c", "curl:-b", "curl:--max-time",
                "curl:--connect-timeout", "curl:--unix-socket", "curl:--fail",
                "curl:-f", "curl:-k", "curl:--compressed", "curl:-A", "curl:-G", "curl:--json",
            ],
            "program": r"""curl -s -S --fail --max-time 1 --connect-timeout 1 -X POST -H 'X-Test: 1' --data-raw 'a=1' http://127.0.0.1:1/test 2>/dev/null || printf 'curl-failed\n'""",
            "tracked_files": [],
        },
    ]


def seed_workspace(root: Path, needs_git: bool = False) -> None:
    for rel, content in SEED_TEXT_FILES.items():
        target = root / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
    (root / "binary.bin").write_bytes(PNG_SAMPLE)
    if needs_git:
        env = {
            **os.environ,
            "GIT_AUTHOR_NAME": "Painted Wolf test",
            "GIT_AUTHOR_EMAIL": "test@paintedwolf.dev",
            "GIT_COMMITTER_NAME": "Painted Wolf test",
            "GIT_COMMITTER_EMAIL": "test@paintedwolf.dev",
            "GIT_AUTHOR_DATE": "2025-01-01T00:00:00Z",
            "GIT_COMMITTER_DATE": "2025-01-01T00:00:00Z",
        }
        subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, env=env, check=True)
        subprocess.run(["git", "config", "user.name", "Painted Wolf test"], cwd=root, env=env, check=True)
        subprocess.run(["git", "config", "user.email", "test@paintedwolf.dev"], cwd=root, env=env, check=True)
        subprocess.run(["git", "add", "sub/a.txt", "sub/b.txt", "notes.txt"], cwd=root, env=env, check=True)
        subprocess.run(["git", "commit", "-q", "-m", "initial fixture commit"], cwd=root, env=env, check=True)


def capture_seed_git_files() -> dict[str, str]:
    with tempfile.TemporaryDirectory(prefix="pw-conf-git-seed-") as tmp:
        root = Path(tmp)
        seed_workspace(root, needs_git=True)
        git_files: dict[str, str] = {}
        git_dir = root / ".git"
        for p in sorted(git_dir.rglob("*")):
            if p.is_file():
                rel = p.relative_to(root).as_posix()
                git_files[rel] = base64.b64encode(p.read_bytes()).decode("ascii")
        return git_files


def run_case(case: dict[str, Any], bash_bin: str, path_env: str) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix=f"pw-conf-{case['id']}-") as tmp:
        root = Path(tmp)
        seed_workspace(root, needs_git=bool(case.get("needs_git")))
        for rel, encoded in case.get("seed_binary_files", {}).items():
            p = root / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_bytes(base64.b64decode(encoded))
        env = {
            "PATH": path_env,
            "HOME": str(root),
            "TMPDIR": str(root),
            "LANG": "C",
            "LC_ALL": "C",
        }
        # Enable pipefail and globstar in bash 5 so pipelines and **/*.go behave per v1 language specification
        script = "set -o pipefail\nshopt -s globstar 2>/dev/null || true\n" + case["program"]
        proc = subprocess.run(
            [bash_bin, "-c", script],
            cwd=root,
            env=env,
            capture_output=True,
            timeout=20,
        )
        files: dict[str, str | None] = {}
        for rel in case.get("tracked_files", []):
            p = root / rel
            if p.exists() and p.is_file():
                files[rel] = p.read_text(encoding="utf-8", errors="replace")
            else:
                files[rel] = None
        return {
            "status": proc.returncode,
            "stdout": proc.stdout.decode("utf-8", errors="replace"),
            "stderr": proc.stderr.decode("utf-8", errors="replace"),
            "files": files,
        }


def capture_suite(mode: str, case_ids: set[str] | None = None) -> dict[str, dict[str, Any]]:
    if mode == "bsd":
        bash_bin = "/opt/homebrew/bin/bash" if Path("/opt/homebrew/bin/bash").exists() else "/bin/bash"
        path_env = str(Path.home() / ".local" / "bin") + ":/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin"
    else:
        bash_bin = "/bin/bash"
        path_env = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    results: dict[str, dict[str, Any]] = {}
    for case in corpus_cases():
        if case_ids is not None and case["id"] not in case_ids:
            continue
        results[case["id"]] = run_case(case, bash_bin, path_env)
    return results


def capture_gnu_in_docker(script_path: Path) -> dict[str, dict[str, Any]]:
    cmd = [
        "docker", "run", "--rm", "-u", "0",
        "-v", f"{script_path.parent}:/work:ro",
        "--entrypoint", "bash",
        DOCKER_IMAGE,
        "-c",
        (
            "apt-get update -qq && "
            "apt-get install -y -qq --no-install-recommends "
            "file xxd bsdextrautils zip unzip bzip2 curl ripgrep gawk procps lsof yq >/dev/null 2>&1 && "
            "python3 /work/capture_fixtures.py --mode gnu-inner"
        ),
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=True, timeout=180)
    return json.loads(proc.stdout)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mode", choices=["all", "bsd", "gnu-inner"], default="all")
    parser.add_argument("--case-id", action="append", default=[])
    args = parser.parse_args()

    if args.mode == "gnu-inner":
        print(json.dumps(capture_suite("gnu", set(args.case_id) if args.case_id else None), indent=2, sort_keys=True))
        return
    if args.mode == "bsd":
        print(json.dumps(capture_suite("bsd", set(args.case_id) if args.case_id else None), indent=2, sort_keys=True))
        return

    script_path = Path(__file__).resolve()
    bsd_results = capture_suite("bsd")
    gnu_results = capture_gnu_in_docker(script_path)

    cases_out = []
    for case in corpus_cases():
        cid = case["id"]
        cases_out.append({
            **case,
            "gnu": gnu_results[cid],
            "bsd": bsd_results[cid],
        })

    payload = {
        "schema_version": 1,
        "seed_text_files": SEED_TEXT_FILES,
        "seed_binary_files": {
            "binary.bin": base64.b64encode(PNG_SAMPLE).decode("ascii"),
        },
        "seed_git_files": capture_seed_git_files(),
        "gnu_environment": f"Debian GNU/Linux 13 ({DOCKER_IMAGE}): GNU bash 5.2, GNU coreutils 9.7, GNU sed 4.9, GNU grep 3.11, GNU findutils 4.10.0, GNU tar 1.35, gawk",
        "bsd_environment": "macOS Darwin arm64: BSD /usr/bin and /bin utilities with bash 5.3",
        "cases": cases_out,
    }
    out_path = script_path.parent / "fixtures.json"
    out_path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {len(cases_out)} conformance fixtures to {out_path}")


if __name__ == "__main__":
    main()
