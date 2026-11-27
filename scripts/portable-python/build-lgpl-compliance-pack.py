#!/usr/bin/env python3
"""Build the LGPL-3.0 compliance pack (fpdf2 + pygal), reconcile staged image
closures against THIRD_PARTY_NOTICES.txt, and optionally generate a harmless
visible-change relinked image for relink & mixed-identity rejection proofs.
"""

from __future__ import annotations

import argparse
import difflib
import hashlib
import json
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
import zipfile
from pathlib import Path
from typing import Any

SCRIPT_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(SCRIPT_DIR))
from image_assets import apply_wasi_package_patches  # noqa: E402


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def download_verified(url: str, dest: Path, expected_sha256: str) -> Path:
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.is_file() and sha256_file(dest) == expected_sha256:
        return dest
    req = urllib.request.Request(url, headers={"User-Agent": "pw-lgpl-compliance/1.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read()
    got = hashlib.sha256(data).hexdigest()
    if got != expected_sha256:
        raise RuntimeError(f"SHA-256 mismatch for {url}: got {got}, want {expected_sha256}")
    dest.write_bytes(data)
    return dest


def extract_license_and_copyright(sdist_path: Path, out_dir: Path) -> dict[str, Any]:
    """Extract LICENSE/COPYING files and copyright lines from an upstream sdist."""
    out_dir.mkdir(parents=True, exist_ok=True)
    extracted_licenses: list[str] = []
    copyright_lines: list[str] = []
    with tarfile.open(sdist_path, "r:gz") as tf:
        for m in tf.getmembers():
            if not m.isfile():
                continue
            base = Path(m.name).name.upper()
            if base.startswith(("LICENSE", "COPYING", "NOTICE")):
                fh = tf.extractfile(m)
                if fh is None:
                    continue
                content = fh.read()
                dest = out_dir / Path(m.name).name
                dest.write_bytes(content)
                extracted_licenses.append(dest.name)
                for line in content.decode("utf-8", errors="replace").splitlines():
                    if "copyright" in line.lower() and line.strip() not in copyright_lines:
                        copyright_lines.append(line.strip())
            elif m.name.endswith("PKG-INFO") or m.name.endswith("pyproject.toml"):
                fh = tf.extractfile(m)
                if fh is None:
                    continue
                for line in fh.read().decode("utf-8", errors="replace").splitlines():
                    if "copyright" in line.lower() and line.strip() not in copyright_lines:
                        copyright_lines.append(line.strip())
    if not extracted_licenses:
        raise RuntimeError(f"No LICENSE/COPYING file found in {sdist_path.name}")
    return {
        "sdist": sdist_path.name,
        "sdist_sha256": sha256_file(sdist_path),
        "license_files": sorted(set(extracted_licenses)),
        "copyright_notices": copyright_lines[:20],
    }


def export_fpdf2_patch(fpdf2_sdist: Path, patch_out: Path) -> str:
    """Generate the exact unified diff of the local WASI patch on fpdf/image_parsing.py."""
    with tempfile.TemporaryDirectory(prefix="pw-fpdf2-patch-") as td:
        tmp = Path(td)
        orig_text: str | None = None
        with tarfile.open(fpdf2_sdist, "r:gz") as tf:
            for m in tf.getmembers():
                if m.isfile() and m.name.endswith("/fpdf/image_parsing.py"):
                    fh = tf.extractfile(m)
                    assert fh is not None
                    orig_text = fh.read().decode("utf-8")
                    break
        if orig_text is None:
            raise RuntimeError(f"fpdf/image_parsing.py not found in {fpdf2_sdist.name}")
        target = tmp / "fpdf" / "image_parsing.py"
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(orig_text, encoding="utf-8")
        patches = apply_wasi_package_patches(tmp, lambda p: ("sha256=test", p.stat().st_size))
        if not any(p["target"] == "packages/fpdf/image_parsing.py" for p in patches):
            raise RuntimeError("fpdf2 WASI patch did not apply cleanly to upstream fpdf/image_parsing.py")
        patched_text = target.read_text(encoding="utf-8")
        diff = "".join(
            difflib.unified_diff(
                orig_text.splitlines(keepends=True),
                patched_text.splitlines(keepends=True),
                fromfile="a/fpdf/image_parsing.py",
                tofile="b/fpdf/image_parsing.py",
            )
        )
        patch_out.parent.mkdir(parents=True, exist_ok=True)
        patch_out.write_text(diff, encoding="utf-8")
        return sha256_file(patch_out)


def reconcile_staged_image(image_dir: Path) -> dict[str, Any]:
    """Reconcile every staged distribution, resource, font, and transitive license
    against image-manifest.json and THIRD_PARTY_NOTICES.txt.
    """
    manifest_path = image_dir / "image-manifest.json"
    notices_path = image_dir / "THIRD_PARTY_NOTICES.txt"
    if not manifest_path.is_file():
        raise RuntimeError(f"Missing image-manifest.json in {image_dir}")
    if not notices_path.is_file():
        raise RuntimeError(f"Missing THIRD_PARTY_NOTICES.txt in {image_dir}")

    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    notices_text = notices_path.read_text(encoding="utf-8")

    # 1. Every .dist-info on disk must appear in manifest and notices
    packages_dir = image_dir / manifest.get("packages_root", "packages")
    disk_dist_infos = sorted(p.name for p in packages_dir.glob("*.dist-info") if p.is_dir())
    manifest_dist_infos = sorted(Path(d["dist_info_path"]).name for d in manifest["distributions"])
    if disk_dist_infos != manifest_dist_infos:
        raise RuntimeError(
            f"Staged .dist-info mismatch: disk={set(disk_dist_infos) - set(manifest_dist_infos)} "
            f"manifest={set(manifest_dist_infos) - set(disk_dist_infos)}"
        )

    missing_notices: list[str] = []
    lgpl_entries: list[dict[str, str]] = []
    for d in manifest["distributions"]:
        if not d.get("license"):
            raise RuntimeError(f"Distribution {d['name']} missing license in image-manifest.json")
        expected_line = f"- {d['name']} {d['version']} ({d['license']})"
        if expected_line not in notices_text:
            missing_notices.append(expected_line)
        if "LGPL" in d["license"].upper():
            lgpl_entries.append({"name": d["name"], "version": d["version"], "license": d["license"]})
        if "GPL" in d["license"].upper() and "LGPL" not in d["license"].upper():
            raise RuntimeError(f"Disallowed GPL-only distribution in guest image: {d['name']} ({d['license']})")

    # 2. Every bundled font and its license file must exist and appear in notices
    for font in manifest.get("bundled_fonts") or []:
        if not (image_dir / font["image_path"]).is_file():
            raise RuntimeError(f"Missing staged font: {font['image_path']}")
        if not (image_dir / font["license_path"]).is_file():
            raise RuntimeError(f"Missing staged font license: {font['license_path']}")
        if font["guest_path"] not in notices_text:
            missing_notices.append(f"font:{font['guest_path']}")

    if missing_notices:
        raise RuntimeError(f"THIRD_PARTY_NOTICES.txt missing entries: {missing_notices[:10]}")

    return {
        "image_id": manifest["image_id"],
        "distribution_count": len(manifest["distributions"]),
        "public_count": sum(1 for d in manifest["distributions"] if d["public"]),
        "supporting_count": sum(1 for d in manifest["distributions"] if not d["public"]),
        "bundled_font_count": len(manifest.get("bundled_fonts") or []),
        "lgpl_distributions": lgpl_entries,
        "reconciled": True,
    }


def make_modified_lgpl_wheels(
    lock: dict[str, Any],
    cache_dir: Path,
    work_dir: Path,
    marker: str,
) -> tuple[Path, Path]:
    """Produce a copy of lock.json and a local wheel cache where fpdf2 and pygal
    carry a harmless visible marker for LGPL relink verification.
    """
    mod_cache = work_dir / "cache"
    shutil.copytree(cache_dir, mod_cache, dirs_exist_ok=True)
    mod_lock = json.loads(json.dumps(lock))

    for d in mod_lock["distributions"]:
        if d["normalized_name"] not in ("fpdf2", "pygal"):
            continue
        whl_path = mod_cache / "packages" / d["filename"]
        if not whl_path.is_file():
            raise RuntimeError(f"Cached wheel not found for relink demo: {whl_path}")
        mod_whl = mod_cache / "packages" / f"relinked-{d['filename']}"
        with zipfile.ZipFile(whl_path, "r") as zin, zipfile.ZipFile(mod_whl, "w", compression=zipfile.ZIP_DEFLATED) as zout:
            for item in zin.infolist():
                data = zin.read(item.filename)
                if d["normalized_name"] == "fpdf2" and item.filename == "fpdf/fpdf.py":
                    text = data.decode("utf-8")
                    needle = 'self.producer = f"py-pdf/fpdf{FPDF_VERSION}"'
                    if needle in text:
                        text = text.replace(needle, f'self.producer = f"py-pdf/fpdf{{FPDF_VERSION}} [{marker}]"', 1)
                    else:
                        text += f"\nPW_LGPL_RELINK_MARKER = {marker!r}\n"
                    data = text.encode("utf-8")
                elif d["normalized_name"] == "pygal" and item.filename == "pygal/graph/base.py":
                    text = data.decode("utf-8")
                    needle = "Generated with pygal"
                    if needle in text:
                        text = text.replace(needle, f"Generated with pygal [{marker}]", 1)
                    else:
                        text += f"\nPW_LGPL_RELINK_MARKER = {marker!r}\n"
                    data = text.encode("utf-8")
                zout.writestr(item, data)
        d["filename"] = mod_whl.name
        d["source_sha256"] = sha256_file(mod_whl)

    mod_lock_path = work_dir / "lock-relinked.json"
    mod_lock_path.write_text(json.dumps(mod_lock, indent=2) + "\n", encoding="utf-8")
    return mod_lock_path, mod_cache


def main() -> None:
    ap = argparse.ArgumentParser(description="Build Portable Python LGPL-3.0 compliance pack")
    ap.add_argument("--lock", type=Path, default=SCRIPT_DIR / "lock.json")
    ap.add_argument(
        "--bundled-manifest",
        type=Path,
        default=SCRIPT_DIR.parent.parent / "lycaon" / "config" / "runtime" / "portable-python" / "bundled-manifest.yaml",
    )
    ap.add_argument("--cache-dir", type=Path, required=True)
    ap.add_argument("--out-dir", type=Path, required=True)
    ap.add_argument("--reconcile-image", type=Path, default=None, help="Staged image directory to reconcile")
    ap.add_argument("--relink-demo-dir", type=Path, default=None, help="Optional dir to prepare relinked LGPL lock+cache")
    ap.add_argument("--relink-marker", default="LGPL-RELINK-VERIFIED")
    args = ap.parse_args()

    lock = json.loads(args.lock.read_text(encoding="utf-8"))
    out_dir: Path = args.out_dir
    if out_dir.exists():
        shutil.rmtree(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    summary: dict[str, Any] = {"lgpl_libraries": []}
    for entry in lock.get("lgpl_sources") or []:
        dist = entry["distribution"]
        sdist_path = download_verified(
            entry["sdist_url"],
            args.cache_dir / "lgpl-sources" / entry["sdist_filename"],
            entry["sdist_sha256"],
        )
        dist_dir = out_dir / "sources" / dist
        dist_dir.mkdir(parents=True, exist_ok=True)
        shutil.copy2(sdist_path, dist_dir / entry["sdist_filename"])
        meta = extract_license_and_copyright(sdist_path, dist_dir)
        meta["distribution"] = dist
        meta["version"] = entry["version"]
        meta["license"] = entry["license"]
        meta["local_patches"] = entry.get("local_patches") or []
        if dist == "fpdf2":
            patch_sha = export_fpdf2_patch(sdist_path, out_dir / "patches" / "fpdf2-image_parsing.patch")
            meta["exported_patch"] = "patches/fpdf2-image_parsing.patch"
            meta["exported_patch_sha256"] = patch_sha
        summary["lgpl_libraries"].append(meta)

    scripts_out = out_dir / "build-scripts"
    scripts_out.mkdir(parents=True, exist_ok=True)
    for name in ("build-image.py", "image_assets.py", "lock.json", "stage-bundled.sh", "LGPL_COMPLIANCE.md"):
        src = SCRIPT_DIR / name
        if src.is_file():
            shutil.copy2(src, scripts_out / name)
    if args.bundled_manifest.is_file():
        shutil.copy2(args.bundled_manifest, scripts_out / "bundled-manifest.yaml")

    if args.reconcile_image is not None:
        report = reconcile_staged_image(args.reconcile_image)
        summary["reconciliation"] = report
        (out_dir / "reconciliation-report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        shutil.copy2(args.reconcile_image / "THIRD_PARTY_NOTICES.txt", out_dir / "THIRD_PARTY_NOTICES.txt")

    if args.relink_demo_dir is not None:
        args.relink_demo_dir.mkdir(parents=True, exist_ok=True)
        mod_lock, mod_cache = make_modified_lgpl_wheels(lock, args.cache_dir, args.relink_demo_dir, args.relink_marker)
        summary["relink_demo"] = {
            "marker": args.relink_marker,
            "modified_lock": str(mod_lock),
            "modified_cache": str(mod_cache),
        }

    (out_dir / "compliance-manifest.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")

    # Write SHA256SUMS for every file in out_dir
    sum_lines = []
    for p in sorted(out_dir.rglob("*")):
        if p.is_file() and p.name != "SHA256SUMS":
            rel = p.relative_to(out_dir).as_posix()
            sum_lines.append(f"{sha256_file(p)}  {rel}")
    (out_dir / "SHA256SUMS").write_text("\n".join(sum_lines) + "\n", encoding="utf-8")
    print(f"[lgpl-compliance] pack written to {out_dir} ({len(sum_lines)} files)")


if __name__ == "__main__":
    main()
