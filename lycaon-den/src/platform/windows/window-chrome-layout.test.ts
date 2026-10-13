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

const {
  DEFAULT_WINDOW_CHROME_LAYOUT,
  drawsWindowControls,
  loadWindowChromeLayout,
  normalizeWindowChromeLayout,
  setWindowChromeLayoutForTests,
  windowChromeControls,
  windowChromeLayout,
} = await import("./window-chrome-layout.ts");

beforeEach(() => {
  platform = "linux";
  tauri = true;
  invoke.mockReset();
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

describe("loadWindowChromeLayout", () => {
  it("publishes the host's layout on Linux", async () => {
    invoke.mockResolvedValueOnce({ left: ["appmenu"], right: ["close"], source: "portal" });
    await loadWindowChromeLayout();
    expect(invoke).toHaveBeenCalledWith("linux_decoration_layout");
    expect(windowChromeLayout()).toEqual({ left: ["appmenu"], right: ["close"], source: "portal" });
    expect(windowChromeControls("left")).toEqual(["appmenu"]);
    expect(windowChromeControls("right")).toEqual(["close"]);
  });

  it("keeps the default when the host cannot answer", async () => {
    invoke.mockRejectedValueOnce(new Error("no portal"));
    await loadWindowChromeLayout();
    expect(windowChromeLayout()).toBe(DEFAULT_WINDOW_CHROME_LAYOUT);
  });

  it("asks nothing on platforms with native window buttons", async () => {
    platform = "macos";
    await loadWindowChromeLayout();
    expect(invoke).not.toHaveBeenCalled();
    expect(drawsWindowControls()).toBe(false);
  });

  it("asks nothing outside the desktop runtime", async () => {
    tauri = false;
    await loadWindowChromeLayout();
    expect(invoke).not.toHaveBeenCalled();
  });
});
