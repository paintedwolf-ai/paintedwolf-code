import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@solidjs/testing-library";
import type { ContextMenuItem } from "../../components/ContextMenu.tsx";

let tauri = true;
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  tauriPlatform: () => "linux" as const,
  isTauriRuntime: () => tauri,
}));

const copyTextToClipboard = vi.fn(async (_text: string) => undefined);
vi.mock("../../utils/clipboard.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../utils/clipboard.ts")>()),
  copyTextToClipboard: (text: string) => copyTextToClipboard(text),
}));

const minimize = vi.fn(async () => undefined);
const toggleMaximize = vi.fn(async () => undefined);
const setFullscreen = vi.fn(async (_on: boolean) => undefined);
vi.mock("../windows/current-window.ts", () => ({
  currentWindow: async () => ({ minimize, toggleMaximize, setFullscreen }),
}));

const { appMenuOpenRequests, inWindowAppMenuItems, requestAppMenuOpen } = await import("./in-window-app-menu.ts");
const { attachAppMenuCommands, resetAppMenuForTests } = await import("./app-menu.ts");
const { seedStockFrame, stockId } = await import("../../contributions/stock-frame-test.ts");
const { resetContributionStoreForTest } = await import("../../contributions/contribution-store.ts");
const { setShortcutPlatformForTests } = await import("../../shortcuts/platform.ts");
const { registerCommandHandler, resetDispatcherForTests } = await import("../../shortcuts/dispatcher.ts");
const { setWindowFullscreenForTests, setWindowMaximizedForTests } = await import("../windows/window-maximized.ts");
const { AppMenuButton } = await import("../../components/shell/AppMenuButton.tsx");
const { EditorState } = await import("@codemirror/state");
const { EditorView } = await import("@codemirror/view");
const { history } = await import("@codemirror/commands");

type Section = ContextMenuItem & { submenu: readonly ContextMenuItem[] };

function section(items: ContextMenuItem[], label: string): Section {
  const found = items.find((item) => item.label === label);
  if (!found || found.submenu === undefined) throw new Error(`no ${label} section`);
  return found as Section;
}

const labels = (items: readonly ContextMenuItem[]) => items.map((item) => item.separator ? "—" : item.label);

beforeEach(() => {
  setShortcutPlatformForTests("linux");
  seedStockFrame();
  resetAppMenuForTests();
  setWindowMaximizedForTests(false);
  setWindowFullscreenForTests(false);
  minimize.mockClear();
  toggleMaximize.mockClear();
  setFullscreen.mockClear();
});

afterEach(() => {
  tauri = true;
  document.body.replaceChildren();
  cleanup();
  resetDispatcherForTests();
  resetContributionStoreForTest();
  setShortcutPlatformForTests(null);
});

