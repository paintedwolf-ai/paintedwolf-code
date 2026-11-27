"""macOS D12 native Den keyboard-navigation driver.

Strictly binds to the owned app PID / bundle, isolated profile directory, verified
product engine identity, and target fixture session before sending Accessibility
keyboard input. Captures ONLY the owned window (`screencapture -l <CGWindowID>`),
verifies an observable focus transition, and terminates ONLY owned processes.
If ownership or window attribution cannot be proven, reports `status: "blocked"`
with the observed reason instead of passing.
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

from wea.observers import read_host_process_table
from wea.provenance import REPO_ROOT, collect_provenance


def _api_readback(api_url: str, token: str, path: str) -> Any:
    req = urllib.request.Request(
        api_url.rstrip("/") + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _osascript(script: str, *, timeout: float = 15.0) -> tuple[int, str, str]:
    try:
        proc = subprocess.run(
            ["osascript", "-e", script],
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
        return proc.returncode, proc.stdout.strip(), proc.stderr.strip()
    except subprocess.TimeoutExpired:
        return -1, "", "osascript_timeout"


def _resolve_owned_window_id(owned_pid: int, *, timeout: float = 15.0) -> int | None:
    """Resolve the CGWindowID belonging strictly to owned_pid via AppleScript/System Events."""
    script = (
        f'tell application "System Events" to tell (first process whose unix id is {owned_pid}) '
        'to get value of attribute "AXWindowNumber" of front window'
    )
    rc, out, _ = _osascript(script, timeout=timeout)
    if rc == 0 and out.isdigit() and int(out) > 0:
        return int(out)
    # Fallback via AppleScript window id query (still strictly scoped to owned_pid).
    script_alt = (
        'ObjC.import("CoreGraphics");'
        'var list = ObjC.deepUnwrap($.CGWindowListCopyWindowInfo($.kCGWindowListOptionOnScreenOnly, $.kCGNullWindowID));'
        f'var hit = list.find(function(w){{ return w.kCGWindowOwnerPID === {owned_pid} && w.kCGWindowLayer === 0; }});'
        'hit ? String(hit.kCGWindowNumber) : "";'
    )
    try:
        proc = subprocess.run(
            ["osascript", "-l", "JavaScript", "-e", script_alt],
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
    except subprocess.TimeoutExpired:
        return None
    out2 = proc.stdout.strip()
    if proc.returncode == 0 and out2.isdigit() and int(out2) > 0:
        return int(out2)
    return None


def _read_focused_element(owned_pid: int) -> str:
    """Read the AXRole + AXDescription/AXTitle of the currently focused element in owned_pid."""
    script = (
        f'tell application "System Events" to tell (first process whose unix id is {owned_pid})\n'
        '  set fe to value of attribute "AXFocusedUIElement"\n'
        '  set r to value of attribute "AXRole" of fe\n'
        '  set d to ""\n'
        '  try\n    set d to value of attribute "AXDescription" of fe\n  end try\n'
        '  return r & ":" & d\n'
        'end tell'
    )
    rc, out, _ = _osascript(script)
    return out if rc == 0 else ""


def _send_owned_keystrokes(owned_pid: int, key_codes: list[int]) -> list[dict[str, Any]]:
    """Bring owned_pid frontmost and send key codes strictly to that owned process."""
    executed: list[dict[str, Any]] = []
    rc, _, err = _osascript(
        f'tell application "System Events" to set frontmost of (first process whose unix id is {owned_pid}) to true'
    )
    executed.append({"step": "frontmost", "owned_pid": owned_pid, "returncode": rc, "stderr": err})
    if rc != 0:
        raise RuntimeError(f"failed to focus owned process {owned_pid}: {err}")
    time.sleep(0.4)

    for code in key_codes:
        rc, _, err = _osascript(f'tell application "System Events" to key code {int(code)}')
        executed.append({"step": "key_code", "code": int(code), "returncode": rc, "stderr": err})
        if rc != 0:
            raise RuntimeError(f"failed to send key code {code} to owned process {owned_pid}: {err}")
        time.sleep(0.35)
    return executed


def capture_owned_window_screenshot(window_id: int, dest_path: pathlib.Path, *, timeout: float = 15.0) -> dict[str, Any]:
    """Capture ONLY the owned CGWindowID (`screencapture -x -l <window_id>`)."""
    dest_path.parent.mkdir(parents=True, exist_ok=True)
    try:
        proc = subprocess.run(
            ["screencapture", "-x", "-l", str(int(window_id)), str(dest_path)],
            capture_output=True,
            text=True,
            check=False,
            timeout=timeout,
        )
        stderr = proc.stderr.strip()
        rc = proc.returncode
    except subprocess.TimeoutExpired:
        stderr = "screencapture_timeout"
        rc = -1
    return {
        "window_id": int(window_id),
        "path": str(dest_path),
        "captured": rc == 0 and dest_path.is_file() and dest_path.stat().st_size > 0,
        "bytes": dest_path.stat().st_size if dest_path.is_file() else 0,
        "stderr": stderr,
    }


def _write_receipt(out: pathlib.Path, receipt: dict[str, Any]) -> dict[str, Any]:
    (out / "native-den-macos-receipt.json").write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return receipt


def run_macos_native_den(
    out_dir: str | pathlib.Path,
    *,
    api_url: str,
    token: str,
    session_id: str,
    profile_dir: str | pathlib.Path,
    owned_pid: int | None = None,
    launch_den: bool = False,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Run one attributed keyboard-navigation check in the owned macOS Den desktop window."""
    if platform.system() != "Darwin":
        raise RuntimeError("native_den_macos.py requires macOS (Darwin)")

    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    profile = pathlib.Path(profile_dir).resolve()
    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )

    # Verify engine provenance and target fixture session BEFORE launching/driving UI.
    if not prov["product_source"].get("verified"):
        return _write_receipt(out, {
            "schema": "wea-native-den-macos-v1",
            "seat": "macos-native-den",
            "status": "blocked",
            "blocked_reason": "unverified_product_engine_provenance",
            "provenance": prov,
            "passed": False,
        })

    if not session_id:
        return _write_receipt(out, {
            "schema": "wea-native-den-macos-v1",
            "seat": "macos-native-den",
            "status": "blocked",
            "blocked_reason": "missing_fixture_session_id",
            "provenance": prov,
            "passed": False,
        })

    den_proc: subprocess.Popen[bytes] | None = None
    try:
        if launch_den:
            env = dict(os.environ)
            env["LYCAON_CONFIG_DIR"] = str(profile)
            env["LYCAON_E2E_API_URL"] = api_url
            env["LYCAON_E2E_TOKEN"] = token
            # Use documented `./task den:app -- --debug --no-devtools --open` isolated launch route.
            den_proc = subprocess.Popen(
                ["./task", "den:app", "--", "--debug", "--no-devtools", "--open"],
                cwd=str(REPO_ROOT),
                env=env,
                stdout=(out / "den-app.log").open("wb"),
                stderr=subprocess.STDOUT,
            )
            owned_pid = den_proc.pid
            time.sleep(5.0)

        if not owned_pid or owned_pid <= 0:
            return _write_receipt(out, {
                "schema": "wea-native-den-macos-v1",
                "seat": "macos-native-den",
                "status": "blocked",
                "blocked_reason": "no_owned_app_pid_attributed",
                "provenance": prov,
                "passed": False,
            })

        ps_table = read_host_process_table([owned_pid])
        if str(owned_pid) not in ps_table["alive"]:
            return _write_receipt(out, {
                "schema": "wea-native-den-macos-v1",
                "seat": "macos-native-den",
                "status": "blocked",
                "blocked_reason": f"owned_pid_{owned_pid}_not_alive",
                "provenance": prov,
                "host_processes": ps_table,
                "passed": False,
            })

        window_id = _resolve_owned_window_id(owned_pid)
        if window_id is None:
            return _write_receipt(out, {
                "schema": "wea-native-den-macos-v1",
                "seat": "macos-native-den",
                "status": "blocked",
                "blocked_reason": f"unable_to_attribute_cgwindow_for_pid_{owned_pid}",
                "provenance": prov,
                "host_processes": ps_table,
                "passed": False,
            })

        # Verify target fixture session exists on the attached API before driving input.
        session_before = _api_readback(api_url, token, f"/v1/sessions/{session_id}")
        focus_before = _read_focused_element(owned_pid)
        try:
            keyboard_log = _send_owned_keystrokes(owned_pid, [48, 48, 53])  # Tab, Tab, Escape
        except RuntimeError as exc:
            return _write_receipt(out, {
                "schema": "wea-native-den-macos-v1",
                "seat": "macos-native-den",
                "status": "blocked",
                "blocked_reason": str(exc),
                "owned_pid": owned_pid,
                "owned_window_id": window_id,
                "isolated_profile_dir": str(profile),
                "fixture_session_id": session_id,
                "provenance": prov,
                "host_processes": ps_table,
                "passed": False,
            })
        focus_after = _read_focused_element(owned_pid)
        session_after = _api_readback(api_url, token, f"/v1/sessions/{session_id}")
        screenshot = capture_owned_window_screenshot(window_id, out / "macos-native-den.png")

        focus_transition_observed = bool(focus_before and focus_after and focus_after != focus_before)
        passed = bool(
            screenshot["captured"]
            and session_before.get("id") == session_id
            and session_after.get("id") == session_id
            and focus_transition_observed
        )
        return _write_receipt(out, {
            "schema": "wea-native-den-macos-v1",
            "seat": "macos-native-den",
            "status": "passed" if passed else "failed",
            "owned_pid": owned_pid,
            "owned_window_id": window_id,
            "isolated_profile_dir": str(profile),
            "fixture_session_id": session_id,
            "provenance": prov,
            "host_processes": ps_table,
            "focus_transition": {
                "before": focus_before,
                "after": focus_after,
                "transitioned": focus_transition_observed,
            },
            "keyboard_steps": keyboard_log,
            "api_readback": {
                "session_id": session_after.get("id"),
                "status": session_after.get("status"),
            },
            "screenshot": screenshot,
            "passed": passed,
        })
    finally:
        # Stop ONLY the process launched by this driver.
        if den_proc is not None and den_proc.poll() is None:
            den_proc.terminate()
            try:
                den_proc.wait(timeout=15)
            except subprocess.TimeoutExpired:
                den_proc.kill()
                den_proc.wait()


