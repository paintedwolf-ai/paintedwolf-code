import { describe, expect, it } from "vitest";
import { frameKeymapSource } from "../contributions/frame-keymap.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";
import { stockBindingId } from "../contributions/stock-frame-test.ts";
import { shortcutDisplayEntries } from "./command-display.ts";

describe("shortcut rows", () => {
  it("lists exactly the bindings each platform's keymap can change", () => {
    for (const platform of ["macos", "linux", "windows"] as const) {
      const rows = shortcutDisplayEntries(STOCK_FRAME, platform).map((entry) => entry.id).sort();
      const changeable = frameKeymapSource(STOCK_FRAME, platform).declarations.map((row) => row.id).sort();
      expect(rows, platform).toEqual(changeable);
    }
  });

  it("leaves out a binding the macOS menu bar owns", () => {
    const id = stockBindingId("app-menu-open");
    expect(shortcutDisplayEntries(STOCK_FRAME, "macos").some((entry) => entry.id === id)).toBe(false);
    expect(shortcutDisplayEntries(STOCK_FRAME, "linux").some((entry) => entry.id === id)).toBe(true);
  });
});
