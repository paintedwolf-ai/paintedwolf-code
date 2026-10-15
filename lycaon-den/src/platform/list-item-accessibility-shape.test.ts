import { describe, expect, it } from "vitest";
import { denSourceRoot } from "../test/source-corpus.ts";
import { findListItemGridRules, findListItemShapes } from "../test/style-contracts/list-item-shapes.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

/**
 * WebKitGTK aborts its web process answering a screen reader's AT-SPI text-attribute query on a
 * list item that holds a `display: none` subtree or is itself the grid with an actions column.
 * Grid layout goes on a wrapper inside the item on every engine. Content hidden until hover or
 * expansion is not rendered under the WebKitGTK AT-SPI workaround; rules scoped off the workaround
 * keep other engines' rendering. `den:webkit:atspi` exercises the real engine.
 */
describe("list items WebKitGTK can describe", () => {
  it("never hold a CSS-hidden subtree or lay out as a grid", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
    const findings = findListItemShapes(denSourceRoot).map((f) => `${f.site} ${f.shape}: ${f.detail}`);
    expect(findings).toEqual([]);
  });

  it("are never the subject of a grid rule", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
    expect(findListItemGridRules(denSourceRoot)).toEqual([]);
  });
});
