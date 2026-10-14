import { beforeEach, describe, expect, it, vi } from "vitest";

let tauri = true;
vi.mock("../runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../runtime.ts")>()),
  isTauriRuntime: () => tauri,
}));

const isMaximized = vi.fn(async () => false);
const isFullscreen = vi.fn(async () => false);
let resized: (() => void) | null = null;
const onResized = vi.fn(async (handler: () => void) => {
  resized = handler;
  return () => undefined;
});
vi.mock("./current-window.ts", () => ({
  currentWindow: async () => ({ isMaximized, isFullscreen, onResized }),
}));

const {
  setWindowFullscreenForTests,
  setWindowMaximizedForTests,
  trackWindowMaximized,
  windowFullscreen,
  windowMaximized,
} = await import("./window-maximized.ts");

beforeEach(() => {
  tauri = true;
  resized = null;
  isMaximized.mockReset().mockResolvedValue(false);
  isFullscreen.mockReset().mockResolvedValue(false);
  onResized.mockClear();
  setWindowMaximizedForTests(false);
  setWindowFullscreenForTests(false);
});

describe("trackWindowMaximized", () => {
  it("reads the state once and again after every resize", async () => {
    isMaximized.mockResolvedValueOnce(true);
    await trackWindowMaximized();
    expect(windowMaximized()).toBe(true);
    expect(windowFullscreen()).toBe(false);

    isMaximized.mockResolvedValueOnce(false);
    isFullscreen.mockResolvedValueOnce(true);
    resized?.();
    await vi.waitFor(() => expect(windowMaximized()).toBe(false));
    expect(windowFullscreen()).toBe(true);
  });

  it("keeps the last state when a read fails", async () => {
    await trackWindowMaximized();
    setWindowMaximizedForTests(true);
    isMaximized.mockRejectedValueOnce(new Error("gone"));
    resized?.();
    await Promise.resolve();
    expect(windowMaximized()).toBe(true);
  });

  it("does nothing outside the desktop runtime", async () => {
    tauri = false;
    await trackWindowMaximized();
    expect(onResized).not.toHaveBeenCalled();
  });
});
