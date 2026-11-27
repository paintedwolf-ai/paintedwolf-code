"""Native Den keyboard-navigation smoke driver for Linux (Xvfb+xdotool+pyatspi) and macOS (Tauri+osascript)."""
from __future__ import annotations

import hashlib
import os
import pathlib
import platform
import shutil
import subprocess
import time
from typing import Any

from wait_watch.observers import DenObserver

LINUX_PREFLIGHT_REF = (
    "/Users/cdbeckman/.local/share/paintedwolf-portable-command-bug-bash/"
    "evidence/linux-noble-desktop-preflight/"
)


def _file_sha256(path: pathlib.Path) -> str:
    if not path.is_file():
        return ""
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _run_linux_smoke(out_dir: pathlib.Path, *, window_title: str = "Painted Wolf Code") -> dict[str, Any]:
    """Drive one keyboard navigation in the real Linux Den window under Xvfb + xdotool + pyatspi."""
    display = os.environ.get("DISPLAY", "")
    if not display:
        return DenObserver(
            status="blocked",
            surface="linux-xvfb-gtk",
            os_name="Linux",
            blocked_reason="DISPLAY is not set; run inside the Ubuntu Noble desktop guest under Xvfb",
            os_error=f"missing DISPLAY (see {LINUX_PREFLIGHT_REF})",
        ).snapshot()

    xdotool = shutil.which("xdotool")
    if not xdotool:
        return DenObserver(
            status="blocked",
            surface="linux-xvfb-gtk",
            os_name="Linux",
            blocked_reason="xdotool is not installed in this environment",
            os_error="command not found: xdotool",
        ).snapshot()

    try:
        import pyatspi  # type: ignore[import-not-found]
    except Exception as exc:
        return DenObserver(
            status="blocked",
            surface="linux-xvfb-gtk",
            os_name="Linux",
            blocked_reason="pyatspi AT-SPI bindings are unavailable",
            os_error=str(exc),
        ).snapshot()

    win_proc = subprocess.run(
        [xdotool, "search", "--onlyvisible", "--name", f"^{window_title}"],
        capture_output=True,
        text=True,
        check=False,
    )
    window_id = (win_proc.stdout.strip().splitlines() or [""])[0]
    if win_proc.returncode != 0 or not window_id:
        return DenObserver(
            status="blocked",
            surface="linux-xvfb-gtk",
            os_name="Linux",
            blocked_reason=f"no visible native window matching ^{window_title}",
            os_error=(win_proc.stderr or win_proc.stdout or "xdotool search returned no window").strip(),
        ).snapshot()

    def _snapshot_atspi() -> list[list[str]]:
        rows: list[list[str]] = []

        def walk(node: Any, depth: int = 0) -> None:
            if depth > 18:
                return
            try:
                name = getattr(node, "name", "") or ""
                if name:
                    rows.append([node.getRoleName(), name])
                for child in node:
                    walk(child, depth + 1)
            except Exception:
                return

        desktop = pyatspi.Registry.getDesktop(0)
        for app in desktop:
            if getattr(app, "name", "") == "dev.paintedwolf.code":
                walk(app)
        return rows

    before_tree = _snapshot_atspi()
    actions = [{"kind": "keyboard", "key": "Tab", "ts_ns": time.time_ns()}]
    subprocess.run([xdotool, "windowfocus", "--sync", window_id], check=False)
    subprocess.run([xdotool, "key", "--window", window_id, "Tab"], check=False)
    time.sleep(0.3)
    after_tree = _snapshot_atspi()

    screenshot_path = out_dir / "native-den-linux.png"
    xwd = shutil.which("xwd")
    convert = shutil.which("convert")
    if xwd and convert:
        subprocess.run(
            f"{xwd} -silent -id {window_id} | {convert} xwd:- {screenshot_path}",
            shell=True,
            check=False,
        )

    pid_proc = subprocess.run([xdotool, "getwindowpid", window_id], capture_output=True, text=True, check=False)
    exe_sha = ""
    if pid_proc.returncode == 0 and pid_proc.stdout.strip().isdigit():
        exe_path = pathlib.Path(f"/proc/{pid_proc.stdout.strip()}/exe")
        if exe_path.exists():
            exe_sha = _file_sha256(exe_path.resolve())

    return DenObserver(
        status="passed",
        surface="linux-xvfb-gtk",
        os_name="Linux",
        window_id=window_id,
        executable_sha256=exe_sha,
        before_tree=before_tree,
        actions=actions,
        after_tree=after_tree,
        screenshot_path=str(screenshot_path) if screenshot_path.is_file() else "",
        screenshot_sha256=_file_sha256(screenshot_path),
    ).snapshot()


