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

from wea.provenance import REPO_ROOT, collect_provenance

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


def _api_readback(api_url: str, token: str, path: str) -> Any:
    req = urllib.request.Request(
        api_url.rstrip("/") + path,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _probe_atspi(target_pid: int) -> dict[str, Any]:
    """Observe the owned PID's AT-SPI accessibility application/focused object if bus is available."""
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
        return {
            "available": True,
            "observed": True,
            "app_name": app_node.get_name() or "",
            "child_count": int(app_node.get_child_count()),
        }
    except Exception as exc:  # noqa: BLE001
        return {"available": False, "observed": False, "reason": str(exc)}


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
                api_readback["sessions"] = _api_readback(api_url, token, "/v1/sessions")

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
