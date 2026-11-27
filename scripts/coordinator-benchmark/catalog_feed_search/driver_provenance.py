"""D1 product binary and D2 harness provenance capture + SHA-256 evidence manifests."""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import platform
import socket
import subprocess
import tempfile

from .references import capture_rg_version


def file_sha256(path: Path) -> str:
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def _run_text(argv: list[str]) -> str:
    try:
        proc = subprocess.run(argv, capture_output=True, text=True, check=False)
        return proc.stdout.strip() if proc.returncode == 0 else ""
    except OSError:
        return ""


def probe_case_sensitive(directory: Path) -> bool:
    """Independently probe whether the filesystem backing directory is case-sensitive."""
    directory.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=str(directory)) as tmp:
        lower = Path(tmp) / "case_probe_a.tmp"
        upper = Path(tmp) / "CASE_PROBE_A.tmp"
        lower.write_bytes(b"1")
        return not upper.exists()


def parse_proc_mounts_fs(target: Path, mounts_text: str) -> str:
    """Return the filesystem type for the longest matching mountpoint prefix in /proc/mounts."""
    resolved = str(target)
    best_mount = ""
    best_fs = ""
    for line in mounts_text.splitlines():
        parts = line.split()
        if len(parts) < 3:
            continue
        mount_point = parts[1].replace("\\040", " ")
        fs_type = parts[2]
        if resolved == mount_point or resolved.startswith(mount_point.rstrip("/") + "/"):
            if len(mount_point) > len(best_mount):
                best_mount = mount_point
                best_fs = fs_type
    return best_fs


def parse_darwin_mount_fs(target: Path, mount_text: str) -> str:
    """Return filesystem type for the longest matching mountpoint in macOS `mount` output."""
    resolved = str(target)
    best_mount = ""
    best_fs = ""
    for line in mount_text.splitlines():
        if " on " not in line or " (" not in line:
            continue
        _, rest = line.split(" on ", 1)
        mount_point, paren = rest.rsplit(" (", 1)
        fs_type = paren.split(",", 1)[0].split(")", 1)[0].strip()
        if resolved == mount_point or resolved.startswith(mount_point.rstrip("/") + "/"):
            if len(mount_point) > len(best_mount):
                best_mount = mount_point
                best_fs = fs_type
    return best_fs


def detect_filesystem_type(directory: Path) -> str:
    """Best-effort filesystem type label for host evidence (apfs, ext4, etc.)."""
    sys_name = platform.system().lower()
    if sys_name == "darwin":
        out = _run_text(["mount"])
        if out:
            fs = parse_darwin_mount_fs(Path(directory).resolve(), out)
            if fs:
                return fs
        return "apfs_or_darwin"
    if sys_name == "linux":
        proc_mounts = Path("/proc/mounts")
        if proc_mounts.is_file():
            try:
                fs = parse_proc_mounts_fs(Path(directory).resolve(), proc_mounts.read_text(encoding="utf-8"))
                if fs:
                    return fs
            except OSError:
                pass
        out = _run_text(["stat", "-f", "-c", "%T", str(directory)])
        return out or "linux_unknown"
    return sys_name or "unknown"


@dataclass(frozen=True)
class BinaryProvenance:
    """D1 product binary provenance bound independently from the harness checkout."""

    source_commit: str
    source_tree: str
    binary_path: str
    binary_sha256: str
    go_buildid: str
    product_version: str
    vcs_revision: str
    vcs_modified: str

    def to_dict(self) -> dict[str, object]:
        return {
            "source_commit": self.source_commit,
            "source_tree": self.source_tree,
            "binary_path": self.binary_path,
            "binary_sha256": self.binary_sha256,
            "go_buildid": self.go_buildid,
            "product_version": self.product_version,
            "vcs_revision": self.vcs_revision,
            "vcs_modified": self.vcs_modified,
        }


@dataclass(frozen=True)
class RunProvenance:
    """Combined D1 product + D2 harness + host + reference provenance."""

    product: BinaryProvenance
    harness_commit: str
    harness_tree: str
    rg_version: str
    host: str
    platform_system: str
    platform_machine: str
    filesystem: str
    case_sensitive: bool
    clock_domain: str

    def to_dict(self) -> dict[str, object]:
        return {
            "product": self.product.to_dict(),
            "harness_commit": self.harness_commit,
            "harness_tree": self.harness_tree,
            "rg_version": self.rg_version,
            "host": self.host,
            "platform_system": self.platform_system,
            "platform_machine": self.platform_machine,
            "filesystem": self.filesystem,
            "case_sensitive": self.case_sensitive,
            "clock_domain": self.clock_domain,
        }


