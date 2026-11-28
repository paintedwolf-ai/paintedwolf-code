"""TB02 / D12 Linux native Den owned-window WebKitGTK + AT-SPI probe.

Opens a real `Gtk.Window` + ephemeral `WebKit2.WebView` pointed at the live harness
Den URL (`LYCAON_E2E_BASE_URL` backed by the pinned `lycaon-dev` sidecar), connects via
`window.__harness`, records the owned X11 window ID (`get_xid()`) and PID, sends real
native keyboard input (`Tab`) directly to the owned X11 window (`xdotool`), verifies both
DOM focus transition and AT-SPI accessibility observation when available, captures a real
native X11 screenshot of ONLY the owned window (`import -window <xid>` with `xwd`+`convert`
fallback), reads back live state from the Den API, and emits a structured JSON receipt.

Usage:
  Controller: python3 native_den_linux.py --url <den-url> --out <dir> [--api-url <url> --token <tok>]
  Page child: python3 native_den_linux.py --page <den-url>
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import queue
import re
import secrets
import shutil
import struct
import subprocess
import sys
import threading
import time
import urllib.request
from typing import Any

BENCHMARK_DIR = pathlib.Path(__file__).resolve().parent.parent
if str(BENCHMARK_DIR) not in sys.path:
    sys.path.insert(0, str(BENCHMARK_DIR))

from wea.observers import read_host_process_table
from wea.provenance import REPO_ROOT, collect_provenance

_LISTENING_ADDR_RE = re.compile(r"server listening[^\n]*\baddr=(127\.0\.0\.1:\d+)")

FIRST_RUN_DONE = """
localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ completedSetupRevision: 2, completedLocalModelStepRevision: 2 }));
localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
"""

WINDOW_TITLE = "WEA Native Den Probe"

SETUP_JS = r"""
for (let i = 0; i < 240 && !window.__harness; i++) await new Promise((r) => setTimeout(r, 250));
if (!window.__harness) return { ok: false, error: "harness API missing" };
for (let i = 0; i < 480 && !(__harness.state().connected && __harness.state().view !== "loading"); i++) await new Promise((r) => setTimeout(r, 250));
const st = __harness.state();
if (!st.connected) return { ok: false, error: "harness not connected", state: st };
if (typeof __harness.setWebEngine === "function") {
  __harness.setWebEngine("webkitgtk", "WEBKIT_VERSION");
}
const opened = await __harness.openProject("Harness");
if (opened?.ok !== true) return { ok: false, error: "openProject failed", detail: opened };
const session = await __harness.newSession();
if (session?.ok !== true) return { ok: false, error: "newSession failed", detail: session };
await new Promise((r) => setTimeout(r, 500));
const composer = document.querySelector("textarea, [contenteditable='true'], input[type='text']");
if (composer && typeof composer.focus === "function") composer.focus();
await new Promise((r) => setTimeout(r, 150));
return {
  ok: true,
  state: __harness.state(),
  sessionId: session.sessionId || __harness.state().sessionId || "",
};
"""

FOCUS_JS = r"""
const el = document.activeElement;
const tagName = el ? (el.tagName || "") : "";
const testId = el && el.getAttribute ? (el.getAttribute("data-testid") || "") : "";
const role = el && el.getAttribute ? (el.getAttribute("role") || "") : "";
const ariaLabel = el && el.getAttribute ? (el.getAttribute("aria-label") || "") : "";
return {
  tagName,
  testId,
  role,
  ariaLabel,
  key: `${tagName}|${testId}|${role}|${ariaLabel}`,
};
"""

STATE_JS = r"""
return window.__harness ? __harness.state() : null;
"""


def parse_png_info(path: pathlib.Path) -> dict[str, Any]:
    """Verify PNG signature and extract (width, height), byte size, and SHA-256."""
    if not path.is_file():
        return {"exists": False, "bytes": 0, "valid_png": False, "width": 0, "height": 0, "sha256": ""}
    raw = path.read_bytes()
    sha = hashlib.sha256(raw).hexdigest()
    valid = len(raw) >= 24 and raw[:8] == b"\x89PNG\r\n\x1a\n" and raw[12:16] == b"IHDR"
    width, height = (struct.unpack(">II", raw[16:24]) if valid else (0, 0))
    return {
        "exists": True,
        "bytes": len(raw),
        "valid_png": bool(valid and width > 0 and height > 0),
        "width": int(width),
        "height": int(height),
        "sha256": sha,
    }


def _api_readback(api_url: str, token: str | None, path: str) -> Any:
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(api_url.rstrip("/") + path, headers=headers)
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _find_child_engine_pid(parent_pid: int) -> int:
    """Find a spawned `lycaon-dev` child process whose PPID is `parent_pid`."""
    proc = subprocess.run(
        ["ps", "-eo", "pid=,ppid=,args="],
        capture_output=True,
        text=True,
        check=False,
        timeout=5,
    )
    if proc.returncode != 0:
        return 0
    for line in proc.stdout.splitlines():
        parts = line.strip().split(None, 2)
        if len(parts) < 3 or not parts[0].isdigit() or not parts[1].isdigit():
            continue
        pid, ppid, args = int(parts[0]), int(parts[1]), parts[2]
        if ppid == parent_pid and "lycaon" in args:
            return pid
    return 0


def _discover_listening_url(profile: pathlib.Path, tauri_log: pathlib.Path) -> str:
    """Scan isolated profile `engine.log` files and `tauri-app.log` for `server listening addr=127.0.0.1:<port>`."""
    candidates = [
        profile / "config" / "engine.log",
        tauri_log,
    ]
    for extra in sorted(profile.rglob("engine.log")):
        if extra not in candidates:
            candidates.append(extra)
    for path in candidates:
        if not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        matches = _LISTENING_ADDR_RE.findall(text)
        if matches:
            return f"http://{matches[-1]}"
    return ""


def _wait_for_spawned_engine(
    app_pid: int,
    profile: pathlib.Path,
    tauri_log: pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    timeout_s: float = 35.0,
    proc: subprocess.Popen[Any] | None = None,
) -> dict[str, Any]:
    """Wait up to `timeout_s` for the spawned `lycaon-dev` engine to serve `/health`."""
    deadline = time.monotonic() + timeout_s
    engine_pid = 0
    discovered_url = api_url or ""
    while time.monotonic() < deadline:
        if proc is not None and proc.poll() is not None:
            raise RuntimeError(f"tauri_app_exited_during_engine_boot (rc={proc.returncode})")
        if not engine_pid:
            engine_pid = _find_child_engine_pid(app_pid)
        if not discovered_url:
            discovered_url = _discover_listening_url(profile, tauri_log)
        if discovered_url:
            try:
                health = _api_readback(discovered_url, token, "/health")
                return {
                    "ready": True,
                    "engine_url": discovered_url,
                    "engine_pid": engine_pid,
                    "health": health,
                }
            except Exception:  # noqa: BLE001
                pass
        time.sleep(0.5)
    return {
        "ready": False,
        "engine_url": discovered_url,
        "engine_pid": engine_pid,
        "health": None,
    }


def _probe_atspi(target_pid: int) -> dict[str, Any]:
    """Observe the owned PID's AT-SPI accessibility application and focused object if bus is available."""
    try:
        import gi

        gi.require_version("Atspi", "2.0")
        from gi.repository import Atspi

        desktop = Atspi.get_desktop(0)
        app_node = None
        for idx in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(idx)
            if child is not None and child.get_process_id() == target_pid:
                app_node = child
                break
        if app_node is None:
            return {"available": True, "observed": False, "reason": "pid_not_in_atspi_desktop"}

        focused_role = ""
        focused_name = ""
        stack = [(app_node, 0)]
        visited = 0
        while stack and visited < 512:
            node, depth = stack.pop()
            visited += 1
            try:
                ss = node.get_state_set()
                if ss is not None and ss.contains(Atspi.StateType.FOCUSED):
                    focused_role = node.get_role_name() or ""
                    focused_name = node.get_name() or ""
                    break
                if depth < 24:
                    cnt = min(node.get_child_count(), 64)
                    for i in range(cnt):
                        c = node.get_child_at_index(i)
                        if c is not None:
                            stack.append((c, depth + 1))
            except Exception:  # noqa: BLE001
                continue

        return {
            "available": True,
            "observed": True,
            "app_name": app_node.get_name() or "",
            "child_count": int(app_node.get_child_count()),
            "focused_role": focused_role,
            "focused_name": focused_name,
            "focused_key": f"{focused_role}|{focused_name}",
        }
    except Exception as exc:  # noqa: BLE001
        return {"available": False, "observed": False, "reason": str(exc)}


