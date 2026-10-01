"""Stable out-of-tree path resolver for Painted Wolf Code artifacts, caches, and locks."""

import hashlib
import os
from pathlib import Path
import sys
import tempfile


def repo_slug(repo_root):
    """Deterministic directory slug for a repository checkout."""
    resolved = Path(repo_root).resolve()
    digest = hashlib.sha256(str(resolved).encode("utf-8")).hexdigest()[:12]
    return f"{resolved.name}-{digest}"


def user_cache_base():
    """Platform-standard cache directory root."""
    if "XDG_CACHE_HOME" in os.environ and os.environ["XDG_CACHE_HOME"].strip():
        return Path(os.environ["XDG_CACHE_HOME"]) / "paintedwolf"
    if sys.platform == "darwin":
        return Path.home() / "Library" / "Caches" / "PaintedWolf"
    if os.name == "nt":
        local_app_data = os.environ.get("LOCALAPPDATA")
        base = Path(local_app_data) if local_app_data else Path.home() / "AppData" / "Local"
        return base / "PaintedWolf" / "Cache"
    return Path.home() / ".cache" / "paintedwolf"


def artifact_root(repo_root):
    """Artifact root for verification, logs, test outputs, and jobs."""
    override = os.environ.get("PW_TEST_ARTIFACT_ROOT") or os.environ.get("PW_ARTIFACT_ROOT")
    if override and override.strip():
        return Path(override).resolve()
    return user_cache_base() / "artifacts" / repo_slug(repo_root)


def bin_dir(repo_root=None):
    """User-wide directory for downloaded and pinned third-party tools."""
    override = os.environ.get("PW_BIN_DIR")
    if override and override.strip():
        return Path(override).resolve()
    return user_cache_base() / "bin"


def build_dir(repo_root):
    """Checkout-scoped directory for executables and stages compiled from this checkout."""
    override = os.environ.get("PW_BUILD_DIR")
    if override and override.strip():
        return Path(override).resolve()
    return artifact_root(repo_root) / "bin"


def lock_root(repo_root):
    """Ephemeral lock directory in system temp storage."""
    override = os.environ.get("PW_LOCK_ROOT")
    if override and override.strip():
        return Path(override).resolve()
    slug = repo_slug(repo_root)
    if os.name == "nt":
        return Path(tempfile.gettempdir()) / "paintedwolf" / "locks" / slug
    uid = os.getuid() if hasattr(os, "getuid") else 0
    return Path(f"/tmp/paintedwolf-{uid}") / "locks" / slug


def main():
    if len(sys.argv) < 2:
        print("usage: artifact_paths.py {artifacts|bin|build|locks|cache} [repo_root]", file=sys.stderr)
        return 1
    kind = sys.argv[1]
    repo_root = Path(sys.argv[2] if len(sys.argv) > 2 else ".").resolve()

    if kind == "artifacts":
        target = artifact_root(repo_root)
    elif kind == "bin":
        target = bin_dir(repo_root)
    elif kind == "build":
        target = build_dir(repo_root)
    elif kind == "locks":
        target = lock_root(repo_root)
    elif kind == "cache":
        target = user_cache_base()
    else:
        print(f"error: unknown path kind: {kind}", file=sys.stderr)
        return 1

    sys.stdout.write(str(target) + "\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
