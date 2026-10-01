// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const setTheme = vi.fn(async () => undefined);
const theme = vi.fn(async (): Promise<"light" | "dark" | null> => "dark");
const isFocused = vi.fn(async (): Promise<boolean> => false);
const onThemeChanged = vi.fn(async (handler: (event: { payload: "light" | "dark" }) => void) => {
  onThemeChangedHandler = handler;
  return () => undefined;
});
const onFocusChanged = vi.fn(async (handler: (event: { payload: boolean }) => void) => {
  onFocusChangedHandler = handler;
  return () => undefined;
});
let onThemeChangedHandler: ((event: { payload: "light" | "dark" }) => void) | undefined;
let onFocusChangedHandler: ((event: { payload: boolean }) => void) | undefined;

/** Physical geometry converts to logical units through the scale factor. */
const physicalSize = (width: number, height: number) => ({
  width,
  height,
  toLogical: (factor: number) => ({ width: width / factor, height: height / factor }),
});
const physicalPosition = (x: number, y: number) => ({
  x,
  y,
  toLogical: (factor: number) => ({ x: x / factor, y: y / factor }),
});

const isFullscreen = vi.fn(async () => false);
const isMaximized = vi.fn(async () => false);
const scaleFactor = vi.fn(async () => 1);
const innerSize = vi.fn(async () => physicalSize(800, 600));
const outerSize = vi.fn(async () => physicalSize(812, 640));
const outerPosition = vi.fn(async () => physicalPosition(100, 80));
const currentMonitor = vi.fn(async () => ({
  scaleFactor: 1,
  workArea: { position: physicalPosition(0, 0), size: physicalSize(1440, 900) },
}));
type Bounds = { x: number; y: number; width: number; height: number };
const invoke = vi.fn(async (_command: string, _args?: Bounds) => undefined);
/** Bounds written so far, newest last. */
const writtenBounds = () =>
  invoke.mock.calls.filter(([command]) => command === "den_set_window_bounds").map(([, args]) => args as Bounds);

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: [string, Bounds?]) => invoke(...args),
}));

vi.mock("@tauri-apps/api/window", () => ({
  getCurrentWindow: () => ({
    setTheme,
    theme,
    isFocused,
    onThemeChanged,
    onFocusChanged,
    isFullscreen,
    isMaximized,
    scaleFactor,
    innerSize,
    outerSize,
    outerPosition,
  }),
  currentMonitor: () => currentMonitor(),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => true,
  tauriPlatform: () => "macos" as const,
}));

describe("setupWindowChrome", () => {
  beforeEach(() => {
    setTheme.mockClear();
    theme.mockClear();
    isFocused.mockClear();
    onThemeChanged.mockClear();
    onFocusChanged.mockClear();
    onThemeChangedHandler = undefined;
    onFocusChangedHandler = undefined;
    isFocused.mockResolvedValue(false);
    document.documentElement.style.colorScheme = "";
    document.documentElement.className = "";
  });

  afterEach(() => {
    vi.resetModules();
  });

  it("follows system via setTheme(null), seeds from theme(), and reconciles onThemeChanged", async () => {
    const { setupWindowChrome } = await import("./window-chrome.ts");
    const { osColorSchemeIsDark, resetOsColorSchemeWatchForTests } = await import("../../theme.ts");
    resetOsColorSchemeWatchForTests();

    await setupWindowChrome();

    expect(setTheme).toHaveBeenCalledWith(null);
    expect(theme).toHaveBeenCalledOnce();
    expect(onThemeChanged).toHaveBeenCalledOnce();
    expect(osColorSchemeIsDark()).toBe(true);
    expect(document.documentElement.style.colorScheme).toBe("");
    expect(document.documentElement.classList.contains("den-tauri")).toBe(true);
    expect(document.documentElement.classList.contains("den-tauri-macos")).toBe(true);

    onThemeChangedHandler?.({ payload: "light" });
    expect(osColorSchemeIsDark()).toBe(false);
    expect(document.documentElement.style.colorScheme).toBe("");
  });

  it("seeds windowFocused from isFocused and updates on onFocusChanged", async () => {
    isFocused.mockResolvedValueOnce(false);
    const { setupWindowChrome, windowFocused } = await import("./window-chrome.ts");

    await setupWindowChrome();

    expect(isFocused).toHaveBeenCalledOnce();
    expect(onFocusChanged).toHaveBeenCalledOnce();
    expect(windowFocused()).toBe(false);

    onFocusChangedHandler?.({ payload: true });
    expect(windowFocused()).toBe(true);
    onFocusChangedHandler?.({ payload: false });
    expect(windowFocused()).toBe(false);
  });
});

