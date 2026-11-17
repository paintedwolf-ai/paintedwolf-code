import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { findGeneratedViewportLookups, findScrollbarConstructionsOutsideLayer } from "../test/style-contracts/browser.ts";

const denSrc = join(import.meta.dirname, "..");

/** Runtime proof: `src/platform/scrolling/scrollport-restructure.test.tsx`. */
describe("scrollport frames name their own viewport", () => {
  it("constructs scrollbars only in the scroll layer", () => {
    expect(findScrollbarConstructionsOutsideLayer(denSrc)).toEqual([]);
  });

  it("leaves no code reaching for a generated viewport", () => {
    expect(findGeneratedViewportLookups(denSrc)).toEqual([]);
  });
});
