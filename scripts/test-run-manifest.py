#!/usr/bin/env python3
"""Persist and replay exact Go test runs."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import subprocess
import sys
from datetime import datetime, timezone


_ENV_KEYS = (
    "CGO_ENABLED",
    "CI",
    "GOARCH",
    "GODEBUG",
    "GOEXPERIMENT",
    "GOFLAGS",
    "GOOS",
    "GOMAXPROCS",
    "GO_TEST_P",
    "GO_TEST_PARALLEL",
    "LANG",
    "LC_ALL",
    "LYCAON_LLM_MOCK",
    "LYCAON_OPENGREP_CANDIDATE",
    "LYCAON_SCANNER_SUITE_REQUIRED",
    "OPENGREP_STAGE_DIR",
    "PATH",
    "PW_GO_TEST_TIMEOUT_SECONDS",
    "PW_TEST_TIMEOUT_SCALE",
    "TZ",
)


def _nul_items(path: str) -> list[str]:
    data = pathlib.Path(path).read_bytes()
    parts = data.split(b"\0")
    if parts and parts[-1] == b"":
        parts.pop()
    return [part.decode("utf-8") for part in parts]


def _read_json(path: str) -> dict:
    with open(path, encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError("manifest root must be an object")
    return value


def _atomic_json(path: str, value: dict) -> None:
    destination = pathlib.Path(path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    temporary = destination.with_name(f".{destination.name}.{os.getpid()}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(temporary, destination)


def _parse_overrides(values: list[str]) -> dict[str, str]:
    output: dict[str, str] = {}
    for value in values:
        key, separator, item = value.partition("=")
        if not separator or key not in _ENV_KEYS:
            raise ValueError(f"invalid environment override: {value}")
        output[key] = item
    return output


def _rebase_path(value: str, source_root: str, current_source_root: str) -> str:
    prefix = source_root + os.sep
    parts = []
    for part in value.split(os.pathsep):
        if part == source_root:
            part = current_source_root
        elif part.startswith(prefix):
            part = current_source_root + part[len(source_root) :]
        parts.append(part)
    return os.pathsep.join(parts)


def create(args: argparse.Namespace) -> int:
    environment: dict[str, str | None] = {key: os.environ.get(key) for key in _ENV_KEYS}
    environment.update(_parse_overrides(args.env))
    value = {
        "version": 1,
        "id": args.id,
        "name": args.name,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "source_commit": args.source_commit,
        "source_ref": args.source_ref,
        "source_root": args.source_root,
        "runner_argv": _nul_items(args.runner_argv),
        "effective_go_argv": _nul_items(args.go_argv),
        "environment": environment,
        "failed_packages": [],
    }
    _atomic_json(args.output, value)
    return 0


def finalize(args: argparse.Namespace) -> int:
    value = _read_json(args.manifest)
    packages = []
    path = pathlib.Path(args.failed_packages)
    if path.is_file():
        packages = [line.strip() for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    value["failed_packages"] = sorted(set(packages))
    _atomic_json(args.manifest, value)
    return 0


def replay(args: argparse.Namespace) -> int:
    value = _read_json(args.manifest)
    argv = value.get("runner_argv")
    environment = value.get("environment")
    source_root = value.get("source_root")
    if value.get("version") != 1 or not isinstance(argv, list) or not all(isinstance(v, str) for v in argv):
        raise ValueError("unsupported Go test manifest")
    if not isinstance(environment, dict):
        raise ValueError("manifest environment must be an object")
    if not isinstance(source_root, str) or not os.path.isabs(source_root):
        raise ValueError("manifest source root must be an absolute path")
    current_source_root = os.path.abspath(args.current_source_root)
    replay_env = os.environ.copy()
    for key in _ENV_KEYS:
        item = environment.get(key)
        if item is None:
            replay_env.pop(key, None)
        elif isinstance(item, str):
            replay_env[key] = item
        else:
            raise ValueError(f"manifest environment value for {key} must be a string or null")
    if "PATH" in replay_env:
        replay_env["PATH"] = _rebase_path(replay_env["PATH"], source_root, current_source_root)
    replay_env["PW_TEST_REPLAY_MANIFEST"] = os.path.abspath(args.manifest)
    os.execvpe(args.runner, [args.runner, *argv], replay_env)
    return 1


def source_commit(args: argparse.Namespace) -> int:
    value = _read_json(args.manifest)
    commit = value.get("source_commit")
    if not isinstance(commit, str) or not commit:
        raise ValueError("manifest has no source commit")
    print(commit)
    return 0


def go_argv(args: argparse.Namespace) -> int:
    value = _read_json(args.manifest)
    argv = value.get("effective_go_argv")
    if value.get("version") != 1 or not isinstance(argv, list) or not all(isinstance(v, str) for v in argv):
        raise ValueError("manifest has no effective Go argument vector")
    sys.stdout.buffer.write(b"\0".join(item.encode("utf-8") for item in argv) + b"\0")
    return 0


def prune(args: argparse.Namespace) -> int:
    root = pathlib.Path(args.failures_root).resolve()
    root.mkdir(parents=True, exist_ok=True)
    runs = sorted(
        (path for path in root.iterdir() if path.is_dir() and path.name.startswith("go-test-")),
        key=lambda path: path.stat().st_mtime,
        reverse=True,
    )
    for path in runs[args.keep :]:
        manifest_path = path / "manifest.json"
        if manifest_path.is_file():
            ref = _read_json(str(manifest_path)).get("source_ref")
            if isinstance(ref, str) and ref.startswith("refs/painted-wolf/test-failures/"):
                subprocess.run(["git", "-C", args.repo, "update-ref", "-d", ref], check=False)
        shutil.rmtree(path)
    return 0


def parser() -> argparse.ArgumentParser:
    top = argparse.ArgumentParser()
    commands = top.add_subparsers(dest="command", required=True)

    create_parser = commands.add_parser("create")
    create_parser.add_argument("--output", required=True)
    create_parser.add_argument("--id", required=True)
    create_parser.add_argument("--name", required=True)
    create_parser.add_argument("--source-commit", required=True)
    create_parser.add_argument("--source-ref", required=True)
    create_parser.add_argument("--source-root", required=True)
    create_parser.add_argument("--runner-argv", required=True)
    create_parser.add_argument("--go-argv", required=True)
    create_parser.add_argument("--env", action="append", default=[])
    create_parser.set_defaults(func=create)

    finalize_parser = commands.add_parser("finalize")
    finalize_parser.add_argument("--manifest", required=True)
    finalize_parser.add_argument("--failed-packages", required=True)
    finalize_parser.set_defaults(func=finalize)

    replay_parser = commands.add_parser("replay")
    replay_parser.add_argument("--manifest", required=True)
    replay_parser.add_argument("--runner", required=True)
    replay_parser.add_argument("--current-source-root", required=True)
    replay_parser.set_defaults(func=replay)

    source_parser = commands.add_parser("source-commit")
    source_parser.add_argument("--manifest", required=True)
    source_parser.set_defaults(func=source_commit)

    argv_parser = commands.add_parser("go-argv")
    argv_parser.add_argument("--manifest", required=True)
    argv_parser.set_defaults(func=go_argv)

    prune_parser = commands.add_parser("prune")
    prune_parser.add_argument("--failures-root", required=True)
    prune_parser.add_argument("--repo", required=True)
    prune_parser.add_argument("--keep", type=int, default=10)
    prune_parser.set_defaults(func=prune)
    return top


def main() -> int:
    args = parser().parse_args()
    try:
        return args.func(args)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"test-run-manifest: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