describe("inWindowAppMenuItems", () => {
  it("carries every menu the native bar would, in bar order", () => {
    const items = inWindowAppMenuItems();
    expect(items.map((item) => item.label)).toEqual([
      "Painted Wolf Code", "File", "Edit", "Selection", "View", "Go", "Window", "Help",
    ]);
    expect(items.every((item) => item.submenu !== undefined)).toBe(true);
  });

  it("projects contributed commands with their live chords", () => {
    const help = section(inWindowAppMenuItems(), "Help");
    const shortcuts = help.submenu.find((item) => item.testId === `app-menu-item-${stockId("help-shortcuts")}`);
    expect(shortcuts?.label).toBe("Keyboard shortcuts");
    // The one chord with an accelerator spelling, as the native bar shows it.
    expect(shortcuts?.shortcut).toBe("Ctrl+/");
  });

  it("runs a chosen command through the menu activation gate", () => {
    const activate = vi.fn();
    const detach = attachAppMenuCommands(activate);
    const help = section(inWindowAppMenuItems(), "Help");
    help.submenu.find((item) => item.testId === `app-menu-item-${stockId("help-shortcuts")}`)?.onSelect?.();
    expect(activate).toHaveBeenCalledWith(stockId("help-shortcuts"));
    detach();
  });

  it("ends the app menu with Quit on its platform chord", () => {
    const app = section(inWindowAppMenuItems(), "Painted Wolf Code");
    const quit = app.submenu[app.submenu.length - 1]!;
    expect(quit.label).toBe("Quit Painted Wolf Code");
    expect(quit.shortcut).toBe("Ctrl+Q");
    const handler = vi.fn();
    const release = registerCommandHandler("app.quit", handler);
    quit.onSelect?.();
    expect(handler).toHaveBeenCalledTimes(1);
    release();
  });

  it("offers the edit quartet against the focused editable only", () => {
    const edit = section(inWindowAppMenuItems(), "Edit");
    expect(labels(edit.submenu).slice(0, 7)).toEqual(["Undo", "Redo", "—", "Cut", "Copy", "Paste", "Select all"]);
    expect(edit.submenu.find((item) => item.label === "Select all")?.disabled).toBe(true);
    expect(edit.submenu.find((item) => item.label === "Undo")?.shortcut).toBe("Ctrl+Z");

    const textarea = document.createElement("textarea");
    textarea.value = "draft";
    document.body.append(textarea);
    textarea.focus();
    textarea.setSelectionRange(0, 5);
    const focused = section(inWindowAppMenuItems(), "Edit");
    expect(focused.submenu.find((item) => item.label === "Select all")?.disabled).toBe(false);
    expect(focused.submenu.find((item) => item.label === "Copy")?.disabled).toBe(false);
    expect(focused.submenu.find((item) => item.label === "Paste")?.disabled).toBe(false);
    textarea.remove();
  });

  it("retains the opening editable when menu focus moves, and refuses a removed target", async () => {
    const execCommand = vi.fn(() => true);
    Object.defineProperty(document, "execCommand", { value: execCommand, configurable: true });
    copyTextToClipboard.mockClear();
    const textarea = document.createElement("textarea");
    textarea.value = "draft";
    document.body.append(textarea);
    textarea.focus();
    textarea.setSelectionRange(0, 5);
    const edit = section(inWindowAppMenuItems(), "Edit");
    edit.submenu.find((item) => item.label === "Undo")?.onSelect?.();
    edit.submenu.find((item) => item.label === "Redo")?.onSelect?.();
    expect(execCommand.mock.calls).toEqual([["undo"], ["redo"]]);
    edit.submenu.find((item) => item.label === "Copy")?.onSelect?.();
    await vi.waitFor(() => expect(copyTextToClipboard).toHaveBeenCalledWith("draft"));

    textarea.blur();
    copyTextToClipboard.mockClear();
    edit.submenu.find((item) => item.label === "Copy")?.onSelect?.();
    edit.submenu.find((item) => item.label === "Undo")?.onSelect?.();
    await vi.waitFor(() => expect(copyTextToClipboard).toHaveBeenCalledWith("draft"));
    expect(execCommand).toHaveBeenCalledTimes(3);
    textarea.remove();
    copyTextToClipboard.mockClear();
    edit.submenu.find((item) => item.label === "Copy")?.onSelect?.();
    edit.submenu.find((item) => item.label === "Undo")?.onSelect?.();
    expect(copyTextToClipboard).not.toHaveBeenCalled();
    expect(execCommand).toHaveBeenCalledTimes(3);
  });

  it("steps a focused code editor through its own history", () => {
    const parent = document.createElement("div");
    document.body.append(parent);
    const view = new EditorView({ state: EditorState.create({ doc: "one", extensions: [history()] }), parent });
    view.dispatch({ changes: { from: 3, insert: " two" } });
    view.contentDOM.focus();
    const edit = section(inWindowAppMenuItems(), "Edit");
    edit.submenu.find((item) => item.label === "Undo")?.onSelect?.();
    expect(view.state.doc.toString()).toBe("one");
    edit.submenu.find((item) => item.label === "Redo")?.onSelect?.();
    expect(view.state.doc.toString()).toBe("one two");
    view.destroy();
  });

  it("leaves window entries and empty sections out outside the desktop app", () => {
    tauri = false;
    resetContributionStoreForTest();
    const items = inWindowAppMenuItems();
    expect(items.map((item) => item.label)).toEqual(["Painted Wolf Code", "Edit", "Window"]);
    expect(labels(section(items, "Window").submenu)).toEqual(["Close window"]);
  });

  it("drives the window from the Window and View menus", async () => {
    const win = section(inWindowAppMenuItems(), "Window");
    expect(labels(win.submenu).slice(0, 4)).toEqual(["Minimize", "Maximize", "—", "Close window"]);
    win.submenu[0]?.onSelect?.();
    win.submenu[1]?.onSelect?.();
    await vi.waitFor(() => expect(toggleMaximize).toHaveBeenCalledTimes(1));
    expect(minimize).toHaveBeenCalledTimes(1);

    setWindowMaximizedForTests(true);
    expect(section(inWindowAppMenuItems(), "Window").submenu[1]?.label).toBe("Restore");

    const view = section(inWindowAppMenuItems(), "View");
    const fullscreen = view.submenu[view.submenu.length - 1]!;
    expect(fullscreen.label).toBe("Enter full screen");
    fullscreen.onSelect?.();
    await vi.waitFor(() => expect(setFullscreen).toHaveBeenCalledWith(true));
    setWindowFullscreenForTests(true);
    expect(section(inWindowAppMenuItems(), "View").submenu.at(-1)?.label).toBe("Exit full screen");
  });
});

