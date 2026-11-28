"""In-process Go harness scaffolding (D7 & Decision 010 para 11).

Enforces:
- Explicit verified D1 `--product-root` (never defaults to the D2 harness checkout)
- Exact D1 commit (`3bb1464d22dd0a38a149d37610810e7a0324e8cf`) and tree
  (`a05c37487b1254b32be0640bcf47c0d52172f088`) verification
- Go `-overlay` constrained strictly to test-only Go files (`*_test.go`) that
  never replace any shipped or existing non-test product file
"""
from __future__ import annotations

from dataclasses import dataclass
import json
import pathlib
import subprocess
from typing import Any, Mapping


D1_EXPECTED_COMMIT: str = "3bb1464d22dd0a38a149d37610810e7a0324e8cf"
D1_EXPECTED_TREE: str = "a05c37487b1254b32be0640bcf47c0d52172f088"
HARNESS_ROOT: pathlib.Path = pathlib.Path(__file__).resolve().parents[3]

DEFAULT_INPROCESS_PACKAGES: tuple[str, ...] = (
    "./internal/evidence/...",
    "./internal/citationmd/...",
    "./internal/grounding/...",
)


def _git_rev(repo_root: pathlib.Path, rev: str) -> str:
    proc = subprocess.run(
        ["git", "-C", str(repo_root), "rev-parse", rev],
        capture_output=True,
        text=True,
        timeout=5,
        check=False,
    )
    if proc.returncode != 0 or not proc.stdout.strip():
        raise ValueError(f"Failed to resolve git {rev} in {repo_root}: {proc.stderr.strip()}")
    return proc.stdout.strip()


def _is_within(child: pathlib.Path, parent: pathlib.Path) -> bool:
    try:
        child.resolve().relative_to(parent.resolve())
        return True
    except ValueError:
        return False


def verify_d1_product_root(
    product_root: str | pathlib.Path,
    *,
    expected_commit: str = D1_EXPECTED_COMMIT,
    expected_tree: str = D1_EXPECTED_TREE,
    harness_root: str | pathlib.Path = HARNESS_ROOT,
) -> dict[str, str]:
    """Verify `product_root` is outside the harness checkout and matches pinned D1 commit/tree."""
    root = pathlib.Path(product_root).resolve()
    hroot = pathlib.Path(harness_root).resolve()
    if not root.is_dir():
        raise FileNotFoundError(f"D1 product_root not found: {root}")
    if _is_within(root, hroot):
        raise ValueError(
            f"In-process product_root={root} must not reside inside harness_root={hroot}"
        )
    commit = _git_rev(root, "HEAD")
    tree = _git_rev(root, "HEAD^{tree}")
    if expected_commit and commit != expected_commit:
        raise ValueError(
            f"D1 commit mismatch at {root}: got {commit}, expected {expected_commit}"
        )
    if expected_tree and tree != expected_tree:
        raise ValueError(
            f"D1 tree mismatch at {root}: got {tree}, expected {expected_tree}"
        )
    return {"product_root": str(root), "commit": commit, "tree": tree}


def validate_test_only_overlay(
    replacements: Mapping[str, str],
    *,
    product_root: str | pathlib.Path | None = None,
) -> dict[str, str]:
    """Ensure every Go `-overlay` entry is strictly a test-only `*_test.go` file (Decision 010).

    Rejects:
    - Any virtual target path not ending in `_test.go`
    - Any virtual target path that already exists on disk (never replaces shipped files)
    - Any non-empty replacement source path not ending in `_test.go`
    """
    validated: dict[str, str] = {}
    root = pathlib.Path(product_root).resolve() if product_root is not None else None
    for target, source in replacements.items():
        t_str = str(target)
        s_str = str(source)
        t_path = pathlib.Path(t_str)
        if not t_path.name.endswith("_test.go"):
            raise ValueError(
                f"Overlay target must be a test-only *_test.go file, got {t_str!r}"
            )
        if s_str and not pathlib.Path(s_str).name.endswith("_test.go"):
            raise ValueError(
                f"Overlay source must be a test-only *_test.go file, got {s_str!r}"
            )
        if root is not None:
            resolved_target = t_path.resolve() if t_path.is_absolute() else (root / t_path).resolve()
            if resolved_target.exists():
                raise ValueError(
                    f"Overlay target must not replace an existing file in D1 checkout: {resolved_target}"
                )
        validated[t_str] = s_str
    return validated


def write_test_overlay(
    overlay_path: str | pathlib.Path,
    replacements: Mapping[str, str],
    *,
    product_root: str | pathlib.Path | None = None,
) -> pathlib.Path:
    """Validate and write a test-only Go `-overlay` JSON file (D7 & Decision 010)."""
    validated = validate_test_only_overlay(replacements, product_root=product_root)
    out = pathlib.Path(overlay_path)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({"Replace": validated}, indent=2, sort_keys=True), encoding="utf-8")
    return out


def validate_overlay_file(
    overlay_path: str | pathlib.Path,
    *,
    product_root: str | pathlib.Path | None = None,
) -> dict[str, str]:
    """Load and validate an existing Go `-overlay` JSON file."""
    data = json.loads(pathlib.Path(overlay_path).read_text(encoding="utf-8"))
    if not isinstance(data, Mapping) or not isinstance(data.get("Replace"), Mapping):
        raise ValueError("Overlay JSON must contain a 'Replace' object")
    return validate_test_only_overlay(data["Replace"], product_root=product_root)


@dataclass(frozen=True)
class InProcessPlan:
    """Declarative invocation plan for the D7 in-process lane bound to verified D1."""

    product_root: pathlib.Path
    packages: tuple[str, ...] = DEFAULT_INPROCESS_PACKAGES
    run_pattern: str = ""
    overlay_path: pathlib.Path | None = None
    count: int = 1
    expected_commit: str = D1_EXPECTED_COMMIT
    expected_tree: str = D1_EXPECTED_TREE

    def __post_init__(self) -> None:
        object.__setattr__(self, "product_root", pathlib.Path(self.product_root).resolve())
        if self.overlay_path is not None:
            object.__setattr__(self, "overlay_path", pathlib.Path(self.overlay_path).resolve())

    def go_dir(self) -> pathlib.Path:
        return self.product_root / "lycaon"

    def verify(self) -> dict[str, Any]:
        """Verify D1 checkout identity and (if present) test-only overlay constraints."""
        identity = verify_d1_product_root(
            self.product_root,
            expected_commit=self.expected_commit,
            expected_tree=self.expected_tree,
        )
        overlay_entries: dict[str, str] = {}
        if self.overlay_path is not None:
            overlay_entries = validate_overlay_file(self.overlay_path, product_root=self.product_root)
        return {**identity, "overlay_entries": overlay_entries}

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
            "product_root": str(self.product_root),
            "go_dir": str(self.go_dir()),
            "packages": list(self.packages),
            "run_pattern": self.run_pattern,
            "overlay_path": str(self.overlay_path) if self.overlay_path else None,
            "command": self.build_command(),
        }


def run_inprocess_plan(
    plan: InProcessPlan,
    *,
    dry_run: bool = False,
    timeout: float = 300.0,
) -> dict[str, Any]:
    """Verify D1 identity & test-only overlay, then execute or dry-run `go test`."""
    verified = plan.verify()
    cmd = plan.build_command()
    if dry_run:
        return {
            "dry_run": True,
            "verified": verified,
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
        "verified": verified,
        "cwd": str(plan.go_dir()),
        "command": cmd,
        "returncode": proc.returncode,
        "stdout": proc.stdout,
        "stderr": proc.stderr,
    }
