"""Asks WebKitGTK for AT-SPI text attributes across Den's sidebar, as a screen reader does.

WebKitGTK can abort its web process while answering `GetAttributeRun` for a
list item whose content it lays out unusually; Orca asks for those attributes
when focus reaches a control inside such an item. This loads the harness Den in
a WebKitGTK view, opens a chat so the sidebar shows a chat row with its Pin
and Rename actions (focused, so the actions are revealed) and the project's
folder rows, then calls `GetAttributeRun` at every caret offset and
`GetDefaultAttributes` on every text object in the sidebar.

usage: webkit_atspi_text_attributes.py <harness URL>
exit: 0 every call answered; 1 a call aborted the web process or failed;
      2 the scenario could not be set up (unverified, never a pass)
"""

import json
import subprocess
import sys
import time

# A person who finished first-run setup and turned tips off, so nothing covers the sidebar.
FIRST_RUN_DONE = """
localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ firstRunSetupCompleted: true }));
localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
"""

SEED = r"""
for (let i = 0; i < 240 && !window.__harness; i++) await new Promise((r) => setTimeout(r, 250));
if (!window.__harness) return JSON.stringify({ ok: false, error: "harness API missing; page reads " + JSON.stringify(document.body.innerText.slice(0, 200)) });
for (let i = 0; i < 480 && !(__harness.state().connected && __harness.state().view !== "loading"); i++) await new Promise((r) => setTimeout(r, 250));
const opened = await __harness.openProject("Harness");
if (opened?.ok !== true) return JSON.stringify({ ok: false, error: "open project: " + JSON.stringify(opened) + "; page reads " + JSON.stringify(document.body.innerText.slice(0, 300)) });
const session = await __harness.newSession();
if (session?.ok !== true) return JSON.stringify({ ok: false, error: "new session: " + JSON.stringify(session) });
for (let i = 0; i < 80 && !document.querySelector('[data-testid="session-rename-action"]'); i++) await new Promise((r) => setTimeout(r, 250));
const folders = document.querySelector('[data-testid="project-folder-summary"][aria-expanded="false"]');
if (folders) folders.click();
await new Promise((r) => setTimeout(r, 500));
const rename = document.querySelector('[data-testid="session-rename-action"]');
if (!rename) return JSON.stringify({ ok: false, error: "no chat row rename action" });
rename.focus();
await new Promise((r) => setTimeout(r, 500));
return JSON.stringify({ ok: true, folders: document.querySelectorAll('[data-testid^="project-folder-row-"]').length });
"""


def run_page(url: str) -> int:
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

    def report(line: str) -> None:
        print(line, flush=True)

    def terminated(_view, reason):
        report(f"terminated {reason.value_nick}")
        Gtk.main_quit()

    def seeded(source, result):
        try:
            value = source.call_async_javascript_function_finish(result)
            report("seeded " + value.to_string())
        except GLib.Error as error:
            report("seeded " + json.dumps({"ok": False, "error": error.message}))

    def loaded(_view, event):
        if event == WebKit2.LoadEvent.FINISHED:
            view.call_async_javascript_function(SEED, -1, None, None, None, None, seeded)

    view.connect("web-process-terminated", terminated)
    view.connect("load-changed", loaded)
    view.load_uri(url)
    Gtk.main()
    return 0


def describe(node) -> str:
    try:
        attrs = node.get_attributes() or {}
        return f"{node.get_role_name()} {node.get_name()!r} <{attrs.get('tag', '?')}>"
    except Exception as error:  # noqa: BLE001
        return f"<unreadable: {error}>"


def run_scan(url: str) -> int:
    import gi

    gi.require_version("Atspi", "2.0")
    from gi.repository import Atspi

    page = subprocess.Popen([sys.executable, __file__, "--page", url], stdout=subprocess.PIPE, text=True)
    try:
        line = ""
        console = []
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            line = page.stdout.readline().strip()
            if line.startswith(("seeded", "terminated")) or page.poll() is not None:
                break
            if line:
                console = (console + [line])[-20:]
        seed = json.loads(line.split(" ", 1)[1]) if line.startswith("seeded") else {}
        if not seed.get("ok"):
            print(f"unverified: {seed.get('error') or line or 'the page did not seed'}", file=sys.stderr)
            for entry in console:
                print(f"  console: {entry}", file=sys.stderr)
            return 2

        app = None
        desktop = Atspi.get_desktop(0)
        for i in range(desktop.get_child_count()):
            child = desktop.get_child_at_index(i)
            if child is not None and child.get_process_id() == page.pid:
                app = child
        if app is None:
            print("unverified: the page is not on the accessibility bus", file=sys.stderr)
            return 2

        def collect(node, inside, depth, out):
            if node is None or depth > 80:
                return
            try:
                attrs = node.get_attributes() or {}
                sidebar = inside or attrs.get("xml-roles") == "complementary" or attrs.get("tag") == "aside"
                if sidebar and node.get_text_iface() is not None:
                    out.append(node)
                for i in range(node.get_child_count()):
                    collect(node.get_child_at_index(i), sidebar, depth + 1, out)
            except Exception:  # noqa: BLE001
                pass

        objects = []
        collect(app, False, 0, objects)
        rows = [o for o in objects if o.get_role() == Atspi.Role.LIST_ITEM]
        if not rows:
            print("unverified: no sidebar list items reached the accessibility bus", file=sys.stderr)
            return 2

        calls = 0
        for node in objects:
            text = node.get_text_iface()
            label = describe(node)
            try:
                count = Atspi.Text.get_character_count(text)
                for offset in range(0, count + 1):
                    for defaults in (True, False):
                        Atspi.Text.get_attribute_run(text, offset, defaults)
                        calls += 1
                Atspi.Text.get_default_attributes(text)
            except Exception as error:  # noqa: BLE001
                print(f"failed: {label} after {calls} calls: {error}", file=sys.stderr)
                return 1
        if page.poll() is not None:
            print("failed: the page exited during the scan", file=sys.stderr)
            return 1
        print(f"ok: {len(objects)} sidebar text objects ({len(rows)} list items, "
              f"{seed.get('folders', 0)} folder rows), {calls} attribute calls")
        return 0
    finally:
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
