import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { findGeneratedViewportLookups, findScrollbarConstructionsOutsideLayer } from "../test/style-contracts/browser.ts";

const denSrc = join(import.meta.dirname, "..");
const scrollLayer = join(denSrc, "platform", "scrolling", "themed-scrollbars.ts");

/** Runtime proof: `src/platform/scrolling/scrollport-restructure.test.tsx`. */
describe("scrollport frames name their own viewport", () => {
  it("constructs scrollbars only in the scroll layer", () => {
    expect(findScrollbarConstructionsOutsideLayer(denSrc)).toEqual([]);
  });

  it("declares the scrollport as the viewport wherever it constructs one", () => {
    const layer = readFileSync(scrollLayer, "utf8");
    const constructions = [
      ...layer.matchAll(/OverlayScrollbars\(\s*\{([\s\S]*?)\},/g),
    ].map((match) => match[1] ?? "");
    expect(constructions.length).toBeGreaterThan(0);
    for (const construction of constructions) {
      expect(construction).toMatch(/elements:\s*\{\s*viewport/);
    }
    // A bare target lets the library build and populate a viewport of its own.
    expect(layer).not.toMatch(/OverlayScrollbars\(\s*[A-Za-z_$][\w$]*\s*,/);
  });

  it("leaves no code reaching for a generated viewport", () => {
    expect(findGeneratedViewportLookups(denSrc)).toEqual([]);
  });
});
