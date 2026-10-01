// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

describe("tauri platform chrome", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
    vi.resetModules();
  });

  it("uses custom chrome drag regions on macOS and Linux", async () => {
    for (const platform of ["macos", "linux"] as const) {
      vi.resetModules();
      vi.stubEnv("TAURI_ENV_PLATFORM", platform);
      const { usesCustomWindowChrome, tauriDragRegionProps } = await import(
        "./runtime.ts"
      );
      expect(usesCustomWindowChrome()).toBe(true);
      expect(tauriDragRegionProps()).toEqual({ "data-tauri-drag-region": "" });
      expect(tauriDragRegionProps({ deep: true })).toEqual({
        "data-tauri-drag-region": "deep",
      });
    }
  });

  it("falls back to navigator when build-time platform is missing in Tauri", async () => {
    vi.resetModules();
    vi.stubGlobal("window", { __TAURI__: {} });
    vi.stubGlobal("navigator", {
      userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X)",
      platform: "MacIntel",
    });
    const { tauriPlatform, usesCustomWindowChrome } = await import("./runtime.ts");
    expect(tauriPlatform()).toBe("macos");
    expect(usesCustomWindowChrome()).toBe(true);
  });

  it("uses native title bar on Windows and web", async () => {
    vi.stubEnv("TAURI_ENV_PLATFORM", "windows");
    const { usesCustomWindowChrome, tauriDragRegionProps } = await import(
      "./runtime.ts"
    );
    expect(usesCustomWindowChrome()).toBe(false);
    expect(tauriDragRegionProps()).toEqual({});
  });
});

describe("waitForTauriRuntime", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it("returns true immediately when Tauri globals are present", async () => {
    vi.stubGlobal("window", { __TAURI__: {} });
    const { waitForTauriRuntime } = await import("./runtime.ts");
    await expect(waitForTauriRuntime(100)).resolves.toBe(true);
  });

  it("waits until Tauri globals appear", async () => {
    vi.useFakeTimers();
    const win: Record<string, unknown> = {};
    vi.stubGlobal("window", win);
    const { waitForTauriRuntime } = await import("./runtime.ts");
    const pending = waitForTauriRuntime(500);
    win.__TAURI__ = {};
    await vi.advanceTimersByTimeAsync(60);
    await expect(pending).resolves.toBe(true);
  });
});
