import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

let platform: "macos" | "linux" = "linux";
let tauri = true;
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  tauriPlatform: () => platform,
  isTauriRuntime: () => tauri,
}));

const invoke = vi.fn(async (_command: string): Promise<unknown> => undefined);
vi.mock("@tauri-apps/api/core", () => ({
  invoke: (command: string) => invoke(command),
}));

type LayoutHandler = (event: { payload: unknown }) => void;
let layoutChanged: LayoutHandler | null = null;
const listen = vi.fn(async (_event: string, handler: LayoutHandler) => {
  layoutChanged = handler;
  return () => undefined;
});
vi.mock("@tauri-apps/api/event", () => ({
  listen: (event: string, handler: LayoutHandler) => listen(event, handler),
}));

const {
  DEFAULT_WINDOW_CHROME_LAYOUT,
  drawsWindowControls,
  normalizeWindowChromeLayout,
  setWindowChromeLayoutForTests,
  windowChromeControls,
  trackWindowChromeLayout,
  windowChromeLayout,
  WINDOW_CHROME_LAYOUT_CHANGED_EVENT,
} = await import("./window-chrome-layout.ts");

beforeEach(() => {
  platform = "linux";
  tauri = true;
  invoke.mockReset();
  listen.mockClear();
  layoutChanged = null;
});

afterEach(() => {
  setWindowChromeLayoutForTests(null);
});

describe("normalizeWindowChromeLayout", () => {
  it("keeps the desktop's order and sides", () => {
    expect(normalizeWindowChromeLayout({ left: ["close"], right: ["appmenu", "minimize"], source: "portal" }))
      .toEqual({ left: ["close"], right: ["appmenu", "minimize"], source: "portal" });
  });

  it("gives a layout without an app menu one at the start of the right cluster", () => {
    expect(normalizeWindowChromeLayout({ left: [], right: ["close"], source: "portal" }))
      .toEqual({ left: [], right: ["appmenu", "close"], source: "portal" });
    expect(normalizeWindowChromeLayout({ left: ["minimize"], right: [], source: "portal" }))
      .toEqual({ left: ["minimize"], right: ["appmenu"], source: "portal" });
  });

  it("drops tokens the shell does not draw", () => {
    expect(normalizeWindowChromeLayout({ left: ["icon", "appmenu"], right: ["spacer", 7, "close"], source: "portal" }))
      .toEqual({ left: ["appmenu"], right: ["close"], source: "portal" });
  });

  it("falls back to the default for an empty or malformed fact", () => {
    expect(normalizeWindowChromeLayout(null)).toBe(DEFAULT_WINDOW_CHROME_LAYOUT);
    expect(normalizeWindowChromeLayout("appmenu:close")).toBe(DEFAULT_WINDOW_CHROME_LAYOUT);
    expect(normalizeWindowChromeLayout({ left: ["icon"], right: [], source: "portal" })).toBe(DEFAULT_WINDOW_CHROME_LAYOUT);
    expect(normalizeWindowChromeLayout({ left: [], right: ["close"], source: "guess" }).source).toBe("default");
  });

  it("defaults to minimize, maximize, and close on the right beside the app menu", () => {
    expect(DEFAULT_WINDOW_CHROME_LAYOUT).toEqual({
      left: [],
      right: ["appmenu", "minimize", "maximize", "close"],
      source: "default",
    });
  });
});

describe("trackWindowChromeLayout", () => {
  it("publishes the host's layout on Linux", async () => {
    invoke.mockResolvedValueOnce({ left: ["appmenu"], right: ["close"], source: "portal" });
    await trackWindowChromeLayout();
    expect(invoke).toHaveBeenCalledWith("linux_decoration_layout");
    expect(windowChromeLayout()).toEqual({ left: ["appmenu"], right: ["close"], source: "portal" });
    expect(windowChromeControls("left")).toEqual(["appmenu"]);
    expect(windowChromeControls("right")).toEqual(["close"]);
  });

  it("follows the desktop when its button layout changes", async () => {
    invoke.mockResolvedValueOnce({ left: ["appmenu"], right: ["close"], source: "portal" });
    await trackWindowChromeLayout();
    expect(listen).toHaveBeenCalledWith(WINDOW_CHROME_LAYOUT_CHANGED_EVENT, expect.any(Function));
    layoutChanged?.({ payload: { left: ["close", "minimize", "maximize"], right: [], source: "portal" } });
    expect(windowChromeControls("left")).toEqual(["close", "minimize", "maximize"]);
    expect(windowChromeControls("right")).toEqual(["appmenu"]);
  });

  it("listens before it reads, so a change during the read wins", async () => {
    invoke.mockImplementationOnce(async () => {
      expect(listen).toHaveBeenCalledOnce();
      layoutChanged?.({ payload: { left: [], right: ["appmenu", "minimize", "close"], source: "portal" } });
      return { left: ["appmenu"], right: ["close"], source: "portal" };
    });
    await trackWindowChromeLayout();
    expect(windowChromeControls("right")).toEqual(["appmenu", "minimize", "close"]);
  });

  it("keeps the default when the host cannot answer", async () => {
    invoke.mockRejectedValueOnce(new Error("no portal"));
    await trackWindowChromeLayout();
    expect(windowChromeLayout()).toBe(DEFAULT_WINDOW_CHROME_LAYOUT);
  });

  it("asks nothing on platforms with native window buttons", async () => {
    platform = "macos";
    await trackWindowChromeLayout();
    expect(invoke).not.toHaveBeenCalled();
    expect(listen).not.toHaveBeenCalled();
    expect(drawsWindowControls()).toBe(false);
  });

  it("asks nothing outside the desktop runtime", async () => {
    tauri = false;
    await trackWindowChromeLayout();
    expect(invoke).not.toHaveBeenCalled();
  });
});
