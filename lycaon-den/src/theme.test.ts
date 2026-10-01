// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  onOsColorSchemeChange,
  osColorSchemeIsDark,
  reconcileOsColorScheme,
  resetOsColorSchemeWatchForTests,
  syncOsColorScheme,
} from "./theme.ts";

describe("os color scheme", () => {
  afterEach(() => {
    resetOsColorSchemeWatchForTests();
    document.documentElement.style.colorScheme = "";
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("syncOsColorScheme records the current OS preference", () => {
    const mq = {
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal("matchMedia", () => mq);

    syncOsColorScheme();
    expect(osColorSchemeIsDark()).toBe(true);
  });

  it("onOsColorSchemeChange updates when prefers-color-scheme changes", () => {
    let matches = false;
    const changeHandlers: Array<(event: MediaQueryListEvent) => void> = [];
    const mq = {
      get matches() {
        return matches;
      },
      addEventListener: vi.fn((type: string, handler: (event: MediaQueryListEvent) => void) => {
        if (type === "change") changeHandlers.push(handler);
      }),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
    };
    vi.stubGlobal("matchMedia", () => mq);

    const seen: boolean[] = [];
    onOsColorSchemeChange((dark) => seen.push(dark));
    expect(seen).toEqual([false]);

    matches = true;
    for (const handler of changeHandlers) {
      handler({ matches: true } as MediaQueryListEvent);
    }

    expect(seen).toEqual([false, true]);
    expect(osColorSchemeIsDark()).toBe(true);
  });

  it("reconcileOsColorScheme fans out without requiring matchMedia", () => {
    const seen: boolean[] = [];
    onOsColorSchemeChange((dark) => seen.push(dark));
    // Initial listener call uses matchMedia (false without stub) or prior null → false
    expect(seen[0]).toBe(false);

    reconcileOsColorScheme(true);
    expect(osColorSchemeIsDark()).toBe(true);
    expect(seen).toEqual([false, true]);

    reconcileOsColorScheme(false);
    expect(osColorSchemeIsDark()).toBe(false);
    expect(seen).toEqual([false, true, false]);
  });
});