describe("widenWindowBy", () => {
  beforeEach(() => {
    invoke.mockClear();
    isFullscreen.mockResolvedValue(false);
    isMaximized.mockResolvedValue(false);
    innerSize.mockResolvedValue(physicalSize(800, 600));
    outerSize.mockResolvedValue(physicalSize(812, 640));
    outerPosition.mockResolvedValue(physicalPosition(100, 80));
  });

  it("grows the inner width by the requested delta in one native write that keeps the origin", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");

    await widenWindowBy(61);

    expect(writtenBounds()).toEqual([{ x: 100, y: 80, width: 861, height: 600 }]);
  });

  it("slides back onto the display in the same write as the growth", async () => {
    outerPosition.mockResolvedValue(physicalPosition(1200, 80));
    const { widenWindowBy } = await import("./window-chrome.ts");

    await widenWindowBy(61);

    // 1200 + 861 inner + 12 frame overruns the 1440 work area by 633.
    expect(writtenBounds()).toEqual([{ x: 567, y: 80, width: 861, height: 600 }]);
  });

  it("never asks for more than the work area can hold", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");

    await widenWindowBy(900);

    expect(writtenBounds()[0]).toMatchObject({ width: 1428 });
  });

  it("leaves a maximized or fullscreen window alone — it is already that wide", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");

    isMaximized.mockResolvedValue(true);
    expect(await widenWindowBy(61)).toBe("no-room");
    isMaximized.mockResolvedValue(false);
    isFullscreen.mockResolvedValue(true);
    expect(await widenWindowBy(61)).toBe("no-room");

    expect(writtenBounds()).toEqual([]);
  });

  it("reports the display's refusal so a control can answer for it", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");

    expect(await widenWindowBy(61)).toBe("claimed");

    // Already at the work area: the clamp leaves the width where it is.
    innerSize.mockResolvedValue(physicalSize(1428, 600));
    outerSize.mockResolvedValue(physicalSize(1440, 640));
    expect(await widenWindowBy(61)).toBe("no-room");
    expect(writtenBounds()).toHaveLength(1);
  });

  it("ignores a request for no extra width", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");

    expect(await widenWindowBy(0)).toBe("claimed");
    expect(await widenWindowBy(Number.NaN)).toBe("claimed");

    expect(writtenBounds()).toEqual([]);
  });

  it("folds a claim made while another is landing into that one resize", async () => {
    const { widenWindowBy, windowGeometryClaimPending } = await import("./window-chrome.ts");
    // The window reports the first claim's width once it has been written.
    invoke.mockImplementation(async (command, args) => {
      if (command === "den_set_window_bounds" && args) {
        innerSize.mockResolvedValue(physicalSize(args.width, args.height));
      }
    });

    // Both callers measured the same 800 px viewport and want the same 861.
    const first = widenWindowBy(61);
    const second = widenWindowBy(61);
    expect(windowGeometryClaimPending()).toBe(true);

    expect(await first).toBe("claimed");
    expect(await second).toBe("claimed");
    expect(writtenBounds()).toEqual([{ x: 100, y: 80, width: 861, height: 600 }]);
    expect(windowGeometryClaimPending()).toBe(false);
  });

  it("grows only by what an overlapping larger claim still lacks", async () => {
    const { widenWindowBy } = await import("./window-chrome.ts");
    invoke.mockImplementation(async (command, args) => {
      if (command === "den_set_window_bounds" && args) {
        innerSize.mockResolvedValue(physicalSize(args.width, args.height));
      }
    });

    const first = widenWindowBy(61);
    const second = widenWindowBy(100);
    await Promise.all([first, second]);

    expect(writtenBounds().map((bounds) => bounds.width)).toEqual([861, 900]);
  });
});
