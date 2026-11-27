"""Unified D12 owned-window native Den desktop driver for Linux and macOS (TB02).

Dispatches:
  - `linux-webkitgtk-native`: Linux owned `Gtk.Window` + `WebKit2.WebView` + `xdotool` + AT-SPI + X11 window PNG.
  - `macos-wkwebview-native`: macOS owned `NSWindow` + `WKWebView` via `webkit-harness wea-native-probe` + `NSEvent` + WKWebView PNG.
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys
import urllib.request
from typing import Any, Sequence

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.native_den_linux import parse_png_info, run_linux_native_den, run_linux_tauri_native
from wea.native_den_macos import run_macos_tauri_native
from wea.provenance import REPO_ROOT, collect_provenance


def _api_readback(api_url: str, token: str, path: str) -> Any:
    req = urllib.request.Request(
        api_url.rstrip("/") + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


def run_macos_wkwebview_native(
    url: str,
    out_dir: str | pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    webkit_harness_bin: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Run the macOS owned-window WKWebView probe (`webkit-harness wea-native-probe`)."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    screenshot_path = out / "macos-native-den-wkwebview.png"
    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )

    if webkit_harness_bin:
        cmd = [str(pathlib.Path(webkit_harness_bin).resolve()), "wea-native-probe", url, str(screenshot_path)]
    else:
        manifest = REPO_ROOT / "lycaon-den" / "src-tauri" / "Cargo.toml"
        cmd = [
            "cargo",
            "run",
            "--manifest-path",
            str(manifest),
            "-p",
            "webkit-harness",
            "--",
            "wea-native-probe",
            url,
            str(screenshot_path),
        ]

    try:
        proc = subprocess.run(cmd, cwd=str(REPO_ROOT), capture_output=True, text=True, check=False, timeout=240)
    except subprocess.TimeoutExpired:
        receipt = {
            "schema": "wea-native-den-macos-wkwebview-v1",
            "tier": "native-den-wkwebview",
            "status": "blocked",
            "blocked_reason": "webkit_harness_timeout",
            "provenance": prov,
            "passed": False,
        }
        (out / "native-den-macos-wkwebview-receipt.json").write_text(
            json.dumps(receipt, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        return receipt

    raw_json_line = ""
    for line in reversed(proc.stdout.splitlines()):
        stripped = line.strip()
        if stripped.startswith("{") and stripped.endswith("}"):
            raw_json_line = stripped
            break

    if not raw_json_line:
        receipt = {
            "schema": "wea-native-den-macos-wkwebview-v1",
            "tier": "native-den-wkwebview",
            "status": "blocked",
            "blocked_reason": f"webkit_harness_no_json_receipt (rc={proc.returncode})",
            "stderr_tail": proc.stderr[-1000:],
            "provenance": prov,
            "passed": False,
        }
        (out / "native-den-macos-wkwebview-receipt.json").write_text(
            json.dumps(receipt, indent=2, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        return receipt

    probe_receipt = json.loads(raw_json_line)
    png_info = parse_png_info(screenshot_path)

    live_api: dict[str, Any] = {}
    if api_url and token:
        live_api["health"] = _api_readback(api_url, token, "/health")
        sid = str(probe_receipt.get("session_id") or "")
        if sid:
            live_api["session"] = _api_readback(api_url, token, f"/v1/sessions/{sid}")
        else:
            live_api["projects"] = _api_readback(api_url, token, "/v1/projects")

    passed = bool(
        proc.returncode == 0
        and probe_receipt.get("passed") is True
        and probe_receipt.get("focus_changed") is True
        and png_info["valid_png"]
        and png_info["bytes"] > 0
    )
    receipt = {
        **probe_receipt,
        "tier": "native-den-wkwebview",
        "screenshot_sha256": png_info["sha256"],
        "screenshot_bytes": png_info["bytes"],
        "screenshot_dimensions": [png_info["width"], png_info["height"]],
        "live_api_readback": live_api,
        "provenance": prov,
        "passed": passed,
    }
    (out / "native-den-macos-wkwebview-receipt.json").write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return receipt


def run_desktop_driver(
    mode: str,
    url: str,
    out_dir: str | pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    webkit_harness_bin: str | pathlib.Path | None = None,
    app_bin: str | pathlib.Path | None = None,
    profile_dir: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    if mode == "linux-webkitgtk-native":
        return run_linux_native_den(
            url,
            out_dir,
            api_url=api_url,
            token=token,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
        )
    if mode == "linux-tauri-native":
        if not app_bin:
            raise ValueError("--app-bin is required for linux-tauri-native")
        return run_linux_tauri_native(
            app_bin,
            out_dir,
            api_url=api_url,
            token=token,
            profile_dir=profile_dir,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
        )
    if mode == "macos-wkwebview-native":
        return run_macos_wkwebview_native(
            url,
            out_dir,
            api_url=api_url,
            token=token,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
            webkit_harness_bin=webkit_harness_bin,
        )
    if mode == "macos-tauri-native":
        if not app_bin:
            raise ValueError("--app-bin is required for macos-tauri-native")
        return run_macos_tauri_native(
            app_bin,
            out_dir,
            api_url=api_url,
            token=token,
            profile_dir=profile_dir,
            engine_bin=engine_bin,
            engine_manifest=engine_manifest,
        )
    raise ValueError(f"unsupported desktop driver mode: {mode}")


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB02 D12 owned-window native Den desktop driver.")
    parser.add_argument(
        "--mode",
        choices=("linux-webkitgtk-native", "linux-tauri-native", "macos-wkwebview-native", "macos-tauri-native"),
        required=True,
    )
    parser.add_argument("--url", default=os.environ.get("LYCAON_E2E_BASE_URL", ""))
    parser.add_argument("--app-bin", type=pathlib.Path, default=None, help="Path to built Tauri desktop binary.")
    parser.add_argument("--profile-dir", type=pathlib.Path, default=None, help="Isolated profile dir for Tauri app.")
    parser.add_argument("--out", type=pathlib.Path, required=True)
    parser.add_argument("--api-url", default=os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token", default=os.environ.get("LYCAON_E2E_TOKEN"))
    parser.add_argument("--engine", type=pathlib.Path, default=None)
    parser.add_argument("--engine-manifest", type=pathlib.Path, default=None)
    parser.add_argument("--webkit-harness-bin", type=pathlib.Path, default=None)
    args = parser.parse_args(argv)
    if args.mode not in {"linux-tauri-native", "macos-tauri-native"} and not args.url:
        parser.error("--url (or LYCAON_E2E_BASE_URL) is required")
    if args.mode in {"linux-tauri-native", "macos-tauri-native"} and args.app_bin is None:
        parser.error(f"--app-bin is required for --mode {args.mode}")

    receipt = run_desktop_driver(
        args.mode,
        args.url,
        args.out,
        api_url=args.api_url,
        token=args.token,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
        webkit_harness_bin=args.webkit_harness_bin,
        app_bin=args.app_bin,
        profile_dir=args.profile_dir,
    )
    print(json.dumps(receipt, indent=2, sort_keys=True))
    return 0 if receipt.get("passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())
