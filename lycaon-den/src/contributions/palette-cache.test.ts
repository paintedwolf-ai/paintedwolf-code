// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createPaletteReader, watchPaletteTheme, watchWindowIdentityPaint } from "./palette-cache.ts";

const stops: Array<() => void> = [];
afterEach(() => {
  stops.splice(0).forEach((stop) => stop());
  document.body.replaceChildren();
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
function root(): HTMLElement {
  const el = document.createElement("div");
  el.style.setProperty("--ink", "#123456");
  document.body.append(el);
  return el;
}

describe("document palette cache", () => {
  it("resolves a shared theme once, including reads before observer delivery", async () => {
    vi.useFakeTimers();
    const el = root();
    const computed = vi.spyOn(window, "getComputedStyle");
    const read = createPaletteReader((property) => ({ ink: property("--ink") }));
    const first = read(el);
    expect(read(el)).toBe(first);
    expect(computed).toHaveBeenCalledOnce();
    const a = vi.fn(() => read(el));
    const b = vi.fn(() => read(el));
    stops.push(watchPaletteTheme(el, a), watchPaletteTheme(el, b));
    el.style.setProperty("--ink", "#abcdef");
    const next = read(el);
    await vi.advanceTimersByTimeAsync(20);
    expect(a).toHaveReturnedWith(next);
    expect(b).toHaveReturnedWith(next);
    expect(computed).toHaveBeenCalledTimes(2);
    expect(next.ink).toBe("#abcdef");
  });

  it("ignores derived window paint and unrelated element changes", async () => {
    vi.useFakeTimers();
    const el = root();
    const read = createPaletteReader((property) => ({ ink: property("--ink") }));
    const first = read(el);
    const changed = vi.fn();
    const paint = vi.fn();
    stops.push(watchPaletteTheme(el, changed), watchWindowIdentityPaint(el, paint));
    el.style.setProperty("--den-current-window-caret", "#fff");
    el.append(document.createElement("span"));
    await vi.advanceTimersByTimeAsync(20);
    expect(read(el)).toBe(first);
    expect(changed).not.toHaveBeenCalled();
    expect(paint).toHaveBeenCalledOnce();
  });

  it("invalidates appearance and contrast without retaining listeners after teardown", async () => {
    vi.useFakeTimers();
    let contrast = false;
    const listeners = new Set<() => void>();
    vi.stubGlobal("matchMedia", vi.fn(() => ({
      get matches() { return contrast; },
      addEventListener: (_type: string, callback: () => void) => listeners.add(callback),
      removeEventListener: (_type: string, callback: () => void) => listeners.delete(callback),
    }) as unknown as MediaQueryList));
    const el = root();
    const read = createPaletteReader((_property, more) => ({ more }));
    const first = read(el);
    const changed = vi.fn();
    const stop = watchPaletteTheme(el, changed);
    const stop2 = watchPaletteTheme(el, () => {});
    expect(listeners.size).toBe(1);
    el.dataset.denAppearance = "dark";
    await vi.advanceTimersByTimeAsync(20);
    expect(read(el)).not.toBe(first);
    contrast = true;
    listeners.forEach((listener) => listener());
    await vi.advanceTimersByTimeAsync(20);
    expect(read(el).more).toBe(true);
    expect(changed).toHaveBeenCalledTimes(2);
    stop();
    expect(listeners.size).toBe(1);
    stop2();
    expect(listeners.size).toBe(0);
  });
});