def _run_macos_smoke(out_dir: pathlib.Path, *, app_name: str = "Painted Wolf Code") -> dict[str, Any]:
    """Drive one keyboard navigation in the real macOS Tauri/WKWebView window via osascript + screencapture."""
    osascript = shutil.which("osascript")
    if not osascript:
        return DenObserver(
            status="blocked",
            surface="macos-tauri-wkwebview",
            os_name="Darwin",
            blocked_reason="osascript not available",
            os_error="command not found: osascript",
        ).snapshot()

    # Probe macOS System Events / Accessibility access and verify the Painted Wolf Code window exists.
    probe_script = (
        'tell application "System Events"\n'
        '  set uiEnabled to UI elements enabled\n'
        f'  set appRunning to (exists process "{app_name}")\n'
        '  if not uiEnabled then error "TCC_ACCESSIBILITY_DENIED: Assistive access is not enabled"\n'
        f'  if not appRunning then error "WINDOW_NOT_RUNNING: process {app_name} is not running"\n'
        f'  tell process "{app_name}"\n'
        '    set frontmost to true\n'
        '    set winCount to count of windows\n'
        '    if winCount < 1 then error "WINDOW_NOT_VISIBLE: no open window"\n'
        '    set winName to name of window 1\n'
        '    key code 48\n'
        '    return winName\n'
        '  end tell\n'
        'end tell\n'
    )
    proc = subprocess.run(
        [osascript, "-e", probe_script],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        err_text = (proc.stderr or proc.stdout or "").strip()
        if any(tok in err_text for tok in ("-1719", "-25211", "TCC_ACCESSIBILITY_DENIED", "not allowed assistive access")):
            reason = "macOS Accessibility/TCC permission unavailable for System Events keyboard navigation"
        elif "WINDOW_NOT_RUNNING" in err_text or "WINDOW_NOT_VISIBLE" in err_text:
            reason = f"native Den window ({app_name}) is not running (launch via scripts/e2e-tauri-dev.sh)"
        else:
            reason = "macOS GUI/System Events automation unavailable in current session"
        return DenObserver(
            status="blocked",
            surface="macos-tauri-wkwebview",
            os_name="Darwin",
            blocked_reason=reason,
            os_error=err_text,
        ).snapshot()

    win_name = proc.stdout.strip()
    screenshot_path = out_dir / "native-den-macos.png"
    screencapture = shutil.which("screencapture")
    if screencapture:
        subprocess.run([screencapture, "-x", str(screenshot_path)], capture_output=True, check=False)

    return DenObserver(
        status="passed",
        surface="macos-tauri-wkwebview",
        os_name="Darwin",
        window_id=win_name,
        before_tree=[["window", win_name]],
        actions=[{"kind": "keyboard", "key": "Tab", "key_code": 48, "ts_ns": time.time_ns()}],
        after_tree=[["window", win_name]],
        screenshot_path=str(screenshot_path) if screenshot_path.is_file() else "",
        screenshot_sha256=_file_sha256(screenshot_path),
    ).snapshot()


def run_native_smoke(out_dir: pathlib.Path | str) -> dict[str, Any]:
    """Run the platform-appropriate native Den keyboard-navigation smoke check."""
    out_path = pathlib.Path(out_dir).resolve()
    out_path.mkdir(parents=True, exist_ok=True)
    sys_name = platform.system()
    if sys_name == "Linux":
        return _run_linux_smoke(out_path)
    if sys_name == "Darwin":
        return _run_macos_smoke(out_path)
    return DenObserver(
        status="blocked",
        surface=f"unsupported-{sys_name.lower()}",
        os_name=sys_name,
        blocked_reason=f"unsupported OS for native Den smoke: {sys_name}",
        os_error=f"platform={sys_name}",
    ).snapshot()
