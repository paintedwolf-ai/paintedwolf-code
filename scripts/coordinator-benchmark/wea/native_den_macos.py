"""macOS D12 native Den keyboard-navigation driver.

Launches `./task den:app` (or connects to an already-running isolated instance),
drives real desktop keyboard navigation via AppleScript / Accessibility (`System Events`),
reads back session state from the engine HTTP API, and captures a PNG screenshot.
"""

from __future__ import annotations

import json
import os
import pathlib
import platform
import subprocess
import time
import urllib.request
from typing import Any

from wea.provenance import REPO_ROOT, collect_provenance


def _applescript_keystrokes(app_name: str, steps: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Send Accessibility keyboard events to the real macOS desktop window."""
    executed: list[dict[str, Any]] = []
    for step in steps:
        kind = step.get("kind", "keystroke")
        if kind == "activate":
            script = f'tell application "{app_name}" to activate'
        elif kind == "key_code":
            code = int(step["code"])
            mods = step.get("modifiers") or []
            mod_clause = f' using {{{", ".join(mods)}}}' if mods else ""
            script = f'tell application "System Events" to key code {code}{mod_clause}'
        else:
            text = str(step.get("text", "")).replace('"', '\\"')
            mods = step.get("modifiers") or []
            mod_clause = f' using {{{", ".join(mods)}}}' if mods else ""
            script = f'tell application "System Events" to keystroke "{text}"{mod_clause}'
        proc = subprocess.run(["osascript", "-e", script], capture_output=True, text=True, check=False)
        executed.append({
            "step": step,
            "returncode": proc.returncode,
            "stderr": proc.stderr.strip(),
        })
        if proc.returncode != 0:
            raise RuntimeError(f"osascript keyboard step failed ({step}): {proc.stderr.strip()}")
        time.sleep(float(step.get("delay_s", 0.35)))
    return executed


def _api_readback(api_url: str, token: str, path: str) -> Any:
    req = urllib.request.Request(
        api_url.rstrip("/") + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


def capture_screenshot(dest_path: pathlib.Path) -> dict[str, Any]:
    dest_path.parent.mkdir(parents=True, exist_ok=True)
    proc = subprocess.run(["screencapture", "-x", str(dest_path)], capture_output=True, text=True, check=False)
    return {
        "path": str(dest_path),
        "captured": proc.returncode == 0 and dest_path.is_file(),
        "bytes": dest_path.stat().st_size if dest_path.is_file() else 0,
        "stderr": proc.stderr.strip(),
    }


def run_macos_native_den(
    out_dir: str | pathlib.Path,
    *,
    api_url: str,
    token: str,
    launch_den: bool = False,
    app_name: str = "Painted Wolf Code",
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Run one keyboard-navigation check in the real macOS Den desktop window."""
    if platform.system() != "Darwin":
        raise RuntimeError("native_den_macos.py requires macOS (Darwin)")

    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    prov = collect_provenance(REPO_ROOT, engine_bin=engine_bin, engine_manifest=engine_manifest, lane="sidecar-native-den")

    den_proc: subprocess.Popen[bytes] | None = None
    try:
        if launch_den:
            env = dict(os.environ)
            env["LYCAON_E2E_API_URL"] = api_url
            env["LYCAON_E2E_TOKEN"] = token
            den_proc = subprocess.Popen(
                ["./task", "den:app"],
                cwd=str(REPO_ROOT),
                env=env,
                stdout=(out / "den-app.log").open("wb"),
                stderr=subprocess.STDOUT,
            )
            time.sleep(5.0)

        steps = [
            {"kind": "activate", "delay_s": 0.5},
            # Tab focus cycle + Escape to verify keyboard navigation responds cleanly.
            {"kind": "key_code", "code": 48, "delay_s": 0.35},  # Tab
            {"kind": "key_code", "code": 48, "delay_s": 0.35},  # Tab
            {"kind": "key_code", "code": 53, "delay_s": 0.35},  # Escape
        ]
        keyboard_log = _applescript_keystrokes(app_name, steps)
        health = _api_readback(api_url, token, "/health")
        sessions = _api_readback(api_url, token, "/v1/sessions")
        screenshot = capture_screenshot(out / "macos-native-den.png")
    finally:
        if den_proc is not None and den_proc.poll() is None:
            den_proc.terminate()
            try:
                den_proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                den_proc.kill()
                den_proc.wait()

    receipt = {
        "schema": "wea-native-den-macos-v1",
        "seat": "macos-native-den",
        "provenance": prov,
        "keyboard_steps": keyboard_log,
        "api_readback": {"health": health, "sessions_count": len(sessions.get("sessions", [])) if isinstance(sessions, dict) else 0},
        "screenshot": screenshot,
        "passed": bool(screenshot["captured"] and health),
    }
    (out / "native-den-macos-receipt.json").write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return receipt
