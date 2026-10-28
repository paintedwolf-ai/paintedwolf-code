"""Retain a freshly built closure as nonrelease filesystem-cache artifacts."""

from pathlib import Path
import subprocess


def destination(root: Path, path: Path | None) -> Path | None:
    if path is None:
        return None
    if path.is_symlink() or path.exists():
        raise ValueError("closure archive must use a fresh destination")
    selected = path.resolve()
    if selected.is_relative_to(root.resolve()) or selected.is_relative_to(Path("/nix/store")):
        raise ValueError("closure archive must stay outside source and Nix store trees")
    if not selected.parent.is_dir():
        raise ValueError("closure archive parent directory must already exist")
    return selected


def copy_closure(nix: list[str], outputs: list[str], path: Path, environment: dict):
    path.mkdir(mode=0o700)
    return subprocess.run(nix + ["copy", "--to", path.as_uri(), *outputs],
                          env=environment, text=True)