describe("AppMenuButton", () => {
  it.each(["pointer", "keyboard"])("cuts the captured selection after %s menu opening", async (method) => {
    render(() => <AppMenuButton side="right" />);
    const textarea = document.createElement("textarea");
    textarea.value = "draft";
    document.body.append(textarea);
    textarea.focus();
    textarea.setSelectionRange(1, 4);
    copyTextToClipboard.mockClear();
    const trigger = screen.getByTestId("window-app-menu");
    if (method === "pointer") {
      const down = new MouseEvent("pointerdown", { bubbles: true, cancelable: true });
      expect(trigger.dispatchEvent(down)).toBe(false);
      expect(document.activeElement).toBe(textarea);
      trigger.click();
    } else {
      requestAppMenuOpen();
    }
    const edit = await vi.waitFor(() => screen.getByTestId("app-menu-section-edit"));
    edit.click();
    const cut = await vi.waitFor(() => screen.getByRole("menuitem", { name: /^Cut/ }));
    expect(cut.getAttribute("aria-disabled")).not.toBe("true");
    cut.click();
    await vi.waitFor(() => expect(textarea.value).toBe("dt"));
    expect(copyTextToClipboard).toHaveBeenCalledWith("raf");
    expect(textarea.selectionStart).toBe(1);
    textarea.remove();
  });

  it("opens the menu from its button and from a keyboard request", async () => {
    render(() => <AppMenuButton side="right" />);
    const trigger = screen.getByTestId("window-app-menu");
    expect(trigger.getAttribute("aria-label")).toBe("App menu");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");

    trigger.click();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    await vi.waitFor(() => expect(screen.getByTestId("app-menu-section-help")).toBeTruthy());

    trigger.click();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");

    const before = appMenuOpenRequests();
    requestAppMenuOpen();
    expect(appMenuOpenRequests()).toBe(before + 1);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    requestAppMenuOpen();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
  });

  it("opens from F10 like a menu bar, with its first section current", async () => {
    render(() => <AppMenuButton side="right" />);
    requestAppMenuOpen();
    const first = await vi.waitFor(() => {
      const item = screen.getByTestId("app-menu-section-app");
      expect(document.activeElement).toBe(item);
      return item;
    });
    expect(first.closest("[data-keyboard-open]")).not.toBeNull();
  });

  it("opens from a click without marking a current entry", async () => {
    render(() => <AppMenuButton side="right" />);
    screen.getByTestId("window-app-menu").click();
    const first = await vi.waitFor(() => screen.getByTestId("app-menu-section-app"));
    expect(first.closest("[data-keyboard-open]")).toBeNull();
  });
});

describe("Install pw command", () => {
  it("is offered while no package owns the command", () => {
    seedStockFrame({ shellCommandPackaged: false });
    const app = section(inWindowAppMenuItems(), "Painted Wolf Code");
    expect(labels(app.submenu)).toContain("Install pw command");
  });

  it("is left out where the system package installs the command", () => {
    seedStockFrame({ shellCommandPackaged: true });
    const app = section(inWindowAppMenuItems(), "Painted Wolf Code");
    expect(labels(app.submenu)).not.toContain("Install pw command");
  });
});
