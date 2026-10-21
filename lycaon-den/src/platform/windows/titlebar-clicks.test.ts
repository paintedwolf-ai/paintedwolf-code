// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const platform = vi.hoisted(() => ({ name: "linux" }));
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  tauriPlatform: () => platform.name,
  isTauriRuntime: () => true,
}));
const invoke = vi.hoisted(() => vi.fn(async () => undefined));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
const win = vi.hoisted(() => ({ minimize: vi.fn(async () => undefined), toggleMaximize: vi.fn(async () => undefined) }));
vi.mock("@tauri-apps/api/window", () => ({ getCurrentWindow: () => win }));

import { followTitlebarClicks, isTitlebarTarget } from "./titlebar-clicks.ts";
import { setWindowChromeLayoutForTests, type TitlebarActions } from "./window-chrome-layout.ts";

let stop: () => void = () => undefined;
let bar: HTMLElement;
let button: HTMLButtonElement;

function useActions(actions: TitlebarActions) {
  setWindowChromeLayoutForTests({ left: [], right: ["close"], source: "portal", actions });
}

function press(target: HTMLElement, init: MouseEventInit, type = "mousedown") {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, composed: true, ...init });
  const tauriDrag = vi.fn();
  document.addEventListener(type, tauriDrag);
  target.dispatchEvent(event);
  document.removeEventListener(type, tauriDrag);
  return { event, reachedShellDrag: tauriDrag.mock.calls.length > 0 };
}

beforeEach(() => {
  platform.name = "linux";
  document.body.innerHTML = '<header data-tauri-drag-region="deep"><span class="title">Title</span><button>Close</button></header>';
  bar = document.querySelector(".title")!;
  button = document.querySelector("button")!;
  stop = followTitlebarClicks(window);
});
afterEach(() => {
  stop();
  setWindowChromeLayoutForTests(null);
  vi.clearAllMocks();
});

describe("title bar clicks", () => {
  it("follows the shell's drag region rules", () => {
    expect(isTitlebarTarget([bar, bar.parentElement!])).toBe(true);
    expect(isTitlebarTarget([button, button.parentElement!])).toBe(false);
    const own = document.createElement("div");
    own.setAttribute("data-tauri-drag-region", "");
    const child = document.createElement("span");
    own.append(child);
    expect(isTitlebarTarget([own])).toBe(true);
    expect(isTitlebarTarget([child, own])).toBe(false);
  });

  it("applies the desktop's double, middle, and right click actions instead of maximizing", async () => {
    useActions({ double_click: "minimize", middle_click: "lower", right_click: "menu" });
    const double = press(bar, { button: 0, detail: 2 });
    expect([double.event.defaultPrevented, double.reachedShellDrag]).toEqual([true, false]);
    await vi.waitFor(() => expect(win.minimize).toHaveBeenCalledOnce());
    expect(win.toggleMaximize).not.toHaveBeenCalled();
    press(bar, { button: 1, detail: 1 });
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith("linux_titlebar_action", { action: "lower" }));
    expect(press(bar, { button: 2 }, "contextmenu").event.defaultPrevented).toBe(true);
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith("linux_titlebar_action", { action: "menu" }));
  });

  it("keeps a single press for dragging and stops a double click the desktop maps to nothing", async () => {
    useActions({ double_click: "none", middle_click: "none", right_click: "none" });
    expect(press(bar, { button: 0, detail: 1 }).reachedShellDrag).toBe(true);
    const double = press(bar, { button: 0, detail: 2 });
    expect(double.reachedShellDrag).toBe(false);
    useActions({ double_click: "toggle-maximize", middle_click: "none", right_click: "none" });
    press(bar, { button: 0, detail: 2 });
    await vi.waitFor(() => expect(win.toggleMaximize).toHaveBeenCalledOnce());
    expect(invoke).not.toHaveBeenCalled();
  });

  it("leaves controls inside the title bar and other platforms alone", () => {
    expect(press(button, { button: 0, detail: 2 }).reachedShellDrag).toBe(true);
    stop();
    platform.name = "macos";
    stop = followTitlebarClicks(window);
    expect(press(bar, { button: 0, detail: 2 }).reachedShellDrag).toBe(true);
  });
});
