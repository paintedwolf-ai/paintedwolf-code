import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const invoke = vi.fn(async () => undefined);

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...(args as [])),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => true,
}));

import {
  STOCK_DARK_BACKDROP_HEX,
  STOCK_LIGHT_BACKDROP_HEX,
  backdropForTheme,
  parseBackdropHex,
  stockBackdrop,
  syncWindowBackdrop,
} from "./window-backdrop.ts";

const here = dirname(fileURLToPath(import.meta.url));

describe("backdrop hex", () => {
  it("parses theme tokens and rejects junk", () => {
    expect(parseBackdropHex("#1a1b26")).toEqual({ r: 26, g: 27, b: 38 });
    expect(parseBackdropHex("  #191817  ")).toEqual({ r: 25, g: 24, b: 23 });
    expect(parseBackdropHex("191817")).toBeNull();
    expect(parseBackdropHex("#fff")).toBeNull();
    expect(parseBackdropHex("#faf4edff")).toBeNull();
    expect(parseBackdropHex("#ffffff80")).toBeNull();
  });

  it("falls through to stock when the token is missing or unparseable", () => {
    expect(backdropForTheme("dark", "#0b0b10")).toEqual({ r: 11, g: 11, b: 16 });
    expect(backdropForTheme("dark")).toEqual(stockBackdrop("dark"));
    expect(backdropForTheme("light", "paper")).toEqual(stockBackdrop("light"));
  });

  it("keeps stock literals in step with the boot style and the native memo", () => {
    const indexHtml = readFileSync(join(here, "../../../index.html"), "utf8");
    const rust = readFileSync(
      join(here, "../../../src-tauri/src/window_backdrop.rs"),
      "utf8",
    );
    expect(indexHtml).toContain(STOCK_LIGHT_BACKDROP_HEX);
    expect(indexHtml).toContain(STOCK_DARK_BACKDROP_HEX);
    expect(rust).toContain(STOCK_DARK_BACKDROP_HEX);
    expect(rust).toContain("0xff, 0xff, 0xff");
  });
});

describe("syncWindowBackdrop", () => {
  beforeEach(() => {
    invoke.mockClear();
  });

  afterEach(() => {
    invoke.mockReset();
    invoke.mockResolvedValue(undefined);
  });

  it("pushes both scheme paints and the active mode atomically", async () => {
    syncWindowBackdrop("system", "dark", {
      light: "#faf4ed",
      dark: "#1a1b26",
    });
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledOnce());
    expect(invoke).toHaveBeenCalledWith("den_set_window_backdrop", {
      light: { r: 250, g: 244, b: 237 },
      dark: { r: 26, g: 27, b: 38 },
      scheme: "dark",
      mode: "system",
    });
  });
});
