import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  DEN_CHROME_ATTR,
  DEN_PROSE_ATTR,
  chromeProps,
  proseProps,
} from "./ui-chrome.ts";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readFileSync(join(denSrc, rel), "utf8");
}

describe("ui chrome mark", () => {
  it("exposes a single attribute mark and props helper", () => {
    expect(DEN_CHROME_ATTR).toBe("data-den-chrome");
    expect(chromeProps()).toEqual({ "data-den-chrome": "" });
  });

  it("applies non-select via the attribute mark", () => {
    const css = read("styling/ui-chrome.css");
    expect(css).toMatch(/\[data-den-chrome\]\s*\{/);
    expect(css).toMatch(/user-select:\s*none/);
    expect(css).toMatch(
      /\[data-den-chrome\]\s+:is\(input,\s*textarea,\s*\[contenteditable="true"\]\)/,
    );
  });

  it("takes selection back on prose islands under a chrome root", () => {
    expect(DEN_PROSE_ATTR).toBe("data-den-prose");
    expect(proseProps()).toEqual({ "data-den-prose": "" });

    const css = read("styling/ui-chrome.css");
    expect(css).toMatch(/\[data-den-prose\]\s*\{/);
    expect(css).toMatch(/user-select:\s*text/);

    expect(read("components/NoticeRail.tsx")).toMatch(/proseProps\s*\(/);
  });

  it("keeps popup triggers visually pressed while their surface is open", () => {
    const css = read("styling/ui-chrome.css");
    expect(css).toMatch(
      /button\[aria-haspopup\]\[aria-expanded="true"\]:not\(:disabled\):not\(\.den-quiet-icon-btn\)\s*\{[\s\S]*?background:\s*var\(--den-selection-hover\)[\s\S]*?box-shadow:\s*var\(--den-bevel-inset\)/,
    );
  });

  it("leaves a quiet icon control to paint its own open state", () => {
    // This rule is unlayered, so without the exclusion it buries the recipe's
    // open state no matter how specific that recipe is.
    const recipe = read("styling/recipes/buttons-utilities.css");
    expect(recipe).toMatch(
      /@utility den-quiet-icon-btn\s*\{[\s\S]*?&:is\(\[aria-pressed="true"\], \[aria-expanded="true"\]\)/,
    );
  });

  it("is stamped by shared chrome entry points", () => {
    const sources = [
      read("components/shell/ChromeCloseButton.tsx"),
      read("components/shell/ChromeDragSurface.tsx"),
      read("components/settings/SettingsEditorTitle.tsx"),
      read("components/settings/UnderlineTabs.tsx"),
      read("components/shell/Shell.tsx"),
      read("components/nav/TabBar.tsx"),
      read("components/shell/ContextDrawer.tsx"),
      read("components/browse/BrowseChrome.tsx"),
      read("components/list/ListColumnHeader.tsx"),
      read("components/ContextMenu.tsx"),
    ];
    for (const src of sources) {
      expect(src).toMatch(/chromeProps\s*\(|<AnchoredSurface[\s\S]*\bchrome\b/);
    }
    expect(read("components/settings/SettingsStagePanel.tsx")).toMatch(
      /ChromeCloseButton/,
    );
  });
});