def run_macos_tauri_native(
    app_bin: str | pathlib.Path,
    out_dir: str | pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    profile_dir: str | pathlib.Path | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    startup_timeout_s: float = 15.0,
) -> dict[str, Any]:
    """Launch the real macOS `painted-wolf-code` Tauri desktop binary in an isolated profile.

    Attempts owned-PID window attribution, Accessibility keyboard navigation, and owned-window
    screenshot capture. If macOS TCC / SSH GUI blocks window attribution, Accessibility, or
    Screen Recording, writes a clean `status: "blocked"` receipt with the exact observed error
    instead of crashing, and always terminates ONLY the owned process.
    """
    import hashlib

    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    receipt_path = out / "native-den-macos-tauri-receipt.json"
    app_path = pathlib.Path(app_bin).resolve()
    profile = pathlib.Path(profile_dir).resolve() if profile_dir else (out / "tauri-profile")
    profile.mkdir(parents=True, exist_ok=True)
    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )

    def _save(data: dict[str, Any]) -> dict[str, Any]:
        receipt_path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        return data

    if platform.system() != "Darwin":
        return _save({
            "schema": "wea-native-den-macos-tauri-v1",
            "tier": "native-den-tauri-app",
            "status": "blocked",
            "blocked_reason": "requires_macos_darwin",
            "app_bin": str(app_path),
            "provenance": prov,
            "passed": False,
        })

    if not app_path.is_file():
        return _save({
            "schema": "wea-native-den-macos-tauri-v1",
            "tier": "native-den-tauri-app",
            "status": "blocked",
            "blocked_reason": f"app_bin_not_found:{app_path}",
            "app_bin": str(app_path),
            "provenance": prov,
            "passed": False,
        })

    app_sha = hashlib.sha256(app_path.read_bytes()).hexdigest()
    env = dict(os.environ)
    env["LYCAON_CONFIG_DIR"] = str(profile / "config")
    env["XDG_CONFIG_HOME"] = str(profile / "xdg-config")
    env["XDG_DATA_HOME"] = str(profile / "xdg-data")
    if api_url:
        env["LYCAON_E2E_API_URL"] = api_url
    if token:
        env["LYCAON_E2E_TOKEN"] = token
        env["LYCAON_API_TOKEN"] = token

    log_fh = (out / "tauri-app.log").open("w", encoding="utf-8")
    proc = subprocess.Popen(
        [str(app_path)],
        cwd=str(REPO_ROOT),
        env=env,
        stdout=log_fh,
        stderr=subprocess.STDOUT,
    )
    try:
        deadline = time.monotonic() + startup_timeout_s
        window_id: int | None = None
        while time.monotonic() < deadline and proc.poll() is None:
            window_id = _resolve_owned_window_id(proc.pid, timeout=5.0)
            if window_id is not None:
                break
            time.sleep(0.5)

        ps_table = read_host_process_table([proc.pid])
        api_readback: dict[str, Any] = {}
        if api_url and token:
            try:
                api_readback["health"] = _api_readback(api_url, token, "/health")
                api_readback["projects"] = _api_readback(api_url, token, "/v1/projects")
            except Exception as exc:  # noqa: BLE001
                api_readback["error"] = str(exc)

        if proc.poll() is not None:
            return _save({
                "schema": "wea-native-den-macos-tauri-v1",
                "tier": "native-den-tauri-app",
                "status": "blocked",
                "blocked_reason": f"tauri_app_exited_early (rc={proc.returncode})",
                "app_bin": str(app_path),
                "app_bin_sha256": app_sha,
                "app_pid": proc.pid,
                "isolated_profile_dir": str(profile),
                "api_readback": api_readback,
                "provenance": prov,
                "passed": False,
            })

        if window_id is None:
            return _save({
                "schema": "wea-native-den-macos-tauri-v1",
                "tier": "native-den-tauri-app",
                "status": "blocked",
                "blocked_reason": f"unable_to_attribute_cgwindow_for_pid_{proc.pid}_possible_tcc_or_ssh_gui_session",
                "app_bin": str(app_path),
                "app_bin_sha256": app_sha,
                "app_pid": proc.pid,
                "app_alive": proc.poll() is None,
                "isolated_profile_dir": str(profile),
                "host_processes": ps_table,
                "api_readback": api_readback,
                "provenance": prov,
                "passed": False,
            })

        focus_before = _read_focused_element(proc.pid)
        try:
            keyboard_log = _send_owned_keystrokes(proc.pid, [48, 48, 53])
        except RuntimeError as exc:
            return _save({
                "schema": "wea-native-den-macos-tauri-v1",
                "tier": "native-den-tauri-app",
                "status": "blocked",
                "blocked_reason": f"accessibility_keyboard_blocked:{exc}",
                "app_bin": str(app_path),
                "app_bin_sha256": app_sha,
                "app_pid": proc.pid,
                "owned_window_id": window_id,
                "isolated_profile_dir": str(profile),
                "host_processes": ps_table,
                "api_readback": api_readback,
                "provenance": prov,
                "passed": False,
            })

        focus_after = _read_focused_element(proc.pid)
        screenshot = capture_owned_window_screenshot(window_id, out / "macos-native-den-tauri.png")
        if not screenshot["captured"]:
            return _save({
                "schema": "wea-native-den-macos-tauri-v1",
                "tier": "native-den-tauri-app",
                "status": "blocked",
                "blocked_reason": f"screencapture_blocked:{screenshot.get('stderr') or 'empty_capture'}",
                "app_bin": str(app_path),
                "app_bin_sha256": app_sha,
                "app_pid": proc.pid,
                "owned_window_id": window_id,
                "isolated_profile_dir": str(profile),
                "keyboard_steps": keyboard_log,
                "focus_transition": {"before": focus_before, "after": focus_after},
                "screenshot": screenshot,
                "api_readback": api_readback,
                "provenance": prov,
                "passed": False,
            })

        focus_changed = bool(focus_before and focus_after and focus_after != focus_before)
        passed = bool(screenshot["captured"] and focus_changed and proc.poll() is None)
        return _save({
            "schema": "wea-native-den-macos-tauri-v1",
            "tier": "native-den-tauri-app",
            "status": "passed" if passed else "failed",
            "app_bin": str(app_path),
            "app_bin_sha256": app_sha,
            "app_pid": proc.pid,
            "owned_window_id": window_id,
            "isolated_profile_dir": str(profile),
            "host_processes": ps_table,
            "focus_transition": {"before": focus_before, "after": focus_after, "transitioned": focus_changed},
            "keyboard_steps": keyboard_log,
            "screenshot": screenshot,
            "api_readback": api_readback,
            "provenance": prov,
            "passed": passed,
        })
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        log_fh.close()