def inspect_product_binary(
    binary_path: Path | None,
    *,
    source_commit: str = "",
    source_tree: str = "",
) -> BinaryProvenance:
    """Inspect a built D1 product binary via SHA-256, go tool buildid, and go version -m."""
    if binary_path is None:
        return BinaryProvenance(
            source_commit=source_commit,
            source_tree=source_tree,
            binary_path="",
            binary_sha256="",
            go_buildid="",
            product_version="",
            vcs_revision="",
            vcs_modified="",
        )
    resolved = Path(binary_path).resolve()
    exists = resolved.is_file()
    sha = file_sha256(resolved) if exists else ""
    buildid = _run_text(["go", "tool", "buildid", str(resolved)]) if exists else ""
    prod_ver = _run_text([str(resolved), "--version"]) if exists else ""
    vcs_rev = ""
    vcs_mod = ""
    if exists:
        meta = _run_text(["go", "version", "-m", str(resolved)])
        for line in meta.splitlines():
            parts = line.strip().split("\t")
            if len(parts) >= 2 and parts[0] == "build" and "=" in parts[1]:
                k, v = parts[1].split("=", 1)
                if k == "vcs.revision":
                    vcs_rev = v
                elif k == "vcs.modified":
                    vcs_mod = v
    return BinaryProvenance(
        source_commit=source_commit,
        source_tree=source_tree,
        binary_path=str(resolved),
        binary_sha256=sha,
        go_buildid=buildid,
        product_version=prod_ver,
        vcs_revision=vcs_rev,
        vcs_modified=vcs_mod,
    )


def collect_run_provenance(
    *,
    harness_repo: Path,
    work_dir: Path,
    binary_path: Path | None = None,
    d1_commit: str = "",
    d1_tree: str = "",
    rg_bin: str = "rg",
    clock_domain: str = "monotonic_ns",
) -> RunProvenance:
    """Collect full D1 product + D2 harness + host + reference provenance."""
    product = inspect_product_binary(binary_path, source_commit=d1_commit, source_tree=d1_tree)
    harness_commit = _run_text(["git", "-C", str(harness_repo), "rev-parse", "HEAD"])
    harness_tree = _run_text(["git", "-C", str(harness_repo), "rev-parse", "HEAD^{tree}"])
    rg_version = capture_rg_version(rg_bin)
    return RunProvenance(
        product=product,
        harness_commit=harness_commit,
        harness_tree=harness_tree,
        rg_version=rg_version,
        host=socket.gethostname(),
        platform_system=platform.system(),
        platform_machine=platform.machine(),
        filesystem=detect_filesystem_type(work_dir),
        case_sensitive=probe_case_sensitive(work_dir),
        clock_domain=clock_domain,
    )


def write_evidence_manifest(
    out_dir: Path,
    provenance: RunProvenance,
    artifact_files: dict[str, Path],
    extra: dict[str, object] | None = None,
    *,
    replay_command: list[str] | None = None,
    launch_receipt: dict[str, object] | None = None,
) -> Path:
    """Write manifest.json with locally-resolvable relative artifact paths, replay_command, and launch_receipt."""
    out = Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    digests = {
        name: {
            "path": Path(os.path.relpath(Path(p).resolve(), out)).as_posix(),
            "sha256": file_sha256(Path(p)),
            "size_bytes": Path(p).stat().st_size,
        }
        for name, p in sorted(artifact_files.items())
    }
    payload: dict[str, object] = {
        "schema": "cfs-evidence-manifest-v1",
        "provenance": provenance.to_dict(),
        "artifacts": digests,
    }
    if replay_command is not None:
        payload["replay_command"] = list(replay_command)
    if launch_receipt is not None:
        payload["launch_receipt"] = dict(launch_receipt)
    if extra:
        payload["summary"] = extra
    manifest_path = out / "manifest.json"
    manifest_path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return manifest_path
