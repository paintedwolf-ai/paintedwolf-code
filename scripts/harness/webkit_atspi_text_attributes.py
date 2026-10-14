"""Asks WebKitGTK for AT-SPI text attributes across the whole Den window, as a screen reader does.

WebKitGTK can abort its web process while answering `GetAttributeRun` for an
object whose content it lays out unusually; Orca asks for those attributes when
focus or the braille line reaches the object. This loads the harness Den in a
WebKitGTK view with the screen reader fact set, walks it through a chat with a
transcript and an approval, the Files view with an open file, Settings and
Home, and on each calls `GetAttributeRun` at every caret offset, with and
without defaults, and `GetDefaultAttributes` on every text object in the window.

usage: webkit_atspi_text_attributes.py <harness URL>
exit: 0 every call answered; 1 a call aborted the web process or failed;
      2 the scenario could not be set up (unverified, never a pass)
"""

import json
import queue
import subprocess
import sys
import threading
import time

# A person who finished first-run setup and turned tips off, so nothing covers the window.
FIRST_RUN_DONE = """
localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ firstRunSetupCompleted: true }));
localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
"""

PAUSE = "await new Promise((r) => setTimeout(r, 750));"

# Each surface is an async function body run in the page; it returns {ok, ...} as JSON.
SURFACES = [
    ("chat", r"""
for (let i = 0; i < 240 && !window.__harness; i++) await new Promise((r) => setTimeout(r, 250));
if (!window.__harness) return { ok: false, error: "harness API missing; page reads " + JSON.stringify(document.body.innerText.slice(0, 200)) };
for (let i = 0; i < 480 && !(__harness.state().connected && __harness.state().view !== "loading"); i++) await new Promise((r) => setTimeout(r, 250));
// Orca sets the host fact; the plain WebKitGTK view has no host to report it.
__harness.setScreenReader(true);
const opened = await __harness.openProject("Harness");
if (opened?.ok !== true) return { ok: false, error: "open project: " + JSON.stringify(opened) };
const session = await __harness.newSession();
if (session?.ok !== true) return { ok: false, error: "new session: " + JSON.stringify(session) };
const replied = await __harness.prompt("Say hello");
if (replied?.ok !== true) return { ok: false, error: "prompt: " + JSON.stringify(replied) };
const seeded = await __harness.goto("scroll-transcript", { turns: 3, activity: true });
if (seeded?.ok !== true) return { ok: false, error: "transcript: " + JSON.stringify(seeded) };
const tail = await __harness.waitForText("Can we improve the interface?", 15000);
if (tail?.ok !== true) return { ok: false, error: "transcript tail: " + JSON.stringify(tail) };
const folders = document.querySelector('[data-testid="project-folder-summary"][aria-expanded="false"]');
if (folders) folders.click();
PAUSE
const rename = document.querySelector('[data-testid="session-rename-action"]');
if (!rename) return { ok: false, error: "no chat row rename action" };
rename.focus();
PAUSE
return { ok: true, folders: document.querySelectorAll('[data-testid^="project-folder-row-"]').length };
"""),
    ("approval", r"""
const held = await __harness.goto("checkpoint-held");
if (held?.ok !== true) return { ok: false, error: "checkpoint: " + JSON.stringify(held) };
PAUSE
return { ok: !!document.querySelector('[data-testid="tool-approval-card"]'), error: "no approval card" };
"""),
    ("files", r"""
const file = await __harness.openFile("cmd/fixture/main.go");
if (file?.ok !== true) return { ok: false, error: "open file: " + JSON.stringify(file) };
PAUSE
return { ok: true };
"""),
    ("settings", r"""
const settings = await __harness.openSettings();
if (settings?.ok !== true) return { ok: false, error: "settings: " + JSON.stringify(settings) };
PAUSE
return { ok: true };
"""),
    ("home", r"""
const home = await __harness.goHome();
if (home?.ok !== true) return { ok: false, error: "home: " + JSON.stringify(home) };
PAUSE
return { ok: true };
"""),
]