def _get_x11_window_size(wid: int) -> tuple[int, int]:
    """Return `(width, height)` for `wid` via `xwininfo`, defaulting to `(1280, 800)`."""
    if not shutil.which("xwininfo"):
        return (1280, 800)
    res = subprocess.run(
        ["xwininfo", "-id", hex(int(wid))],
        capture_output=True,
        text=True,
        check=False,
        timeout=5,
    )
    if res.returncode != 0:
        return (1280, 800)
    width = 0
    height = 0
    for line in res.stdout.splitlines():
        s = line.strip()
        if s.startswith("Width:"):
            tok = s.split(":", 1)[1].strip()
            if tok.isdigit():
                width = int(tok)
        elif s.startswith("Height:"):
            tok = s.split(":", 1)[1].strip()
            if tok.isdigit():
                height = int(tok)
    return (width if width >= 100 else 1280, height if height >= 100 else 800)


def _click_x11_window_point(xid: int, x: int, y: int) -> dict[str, Any]:
    """Send a window-relative pointer click to the owned X11 window via `xdotool`."""
    if not shutil.which("xdotool"):
        return {"sent": False, "reason": "xdotool_not_found", "x": x, "y": y}
    proc = subprocess.run(
        [
            "xdotool",
            "windowfocus",
            "--sync",
            str(xid),
            "mousemove",
            "--window",
            str(xid),
            str(int(x)),
            str(int(y)),
            "click",
            "1",
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    return {
        "sent": proc.returncode == 0,
        "tool": "xdotool",
        "action": "click",
        "x": int(x),
        "y": int(y),
        "xid": xid,
        "returncode": proc.returncode,
        "stderr": (proc.stderr or "").strip(),
    }


def _collect_atspi_nodes(target_pid: int, *, max_nodes: int = 300) -> list[dict[str, Any]]:
    """Collect visible named AT-SPI nodes (role, name, center) under `target_pid` for UI surface verification."""
    try:
        import gi

        gi.require_version("Atspi", "2.0")
        from gi.repository import Atspi

        desktop = Atspi.get_desktop(0)
        app_node = None
        for idx in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(idx)
            if child is not None and child.get_process_id() == target_pid:
                app_node = child
                break
        if app_node is None:
            return []

        nodes: list[dict[str, Any]] = []
        stack = [(app_node, 0)]
        visited = 0
        while stack and visited < 1500 and len(nodes) < max_nodes:
            node, depth = stack.pop()
            visited += 1
            try:
                name = (node.get_name() or "").strip()
                role = (node.get_role_name() or "").strip()
                if name:
                    cx, cy = 0, 0
                    try:
                        comp = node.get_component_iface()
                        if comp is not None:
                            ext = comp.get_extents(Atspi.CoordType.WINDOW)
                            if ext.width > 0 and ext.height > 0:
                                cx = int(ext.x + ext.width / 2)
                                cy = int(ext.y + ext.height / 2)
                    except Exception:  # noqa: BLE001
                        pass
                    nodes.append({
                        "role": role,
                        "name": name[:240],
                        "center": [cx, cy] if cx > 0 and cy > 0 else None,
                    })
                if depth < 28:
                    cnt = min(node.get_child_count(), 64)
                    for i in range(cnt - 1, -1, -1):
                        c = node.get_child_at_index(i)
                        if c is not None:
                            stack.append((c, depth + 1))
            except Exception:  # noqa: BLE001
                continue
        return nodes
    except Exception:  # noqa: BLE001
        return []


def _activate_atspi_match(target_pid: int, hints: list[str], *, invoke: bool = True) -> dict[str, Any]:
    """Find the first AT-SPI node whose name contains any hint (case-insensitive) and activate/locate it."""
    try:
        import gi

        gi.require_version("Atspi", "2.0")
        from gi.repository import Atspi

        desktop = Atspi.get_desktop(0)
        app_node = None
        for idx in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(idx)
            if child is not None and child.get_process_id() == target_pid:
                app_node = child
                break
        if app_node is None:
            return {"found": False, "invoked": False, "reason": "pid_not_in_atspi_desktop"}

        low_hints = [h.strip().lower() for h in hints if h and h.strip()]
        stack = [(app_node, 0)]
        visited = 0
        while stack and visited < 1500:
            node, depth = stack.pop()
            visited += 1
            try:
                name = (node.get_name() or "").strip()
                role = (node.get_role_name() or "").strip()
                low_name = name.lower()
                if low_name and any(h in low_name for h in low_hints):
                    cx, cy = 0, 0
                    try:
                        comp = node.get_component_iface()
                        if comp is not None:
                            ext = comp.get_extents(Atspi.CoordType.WINDOW)
                            if ext.width > 0 and ext.height > 0:
                                cx = int(ext.x + ext.width / 2)
                                cy = int(ext.y + ext.height / 2)
                    except Exception:  # noqa: BLE001
                        pass
                    invoked = False
                    if invoke:
                        try:
                            act = node.get_action_iface()
                            if act is not None and act.get_n_actions() > 0:
                                invoked = bool(act.do_action(0))
                        except Exception:  # noqa: BLE001
                            invoked = False
                    return {
                        "found": True,
                        "invoked": invoked,
                        "method": "atspi_action" if invoked else "atspi_locate",
                        "name": name[:240],
                        "role": role,
                        "center": [cx, cy] if cx > 0 and cy > 0 else None,
                    }
                if depth < 28:
                    cnt = min(node.get_child_count(), 64)
                    for i in range(cnt - 1, -1, -1):
                        c = node.get_child_at_index(i)
                        if c is not None:
                            stack.append((c, depth + 1))
            except Exception:  # noqa: BLE001
                continue
        return {"found": False, "invoked": False, "reason": f"match_not_found:{low_hints}"}
    except Exception as exc:  # noqa: BLE001
        return {"found": False, "invoked": False, "reason": str(exc)}


def _activate_atspi_button(target_pid: int, label: str, *, invoke: bool = True) -> dict[str, Any]:
    """Locate an AT-SPI node named `label` under `target_pid` and optionally invoke its default Action."""
    try:
        import gi

        gi.require_version("Atspi", "2.0")
        from gi.repository import Atspi

        desktop = Atspi.get_desktop(0)
        app_node = None
        for idx in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(idx)
            if child is not None and child.get_process_id() == target_pid:
                app_node = child
                break
        if app_node is None:
            return {"found": False, "invoked": False, "reason": "pid_not_in_atspi_desktop"}

        want = label.strip().lower()
        stack = [(app_node, 0)]
        visited = 0
        while stack and visited < 1024:
            node, depth = stack.pop()
            visited += 1
            try:
                name = (node.get_name() or "").strip()
                role = (node.get_role_name() or "").strip()
                if name.lower() == want:
                    cx, cy = 0, 0
                    try:
                        comp = node.get_component_iface()
                        if comp is not None:
                            ext = comp.get_extents(Atspi.CoordType.WINDOW)
                            if ext.width > 0 and ext.height > 0:
                                cx = int(ext.x + ext.width / 2)
                                cy = int(ext.y + ext.height / 2)
                    except Exception:  # noqa: BLE001
                        pass
                    invoked = False
                    if invoke:
                        try:
                            act = node.get_action_iface()
                            if act is not None and act.get_n_actions() > 0:
                                invoked = bool(act.do_action(0))
                        except Exception:  # noqa: BLE001
                            invoked = False
                    return {
                        "found": True,
                        "invoked": invoked,
                        "method": "atspi_action" if invoked else "atspi_locate",
                        "name": name,
                        "role": role,
                        "center": [cx, cy] if cx > 0 and cy > 0 else None,
                    }
                if depth < 28:
                    cnt = min(node.get_child_count(), 64)
                    for i in range(cnt):
                        c = node.get_child_at_index(i)
                        if c is not None:
                            stack.append((c, depth + 1))
            except Exception:  # noqa: BLE001
                continue
        return {"found": False, "invoked": False, "reason": f"label_not_found:{label}"}
    except Exception as exc:  # noqa: BLE001
        return {"found": False, "invoked": False, "reason": str(exc)}


def _is_viewable_x11_window(wid: int) -> bool:
    """Return True iff `xwininfo` confirms `wid` is mapped (`IsViewable`) with non-trivial geometry."""
    if not shutil.which("xwininfo"):
        return True
    res = subprocess.run(
        ["xwininfo", "-id", hex(int(wid))],
        capture_output=True,
        text=True,
        check=False,
        timeout=5,
    )
    if res.returncode != 0:
        return False
    viewable = False
    width = 0
    height = 0
    for line in res.stdout.splitlines():
        s = line.strip()
        if s == "Map State: IsViewable":
            viewable = True
        elif s.startswith("Width:"):
            tok = s.split(":", 1)[1].strip()
            if tok.isdigit():
                width = int(tok)
        elif s.startswith("Height:"):
            tok = s.split(":", 1)[1].strip()
            if tok.isdigit():
                height = int(tok)
    return bool(viewable and width >= 100 and height >= 100)


def _find_x11_window_for_pid(pid: int, *, timeout_s: float = 45.0, proc: subprocess.Popen[Any] | None = None) -> int:
    """Poll `xdotool search --onlyvisible --pid` until a viewable top-level X11 window owned by `pid` appears.

    Skips hidden/unmapped GTK leader windows (e.g. 1x1 `IsUnMapped` windows) so `xdotool windowfocus`
    and `import -window <wid>` always bind to the actual mapped Tauri/WebKitGTK top-level window.
    """
    if not shutil.which("xdotool"):
        raise RuntimeError("xdotool_not_found")
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        if proc is not None and proc.poll() is not None:
            raise RuntimeError(f"tauri_app_exited_early (rc={proc.returncode})")
        res = subprocess.run(
            ["xdotool", "search", "--onlyvisible", "--pid", str(pid)],
            capture_output=True,
            text=True,
            check=False,
            timeout=5,
        )
        if res.returncode == 0:
            wids = [int(tok) for tok in res.stdout.split() if tok.isdigit() and int(tok) > 0]
            for wid in reversed(wids):
                if _is_viewable_x11_window(wid):
                    return wid
        time.sleep(0.4)
    return 0


def _capture_x11_window_png(xid: int, dest: pathlib.Path) -> dict[str, Any]:
    """Capture ONLY the owned X11 window (`import -window <xid>` with `xwd`+`convert` fallback)."""
    dest.parent.mkdir(parents=True, exist_ok=True)
    xid_hex = hex(int(xid))
    if shutil.which("import"):
        proc = subprocess.run(
            ["import", "-window", xid_hex, str(dest)],
            capture_output=True,
            text=True,
            check=False,
            timeout=20,
        )
        info = parse_png_info(dest)
        if proc.returncode == 0 and info["valid_png"]:
            return {"tool": "import", **info}

    if shutil.which("xwd") and shutil.which("convert"):
        xwd_path = dest.with_suffix(".xwd")
        p1 = subprocess.run(
            ["xwd", "-id", xid_hex, "-out", str(xwd_path)],
            capture_output=True,
            text=True,
            check=False,
            timeout=20,
        )
        if p1.returncode == 0 and xwd_path.is_file():
            p2 = subprocess.run(
                ["convert", str(xwd_path), str(dest)],
                capture_output=True,
                text=True,
                check=False,
                timeout=20,
            )
            xwd_path.unlink(missing_ok=True)
            info = parse_png_info(dest)
            if p2.returncode == 0 and info["valid_png"]:
                return {"tool": "xwd+convert", **info}

    return {"tool": "none", **parse_png_info(dest)}


def _send_x11_key(xid: int, key_name: str = "Tab") -> dict[str, Any]:
    """Send a real native X11 key event directly to the owned window via `xdotool`."""
    if not shutil.which("xdotool"):
        return {"sent": False, "error": "xdotool_not_found"}
    xid_str = str(int(xid))
    p_focus = subprocess.run(
        ["xdotool", "windowfocus", "--sync", xid_str],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    p_key = subprocess.run(
        ["xdotool", "key", "--window", xid_str, key_name],
        capture_output=True,
        text=True,
        check=False,
        timeout=10,
    )
    return {
        "sent": p_key.returncode == 0,
        "key": key_name,
        "window_id": int(xid),
        "focus_rc": p_focus.returncode,
        "key_rc": p_key.returncode,
        "stderr": (p_focus.stderr + " " + p_key.stderr).strip(),
    }


def run_page(url: str) -> int:
    """Child process: opens a real Gtk.Window + ephemeral WebKit2.WebView and serves stdin commands."""
    import gi

    gi.require_version("Gtk", "3.0")
    gi.require_version("WebKit2", "4.1")
    from gi.repository import GLib, Gtk, WebKit2

    window = Gtk.Window(title=WINDOW_TITLE)
    window.set_default_size(1280, 800)
    view = WebKit2.WebView(is_ephemeral=True)
    view.get_user_content_manager().add_script(
        WebKit2.UserScript.new(
            FIRST_RUN_DONE,
            WebKit2.UserContentInjectedFrames.TOP_FRAME,
            WebKit2.UserScriptInjectionTime.START,
            None,
            None,
        )
    )
    window.add(view)
    window.show_all()

    version = f"{WebKit2.get_major_version()}.{WebKit2.get_minor_version()}.{WebKit2.get_micro_version()}"
    commands = {
        "setup": SETUP_JS.replace("WEBKIT_VERSION", version),
        "focus_state": FOCUS_JS,
        "api_state": STATE_JS,
    }

    def emit(line: str) -> None:
        print(line, flush=True)

    gdk_win = window.get_window()
    xid = int(gdk_win.get_xid()) if gdk_win is not None and hasattr(gdk_win, "get_xid") else 0
    emit("window " + json.dumps({"xid": xid, "pid": os.getpid(), "title": WINDOW_TITLE, "webkit_version": version}))

    def terminated(_view, reason) -> None:
        emit(f"terminated {reason.value_nick}")
        Gtk.main_quit()

    def done(source, result) -> None:
        try:
            value = source.call_async_javascript_function_finish(result)
            emit("ready " + json.dumps(json.loads(value.to_json(0))))
        except GLib.Error as error:
            emit("ready " + json.dumps({"ok": False, "error": error.message}))

    def on_stdin(channel, _condition) -> bool:
        cmd = channel.readline().strip()
        if not cmd or cmd == "quit":
            Gtk.main_quit()
            return False
        body = commands.get(cmd)
        if body is None:
            emit("ready " + json.dumps({"ok": False, "error": f"unknown command: {cmd}"}))
            return True
        view.call_async_javascript_function(body, -1, None, None, None, None, done)
        return True

    def on_load(_view, event) -> None:
        if event == WebKit2.LoadEvent.FINISHED:
            emit("loaded")

    view.connect("web-process-terminated", terminated)
    view.connect("load-changed", on_load)
    GLib.io_add_watch(GLib.IOChannel.unix_new(sys.stdin.fileno()), GLib.IO_IN | GLib.IO_HUP, on_stdin)
    view.load_uri(url)
    Gtk.main()
    return 0


def run_linux_native_den(
    url: str,
    out_dir: str | pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
) -> dict[str, Any]:
    """Drive the owned Linux WebKitGTK window, dispatch native X11 Tab, check AT-SPI, and capture PNG."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )

    page = subprocess.Popen(
        [sys.executable, str(pathlib.Path(__file__).resolve()), "--page", url],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    lines: queue.Queue[str | None] = queue.Queue()

    def drain() -> None:
        assert page.stdout is not None
        for raw in page.stdout:
            lines.put(raw.strip())
        lines.put(None)

    threading.Thread(target=drain, daemon=True).start()

    def await_prefix(prefixes: tuple[str, ...], timeout_s: float) -> str:
        deadline = time.monotonic() + timeout_s
        while (left := deadline - time.monotonic()) > 0:
            try:
                line = lines.get(timeout=left)
            except queue.Empty:
                break
            if line is None:
                break
            if line.startswith(prefixes):
                return line
        return ""

    def run_js_cmd(name: str, timeout_s: float = 120.0) -> Any:
        assert page.stdin is not None
        page.stdin.write(name + "\n")
        page.stdin.flush()
        line = await_prefix(("ready ", "terminated "), timeout_s)
        if line.startswith("ready "):
            return json.loads(line[len("ready "):])
        raise RuntimeError(f"webkitgtk page failed during {name}: {line or 'timeout'}")

    try:
        win_line = await_prefix(("window ", "terminated "), 30.0)
        if not win_line.startswith("window "):
            raise RuntimeError(f"failed to open owned WebKitGTK window: {win_line or 'timeout'}")
        win_meta = json.loads(win_line[len("window "):])
        xid = int(win_meta.get("xid") or 0)
        pid = int(win_meta.get("pid") or page.pid)
        if xid <= 0:
            raise RuntimeError(f"owned WebKitGTK window reported invalid X11 xid: {win_meta}")

        loaded_line = await_prefix(("loaded", "terminated "), 90.0)
        if not loaded_line.startswith("loaded"):
            raise RuntimeError(f"Den page did not finish loading: {loaded_line or 'timeout'}")

        setup = run_js_cmd("setup", timeout_s=120.0)
        if not isinstance(setup, dict) or setup.get("ok") is not True:
            raise RuntimeError(f"harness setup failed: {setup}")

        focus_before = run_js_cmd("focus_state", timeout_s=15.0)
        key_ev = _send_x11_key(xid, "Tab")
        time.sleep(0.3)
        focus_after = run_js_cmd("focus_state", timeout_s=15.0)
        if isinstance(focus_before, dict) and isinstance(focus_after, dict) and focus_after.get("key") == focus_before.get("key"):
            key_ev = _send_x11_key(xid, "Tab")
            time.sleep(0.3)
            focus_after = run_js_cmd("focus_state", timeout_s=15.0)

        before_key = str((focus_before or {}).get("key") or "") if isinstance(focus_before, dict) else ""
        after_key = str((focus_after or {}).get("key") or "") if isinstance(focus_after, dict) else ""
        focus_changed = bool(before_key and after_key and after_key != before_key)

        atspi = _probe_atspi(pid)
        screenshot_path = out / "linux-native-den-webkitgtk.png"
        shot = _capture_x11_window_png(xid, screenshot_path)

        webview_state = run_js_cmd("api_state", timeout_s=15.0)
        session_id = str(setup.get("sessionId") or "")
        api_readback: dict[str, Any] = {"webview_state": webview_state}
        if api_url and token:
            api_readback["health"] = _api_readback(api_url, token, "/health")
            if session_id:
                api_readback["session"] = _api_readback(api_url, token, f"/v1/sessions/{session_id}")
            else:
                api_readback["projects"] = _api_readback(api_url, token, "/v1/projects")

        passed = bool(
            focus_changed
            and key_ev.get("sent") is True
            and shot.get("valid_png") is True
            and int(shot.get("bytes") or 0) > 0
        )
        receipt: dict[str, Any] = {
            "schema": "wea-native-den-linux-webkitgtk-v1",
            "tier": "native-den-webkitgtk",
            "owned_window_id": xid,
            "pid": pid,
            "window_title": win_meta.get("title", WINDOW_TITLE),
            "webkit_version": win_meta.get("webkit_version", ""),
            "session_id": session_id,
            "focus_before": focus_before,
            "focus_after": focus_after,
            "focus_changed": focus_changed,
            "keyboard_event": key_ev,
            "atspi_observed": atspi,
            "api_readback": api_readback,
            "screenshot_path": str(screenshot_path),
            "screenshot_sha256": shot.get("sha256", ""),
            "screenshot_bytes": int(shot.get("bytes") or 0),
            "screenshot_dimensions": [shot.get("width", 0), shot.get("height", 0)],
            "screenshot_tool": shot.get("tool", ""),
            "provenance": prov,
            "passed": passed,
        }
    except Exception as exc:  # noqa: BLE001
        receipt = {
            "schema": "wea-native-den-linux-webkitgtk-v1",
            "tier": "native-den-webkitgtk",
            "status": "blocked",
            "blocked_reason": str(exc),
            "provenance": prov,
            "passed": False,
        }
    finally:
        if page.poll() is None:
            try:
                assert page.stdin is not None
                page.stdin.write("quit\n")
                page.stdin.flush()
                page.wait(timeout=5)
            except Exception:  # noqa: BLE001
                page.terminate()
                try:
                    page.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    page.kill()
                    page.wait()

    (out / "native-den-linux-webkitgtk-receipt.json").write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return receipt


def run_linux_tauri_native(
    app_bin: str | pathlib.Path,
    out_dir: str | pathlib.Path,
    *,
    api_url: str | None = None,
    token: str | None = None,
    profile_dir: str | pathlib.Path | None = None,
    engine_bin: str | pathlib.Path | None = None,
    engine_manifest: str | pathlib.Path | None = None,
    startup_timeout_s: float = 45.0,
    dismiss_onboarding: bool = False,
    seed_store_db: str | pathlib.Path | None = None,
    open_project_hint: str | None = None,
    open_session_hint: str | None = None,
    session_id: str | None = None,
) -> dict[str, Any]:
    """Launch the actual Linux Tauri desktop binary under Xvfb+AT-SPI, drive xdotool Tab, and capture its owned window."""
    out = pathlib.Path(out_dir).resolve()
    out.mkdir(parents=True, exist_ok=True)
    app_path = pathlib.Path(app_bin).resolve()
    profile = pathlib.Path(profile_dir).resolve() if profile_dir else (out / "tauri-profile")
    profile.mkdir(parents=True, exist_ok=True)

    prov = collect_provenance(
        REPO_ROOT,
        engine_bin=engine_bin,
        engine_manifest=engine_manifest,
        lane="sidecar-native-den",
    )
    if not app_path.is_file():
        receipt: dict[str, Any] = {
            "schema": "wea-native-den-linux-tauri-v1",
            "tier": "native-den-tauri-app",
            "status": "blocked",
            "blocked_reason": f"app_bin_not_found:{app_path}",
            "app_bin": str(app_path),
            "provenance": prov,
            "passed": False,
        }
        (out / "native-den-linux-tauri-receipt.json").write_text(
            json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        return receipt

    app_sha = hashlib.sha256(app_path.read_bytes()).hexdigest()
    seeded_store_db = False
    seeded_store_db_sha256 = ""
    cfg_dir = profile / "config"
    cfg_dir.mkdir(parents=True, exist_ok=True)
    try:
        cfg_dir.chmod(0o700)
    except OSError:
        pass
    if seed_store_db:
        src_db = pathlib.Path(seed_store_db).resolve()
        if src_db.is_file():
            dst_db = cfg_dir / "store.db"
            shutil.copy2(src_db, dst_db)
            for suffix in ("-wal", "-shm"):
                side = src_db.parent / (src_db.name + suffix)
                if side.is_file():
                    shutil.copy2(side, cfg_dir / ("store.db" + suffix))
            seeded_store_db = True
            seeded_store_db_sha256 = hashlib.sha256(dst_db.read_bytes()).hexdigest()

    env = dict(os.environ)
    env["GTK_MODULES"] = "gail:atk-bridge"
    env["NO_AT_BRIDGE"] = "0"
    env["WEBKIT_DISABLE_DMABUF_RENDERER"] = "1"
    env["WEBKIT_DISABLE_COMPOSITING_MODE"] = "1"
    env["LYCAON_CONFIG_DIR"] = str(cfg_dir)
    env["XDG_CONFIG_HOME"] = str(profile / "xdg-config")
    env["XDG_DATA_HOME"] = str(profile / "xdg-data")
    eff_token = token
    if api_url:
        env["LYCAON_E2E_API_URL"] = api_url
    if token:
        env["LYCAON_E2E_TOKEN"] = token
        env["LYCAON_API_TOKEN"] = token

    log_path = out / "tauri-app.log"
    log_fh = log_path.open("w", encoding="utf-8")
    proc = subprocess.Popen(
        [str(app_path)],
        cwd=str(REPO_ROOT),
        env=env,
        stdout=log_fh,
        stderr=subprocess.STDOUT,
    )
    try:
        xid = _find_x11_window_for_pid(proc.pid, timeout_s=startup_timeout_s, proc=proc)
        if xid <= 0:
            raise RuntimeError(f"no_x11_window_found_for_tauri_pid_{proc.pid}")

        # Wait up to 35s for the spawned `lycaon-dev` engine to finish booting and serve HTTP.
        engine_boot = _wait_for_spawned_engine(
            proc.pid,
            profile,
            log_path,
            api_url=api_url,
            token=token,
            timeout_s=35.0,
            proc=proc,
        )
        # Allow main workspace UI to finish rendering past the splash screen.
        time.sleep(2.0)

        # Capture pre-input owned-window screenshot BEFORE sending keyboard events.
        screenshot_before_path = out / "linux-native-den-tauri-before.png"
        shot_before = _capture_x11_window_png(xid, screenshot_before_path)

        atspi_before = _probe_atspi(proc.pid)
        keyboard_events: list[dict[str, Any]] = []
        key_ev: dict[str, Any] = {"sent": False}
        atspi_after = atspi_before
        for key_name in ("Tab", "Tab", "Down", "Right"):
            key_ev = _send_x11_key(xid, key_name)
            keyboard_events.append(key_ev)
            time.sleep(0.4)
            atspi_after = _probe_atspi(proc.pid)
            if (
                atspi_before.get("focused_key")
                and atspi_after.get("focused_key")
                and atspi_after.get("focused_key") != atspi_before.get("focused_key")
            ):
                break

        # Activate the focused control via Return to produce an observable UI state transition.
        activate_ev = _send_x11_key(xid, "Return")
        keyboard_events.append(activate_ev)
        time.sleep(0.8)
        atspi_after_step1 = _probe_atspi(proc.pid)
        if atspi_after_step1.get("observed"):
            atspi_after = atspi_after_step1

        # When requested (e.g. TB10), advance past onboarding Step 2/2 ("Get started")
        # via AT-SPI action or window-relative xdotool click so the post-input screenshot
        # visibly renders the live Den workspace UI.
        onboarding_dismiss_events: list[dict[str, Any]] = []
        onboarding_step2_sha256 = ""
        onboarding_dismiss_confirmed = False
        if dismiss_onboarding:
            step2_path = out / "linux-native-den-tauri-step2.png"
            shot_step2 = _capture_x11_window_png(xid, step2_path)
            onboarding_step2_sha256 = str(shot_step2.get("sha256") or "")

            atspi_btn = _activate_atspi_button(proc.pid, "Get started", invoke=True)
            onboarding_dismiss_events.append({"step": "atspi_get_started", **atspi_btn})
            clicked_fallback = False
            if atspi_btn.get("invoked"):
                time.sleep(2.0)
                check_after_atspi = _capture_x11_window_png(xid, out / "linux-native-den-tauri.png")
                if (
                    onboarding_step2_sha256
                    and check_after_atspi.get("sha256")
                    and check_after_atspi.get("sha256") != onboarding_step2_sha256
                ):
                    onboarding_dismiss_confirmed = True
            if not onboarding_dismiss_confirmed:
                center = atspi_btn.get("center")
                if isinstance(center, list) and len(center) == 2 and center[0] > 0 and center[1] > 0:
                    cx, cy = int(center[0]), int(center[1])
                    method = "xdotool_atspi_center"
                else:
                    win_w, win_h = _get_x11_window_size(xid)
                    cx, cy = int(win_w * 0.65), int(win_h * 0.83)
                    method = "xdotool_window_fallback"
                click_ev = _click_x11_window_point(xid, cx, cy)
                onboarding_dismiss_events.append({"step": method, **click_ev})
                clicked_fallback = True
                time.sleep(2.0)
            atspi_after_dismiss = _probe_atspi(proc.pid)
            if atspi_after_dismiss.get("observed"):
                atspi_after = atspi_after_dismiss
            if clicked_fallback:
                check_after_click = _capture_x11_window_png(xid, out / "linux-native-den-tauri.png")
                onboarding_dismiss_confirmed = bool(
                    onboarding_step2_sha256
                    and check_after_click.get("sha256")
                    and check_after_click.get("sha256") != onboarding_step2_sha256
                )

        # Optionally navigate from Home -> All projects -> seeded project -> seeded chat session.
        navigation_events: list[dict[str, Any]] = []
        atspi_home_nodes: list[dict[str, Any]] = []
        atspi_chat_nodes: list[dict[str, Any]] = []
        chat_opened = False
        if open_project_hint or open_session_hint:
            atspi_home_nodes = _collect_atspi_nodes(proc.pid)
            all_proj = _activate_atspi_button(proc.pid, "All projects", invoke=True)
            if all_proj.get("found"):
                navigation_events.append({"step": "all_projects", **all_proj})
                if not all_proj.get("invoked") and isinstance(all_proj.get("center"), list):
                    cx, cy = all_proj["center"]
                    navigation_events.append({"step": "all_projects_click", **_click_x11_window_point(xid, cx, cy)})
                time.sleep(1.5)
            if open_project_hint:
                proj_hints = [f"/{open_session_hint}/project"] if open_session_hint else [f"/{open_project_hint}"]
                proj_match = _activate_atspi_match(proc.pid, proj_hints, invoke=True)
                navigation_events.append({"step": "open_project", **proj_match})
                if not proj_match.get("invoked") and isinstance(proj_match.get("center"), list):
                    cx, cy = proj_match["center"]
                    navigation_events.append({"step": "open_project_click", **_click_x11_window_point(xid, cx, cy)})
                time.sleep(2.0)
                _capture_x11_window_png(xid, out / "linux-native-den-tauri-project.png")
            if open_session_hint:
                sess_hints = [f"TB10 {open_session_hint}"]
                sess_match = _activate_atspi_match(proc.pid, sess_hints, invoke=True)
                navigation_events.append({"step": "open_session", **sess_match})
                if not sess_match.get("invoked") and isinstance(sess_match.get("center"), list):
                    cx, cy = sess_match["center"]
                    navigation_events.append({"step": "open_session_click", **_click_x11_window_point(xid, cx, cy)})
                time.sleep(2.5)
                chat_opened = bool(sess_match.get("found"))
                _capture_x11_window_png(xid, out / "linux-native-den-tauri-chat.png")
            atspi_chat_nodes = _collect_atspi_nodes(proc.pid, max_nodes=400)
            atspi_after_nav = _probe_atspi(proc.pid)
            if atspi_after_nav.get("observed"):
                atspi_after = atspi_after_nav

        atspi_focus_changed = bool(
            atspi_before.get("focused_key")
            and atspi_after.get("focused_key")
            and atspi_after.get("focused_key") != atspi_before.get("focused_key")
        )
        screenshot_path = out / "linux-native-den-tauri.png"
        shot = _capture_x11_window_png(xid, screenshot_path)
        screenshot_changed = bool(
            shot_before.get("valid_png")
            and shot.get("valid_png")
            and shot_before.get("sha256")
            and shot.get("sha256")
            and shot_before.get("sha256") != shot.get("sha256")
        )
        app_alive_after = proc.poll() is None

        engine_pid = int(engine_boot.get("engine_pid") or 0)
        tracked_pids = [proc.pid] + ([engine_pid] if engine_pid > 0 else [])
        host_processes = read_host_process_table(tracked_pids)

        api_readback: dict[str, Any] = {}
        eff_url = str(engine_boot.get("engine_url") or api_url or "")
        if eff_url:
            api_readback["engine_url"] = eff_url
            if engine_boot.get("health") is not None:
                api_readback["health"] = engine_boot["health"]
            else:
                try:
                    api_readback["health"] = _api_readback(eff_url, token, "/health")
                except Exception as exc:  # noqa: BLE001
                    api_readback["health_error"] = str(exc)
            if eff_token:
                try:
                    api_readback["projects"] = _api_readback(eff_url, eff_token, "/v1/projects")
                except Exception as exc:  # noqa: BLE001
                    api_readback["projects_error"] = str(exc)
                if session_id:
                    try:
                        api_readback["session"] = _api_readback(eff_url, eff_token, f"/v1/sessions/{session_id}")
                    except Exception as exc:  # noqa: BLE001
                        api_readback["session_error"] = str(exc)
                    try:
                        api_readback["messages"] = _api_readback(eff_url, eff_token, f"/v1/sessions/{session_id}/messages")
                    except Exception as exc:  # noqa: BLE001
                        api_readback["messages_error"] = str(exc)

        atspi_ok = bool(atspi_after.get("observed")) if atspi_after.get("available") else True
        passed = bool(
            xid > 0
            and any(ev.get("sent") is True for ev in keyboard_events)
            and shot.get("valid_png") is True
            and int(shot.get("bytes") or 0) > 0
            and app_alive_after
            and atspi_ok
            and bool(api_readback.get("health"))
        )
        receipt = {
            "schema": "wea-native-den-linux-tauri-v1",
            "tier": "native-den-tauri-app",
            "app_bin": str(app_path),
            "app_bin_sha256": app_sha,
            "app_pid": proc.pid,
            "engine_pid": engine_pid,
            "engine_boot_ready": bool(engine_boot.get("ready")),
            "host_processes": host_processes,
            "owned_window_id": xid,
            "isolated_profile_dir": str(profile),
            "seeded_store_db": seeded_store_db,
            "seeded_store_db_sha256": seeded_store_db_sha256,
            "onboarding_dismiss_attempted": bool(dismiss_onboarding),
            "onboarding_dismiss_confirmed": onboarding_dismiss_confirmed,
            "onboarding_step2_sha256": onboarding_step2_sha256,
            "onboarding_dismiss_events": onboarding_dismiss_events,
            "navigation_events": navigation_events,
            "chat_opened": chat_opened,
            "atspi_home_nodes": atspi_home_nodes[:80],
            "atspi_chat_nodes": atspi_chat_nodes[:120],
            "app_alive_after": app_alive_after,
            "keyboard_event": key_ev,
            "keyboard_events": keyboard_events,
            "atspi_before": atspi_before,
            "atspi_after_step1": atspi_after_step1,
            "atspi_after": atspi_after,
            "atspi_observed": atspi_after,
            "atspi_focus_changed": atspi_focus_changed,
            "screenshot_before_path": str(screenshot_before_path),
            "screenshot_before_sha256": shot_before.get("sha256", ""),
            "screenshot_before_bytes": int(shot_before.get("bytes") or 0),
            "screenshot_path": str(screenshot_path),
            "screenshot_sha256": shot.get("sha256", ""),
            "screenshot_bytes": int(shot.get("bytes") or 0),
            "screenshot_changed": screenshot_changed,
            "screenshot_dimensions": [shot.get("width", 0), shot.get("height", 0)],
            "screenshot_tool": shot.get("tool", ""),
            "api_readback": api_readback,
            "provenance": prov,
            "passed": passed,
        }
    except Exception as exc:  # noqa: BLE001
        receipt = {
            "schema": "wea-native-den-linux-tauri-v1",
            "tier": "native-den-tauri-app",
            "status": "blocked",
            "blocked_reason": str(exc),
            "app_bin": str(app_path),
            "app_bin_sha256": app_sha,
            "app_pid": proc.pid,
            "isolated_profile_dir": str(profile),
            "provenance": prov,
            "passed": False,
        }
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
        log_fh.close()

    (out / "native-den-linux-tauri-receipt.json").write_text(
        json.dumps(receipt, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    return receipt


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TB02 Linux owned-window WebKitGTK native Den probe.")
    parser.add_argument("--page", default=None, help="Internal child mode: open Gtk/WebKit2 view at URL.")
    parser.add_argument("--url", default=None, help="Live harness Den URL (defaults to LYCAON_E2E_BASE_URL).")
    parser.add_argument("--out", type=pathlib.Path, default=None)
    parser.add_argument("--api-url", default=os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token", default=os.environ.get("LYCAON_E2E_TOKEN"))
    parser.add_argument("--engine", type=pathlib.Path, default=None)
    parser.add_argument("--engine-manifest", type=pathlib.Path, default=None)
    args = parser.parse_args(argv)

    if args.page:
        return run_page(args.page)

    url = args.url or os.environ.get("LYCAON_E2E_BASE_URL", "")
    if not url or args.out is None:
        parser.error("--url (or LYCAON_E2E_BASE_URL) and --out are required in controller mode")
    receipt = run_linux_native_den(
        url,
        args.out,
        api_url=args.api_url,
        token=args.token,
        engine_bin=args.engine,
        engine_manifest=args.engine_manifest,
    )
    print(json.dumps(receipt, indent=2, sort_keys=True))
    return 0 if receipt.get("passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())
