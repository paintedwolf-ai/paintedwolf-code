// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  applySystemAppearancePayload,
  onSystemAppearanceChange,
  resetSystemAppearanceForTests,
  systemThemeAdjustments,
} from "./system-appearance.ts";

const root = () => document.documentElement;

afterEach(() => {
  resetSystemAppearanceForTests();
});

describe("system appearance", () => {
  it("stamps Increase Contrast and Reduce Transparency on the root", () => {
    applySystemAppearancePayload({
      increaseContrast: true,
      reduceTransparency: true,
      accent: null,
      revision: 1,
    });
    expect(root().getAttribute("data-den-contrast")).toBe("more");
    expect(root().getAttribute("data-den-transparency")).toBe("reduced");

    applySystemAppearancePayload({
      increaseContrast: false,
      reduceTransparency: false,
      accent: null,
      revision: 2,
    });
    expect(root().hasAttribute("data-den-contrast")).toBe(false);
    expect(root().hasAttribute("data-den-transparency")).toBe(false);
  });

  it("ignores a snapshot older than the live state", () => {
    applySystemAppearancePayload({
      increaseContrast: true,
      reduceTransparency: false,
      accent: null,
      revision: 3,
    });
    applySystemAppearancePayload({
      increaseContrast: false,
      reduceTransparency: false,
      accent: null,
      revision: 2,
    });
    expect(systemThemeAdjustments().increaseContrast).toBe(true);
  });

  it("carries the chosen accent into theme adjustments and notifies once per change", () => {
    const listener = vi.fn();
    onSystemAppearanceChange(listener);
    const payload = {
      increaseContrast: false,
      reduceTransparency: false,
      accent: "#007aff",
      revision: 1,
    };
    applySystemAppearancePayload(payload);
    applySystemAppearancePayload({ ...payload, revision: 2 });
    expect(listener).toHaveBeenCalledTimes(1);
    expect(systemThemeAdjustments()).toEqual({ accent: "#007aff", increaseContrast: false });
  });
});