def run_page(url: str) -> int:
    """Serves the page; each stdin line names a surface to reach, answered with `ready <json>`."""
    import gi

    gi.require_version("Gtk", "3.0")
    gi.require_version("WebKit2", "4.1")
    from gi.repository import GLib, Gtk, WebKit2

    window = Gtk.Window(title="Den accessibility text attributes")
    window.set_default_size(1280, 800)
    view = WebKit2.WebView(is_ephemeral=True)
    view.get_user_content_manager().add_script(WebKit2.UserScript.new(
        FIRST_RUN_DONE, WebKit2.UserContentInjectedFrames.TOP_FRAME,
        WebKit2.UserScriptInjectionTime.START, None, None))
    # Page console lines reach the scan's log when setup fails.
    view.get_settings().set_enable_write_console_messages_to_stdout(True)
    window.add(view)
    window.show_all()
    bodies = {name: body.replace("PAUSE", PAUSE) for name, body in SURFACES}

    def report(line: str) -> None:
        print(line, flush=True)

    def terminated(_view, reason):
        report(f"terminated {reason.value_nick}")
        Gtk.main_quit()

    def reached(source, result):
        try:
            value = source.call_async_javascript_function_finish(result)
            report("ready " + json.dumps(json.loads(value.to_json(0))))
        except GLib.Error as error:
            report("ready " + json.dumps({"ok": False, "error": error.message}))

    def command(channel, _condition):
        name = channel.readline().strip()
        if not name:
            Gtk.main_quit()
            return False
        view.call_async_javascript_function(bodies[name], -1, None, None, None, None, reached)
        return True

    def loaded(_view, event):
        if event == WebKit2.LoadEvent.FINISHED:
            report("loaded")

    view.connect("web-process-terminated", terminated)
    view.connect("load-changed", loaded)
    GLib.io_add_watch(GLib.IOChannel.unix_new(sys.stdin.fileno()), GLib.IO_IN | GLib.IO_HUP, command)
    view.load_uri(url)
    Gtk.main()
    return 0


def ancestry(node) -> str:
    chain = []
    try:
        parent = node.get_parent()
        while parent is not None and len(chain) < 8:
            attrs = parent.get_attributes() or {}
            chain.append(f"{parent.get_role_name()} {parent.get_name()!r} <{attrs.get('tag', '?')}>")
            parent = parent.get_parent()
    except Exception:  # noqa: BLE001
        pass
    return " < ".join(chain) or "?"


def describe(node) -> str:
    try:
        attrs = node.get_attributes() or {}
        children = [f"{c.get_role_name()} {c.get_name()!r}" for c in
                    (node.get_child_at_index(i) for i in range(min(node.get_child_count(), 4))) if c is not None]
        return f"{node.get_role_name()} {node.get_name()!r} <{attrs.get('tag', '?')}> children={children}"
    except Exception as error:  # noqa: BLE001
        return f"<unreadable: {error}>"


def enable_accessibility_bus() -> None:
    """Asks toolkits on this session to expose their accessibility trees, as a running screen reader does."""
    from gi.repository import Gio, GLib

    bus = Gio.bus_get_sync(Gio.BusType.SESSION, None)
    for _ in range(50):
        try:
            bus.call_sync("org.a11y.Bus", "/org/a11y/bus", "org.freedesktop.DBus.Properties", "Set",
                          GLib.Variant("(ssv)", ("org.a11y.Status", "IsEnabled", GLib.Variant("b", True))),
                          None, Gio.DBusCallFlags.NONE, 2000, None)
            return
        except GLib.Error:
            time.sleep(0.2)
    raise RuntimeError("org.a11y.Bus did not answer")


