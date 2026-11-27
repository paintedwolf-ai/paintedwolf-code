"""In-process Go harness scaffolding (D7: no test seam in the shipped binary).

Invokes D1 Go packages via `go test` with an optional temporary `-overlay` file
so zero shipped product files or binaries are ever edited.
"""
from __future__ import annotations

from dataclasses import dataclass
import json
import pathlib
import subprocess
from typing import Any, Sequence


DEFAULT_INPROCESS_PACKAGES: tuple[str, ...] = (
    "./internal/evidence/...",
    "./internal/citationmd/...",
    "./internal/grounding/...",
)


@dataclass(frozen=True)
class InProcessPlan:
    """Declarative invocation plan for the D7 in-process lane."""

    repo_root: pathlib.Path
    packages: tuple[str, ...] = DEFAULT_INPROCESS_PACKAGES
    run_pattern: str = ""
    overlay_path: pathlib.Path | None = None
    count: int = 1

    def go_dir(self) -> pathlib.Path:
        return self.repo_root / "lycaon"

    def build_command(self) -> list[str]:
        cmd = ["go", "test", "-json", f"-count={self.count}"]
        if self.overlay_path is not None:
            cmd.append(f"-overlay={self.overlay_path}")
        if self.run_pattern:
            cmd.extend(["-run", self.run_pattern])
        cmd.extend(self.packages)
        return cmd

    def to_dict(self) -> dict[str, Any]:
        return {
            "repo_root": str(self.repo_root),
            "go_dir": str(self.go_dir()),
            "packages": list(self.packages),
            "run_pattern": self.run_pattern,
            "overlay_path": str(self.overlay_path) if self.overlay_path else None,
            "command": self.build_command(),
        }


def write_test_overlay(
    overlay_path: str | pathlib.Path,
    replacements: dict[str, str],
) -> pathlib.Path:
    """Write a Go `-overlay` JSON file mapping virtual test paths to temp files.

    Ensures no shipped file inside the repository is modified (D7).
    """
    out = pathlib.Path(overlay_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    payload = {"Replace": replacements}
    out.write_text(json.dumps(payload, indent=2, sort_keys=True), encoding="utf-8")
    return out


def run_inprocess_plan(
    plan: InProcessPlan,
    *,
    dry_run: bool = False,
    timeout: float = 300.0,
) -> dict[str, Any]:
    """Execute or dry-run an InProcessPlan without touching any shipped file."""
    cmd = plan.build_command()
    if dry_run:
        return {
            "dry_run": True,
            "cwd": str(plan.go_dir()),
            "command": cmd,
            "returncode": 0,
        }
    proc = subprocess.run(
        cmd,
        cwd=str(plan.go_dir()),
        capture_output=True,
        text=True,
        timeout=timeout,
        check=False,
    )
    return {
        "dry_run": False,
        "cwd": str(plan.go_dir()),
        "command": cmd,
        "returncode": proc.returncode,
        "stdout": proc.stdout,
        "stderr": proc.stderr,
    }