def run_scan(url: str) -> int:
    import gi

    gi.require_version("Atspi", "2.0")
    from gi.repository import Atspi

    # Large text objects answer slowly; a crash still ends the call at once.
    Atspi.set_timeout(60_000, 60_000)
    try:
        enable_accessibility_bus()
    except RuntimeError as error:
        print(f"unverified: {error}", file=sys.stderr)
        return 2
    page = subprocess.Popen([sys.executable, __file__, "--page", url],
                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    console: list[str] = []
    # A reader thread keeps the pipe drained: page console output must never block the web process.
    lines: queue.Queue[str | None] = queue.Queue()

    def drain() -> None:
        for raw in page.stdout:
            lines.put(raw.strip())
        lines.put(None)

    threading.Thread(target=drain, daemon=True).start()

    def await_line(prefixes: tuple[str, ...], seconds: float) -> str:
        deadline = time.monotonic() + seconds
        while (left := deadline - time.monotonic()) > 0:
            try:
                line = lines.get(timeout=left)
            except queue.Empty:
                break
            if line is None:
                break
            if line.startswith(prefixes):
                return line
            if line:
                console[:] = (console + [line])[-20:]
        return ""

    def unverified(reason: str) -> int:
        print(f"unverified: {reason}", file=sys.stderr)
        for entry in console:
            print(f"  console: {entry}", file=sys.stderr)
        return 2

    def web_process_ended() -> str | None:
        """The page exits once WebKitGTK reports its web process gone; None while it still runs."""
        if not page_exits(5):
            return None
        line = await_line(("terminated",), 5)
        return line or f"the page exited with status {page.returncode}"

    def page_exits(seconds: float) -> bool:
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if page.poll() is not None:
                return True
            time.sleep(0.1)
        return False

    def application():
        desktop = Atspi.get_desktop(0)
        for i in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(i)
            if child is not None and child.get_process_id() == page.pid:
                return child
        return None

    def collect(node, depth, out):
        if node is None or depth > 120:
            return
        try:
            if node.get_text_iface() is not None:
                out.append(node)
            for i in range(node.get_child_count()):
                collect(node.get_child_at_index(i), depth + 1, out)
        except Exception:  # noqa: BLE001
            pass

    try:
        if not await_line(("loaded", "terminated"), 120).startswith("loaded"):
            return unverified("the page did not load")
        totals = {"objects": 0, "calls": 0, "stale": 0}
        reached = []
        for surface, _ in SURFACES:
            page.stdin.write(surface + "\n")
            page.stdin.flush()
            line = await_line(("ready", "terminated"), 300)
            if line.startswith("terminated"):
                print(f"failed: the web process ended reaching {surface}: {line}", file=sys.stderr)
                return 1
            state = json.loads(line.split(" ", 1)[1]) if line.startswith("ready") else {}
            if not state.get("ok"):
                return unverified(f"{surface}: {state.get('error') or 'the page did not answer'}")
            app = application()
            if app is None:
                return unverified("the page is not on the accessibility bus")
            objects = []
            collect(app, 0, objects)
            if not any(o.get_role() == Atspi.Role.LIST_ITEM for o in objects):
                return unverified(f"{surface}: no list items reached the accessibility bus")
            stale = 0
            for node in objects:
                call = "GetText"
                # Read before the calls: a crash leaves nothing to ask.
                label = describe(node)
                inside = ancestry(node)
                try:
                    text = node.get_text_iface()
                    count = Atspi.Text.get_character_count(text)
                    label = f"{label} text={Atspi.Text.get_text(text, 0, -1)!r}"
                    for offset in range(0, count + 1):
                        for defaults in (True, False):
                            call = f"GetAttributeRun({offset}, include_defaults={defaults})"
                            Atspi.Text.get_attribute_run(text, offset, defaults)
                            totals["calls"] += 1
                    call = "GetDefaultAttributes"
                    Atspi.Text.get_default_attributes(text)
                except Exception as error:  # noqa: BLE001
                    ended = web_process_ended()
                    if ended is None:
                        # The page re-rendered this object away; a live web process answered for the rest.
                        stale += 1
                        continue
                    print(f"failed: {surface}: {call} on {label}: {error}", file=sys.stderr)
                    print(f"  inside: {inside}", file=sys.stderr)
                    print(f"  web process: {ended}", file=sys.stderr)
                    return 1
            if stale * 2 > len(objects):
                return unverified(f"{surface}: {stale} of {len(objects)} text objects went away during the scan")
            totals["stale"] += stale
            totals["objects"] += len(objects)
            reached.append(f"{surface} {len(objects)}")
        if page.poll() is not None:
            print("failed: the page exited during the scan", file=sys.stderr)
            return 1
        print(f"ok: {totals['objects']} text objects ({', '.join(reached)}), {totals['calls']} attribute calls, "
              f"{totals['stale']} re-rendered during the scan")
        return 0
    finally:
        try:
            page.stdin.close()
        except BrokenPipeError:
            pass
        page.terminate()
        try:
            page.wait(timeout=10)
        except subprocess.TimeoutExpired:
            page.kill()


def main() -> int:
    if len(sys.argv) == 3 and sys.argv[1] == "--page":
        return run_page(sys.argv[2])
    if len(sys.argv) != 2:
        print(__doc__, file=sys.stderr)
        return 2
    return run_scan(sys.argv[1])


if __name__ == "__main__":
    sys.exit(main())
