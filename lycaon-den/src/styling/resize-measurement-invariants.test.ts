// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { readSourceText } from "../test/stylesheet-source.ts";
import { denSourceRoot } from "../test/source-corpus.ts";

import { extractCssRuleBlock } from "./css-contract.ts";

const read = (rel: string) => readSourceText(join(denSourceRoot, rel));

describe("resize measurement", () => {
  it("measures pane containers through an observer, not on demand", () => {
    // Pointer handlers avoid forced layout reads.
    for (const rel of [
      "components/browse/BrowseStagePanel.tsx",
      "files/tree/files-tree-layout.ts",
    ]) {
      const source = read(rel);
      expect(source).toContain("observeElementExtent");
      expect(source).not.toMatch(/const bodySize[\s\S]{0,200}?clientWidth/);
    }
  });

  it("does not re-measure the split host through a forced layout read", () => {
    const shell = read("components/shell/Shell.tsx");
    const effect =
      /createEffect\(\(\) => \{\s*const el = stageHostEl\(\);[\s\S]*?\n  \}\);/.exec(
        shell,
      )?.[0] ?? "";
    expect(effect).not.toBe("");
    expect(effect).toContain("ResizeObserver");
    expect(effect).not.toContain("getBoundingClientRect");
    expect(effect).not.toContain("splitHostWidthPx()");
  });

  it("syncs resize-handle axis from the handle, not the window", () => {
    const handle = read("components/primitives/ResizeHandle.tsx");
    // ResizeObserver controls axis synchronization.
    expect(handle).not.toMatch(/addEventListener\("resize"/);
    expect(handle).toContain("new ResizeObserver(syncAxis)");
  });


});

describe("layout epoch", () => {
  it("does not publish viewport width from the window resize handler", () => {
    const sinks = read("components/shell/shell-navigation-sinks.ts");
    expect(sinks).toContain("installWindowResizeEpoch");
    expect(sinks).not.toMatch(/setLayoutViewportWidth/);
    expect(read("components/shell/Shell.tsx")).not.toMatch(/setLayoutViewportWidth/);
    const epoch = read("shell/window-resize.ts");
    const onResize =
      /const onResize = \(\) => \{[\s\S]*?\n  \};/.exec(epoch)?.[0] ?? "";
    expect(onResize).toContain("sustainShellLayoutBusy");
    expect(onResize).toContain("requestAnimationFrame");
    expect(onResize).not.toContain("setLayoutViewportWidth");
  });

  it("publishes split-host width on the next frame while resizing", () => {
    const shell = read("components/shell/Shell.tsx");
    const effect =
      /createEffect\(\(\) => \{\s*const el = stageHostEl\(\);[\s\S]*?\n  \}\);/.exec(
        shell,
      )?.[0] ?? "";
    expect(effect).toContain("schedulePublish(width)");
    expect(effect).toContain("requestAnimationFrame");
    expect(effect).not.toContain("isShellLayoutBusy");
    expect(effect).not.toContain("onShellLayoutSettled");
  });

  it("isolates column style and layout without paint containment", () => {
    const split = extractCssRuleBlock(read("shell-domain.css"), ".den-split-col");
    expect(split).toMatch(/contain:\s*layout style/);
    expect(split).not.toMatch(/contain:\s*[^;]*paint/);
  });

  it("isolates the editor scroller from document layout and scroll anchoring", () => {
    const theme = read("components/source/editor/editor-theme-rules.ts");
    expect(theme).toMatch(/contain:\s*"layout style"/);
    expect(theme).toMatch(/overflowAnchor:\s*"none"/);
    expect(theme).toMatch(/overscrollBehavior:\s*"none"/);
    expect(theme).toMatch(
      /\.cm-scroller": \{[\s\S]*?backgroundColor:\s*"var\(--den-background\)"/,
    );
    const scrollbars = read("components/source/editor/codemirror-scrollbars.ts");
    expect(scrollbars).toContain("attachThemedViewportScrollbar");
    expect(scrollbars).toMatch(
      /if \(!update\.docChanged && !update\.geometryChanged\) return/,
    );
    expect(scrollbars).toContain("this.view.requestMeasure");
    expect(scrollbars).toMatch(/read:\s*\(view\) =>\s*\{[\s\S]*?scrollbarChrome\.read\(view\.dom\)[\s\S]*?view\.contentHeight/);
    expect(scrollbars).toMatch(/write:\s*\(measurement\) =>[\s\S]*?updateThemedViewportScrollbar\(this\.view\.dom, measurement\.signature, measurement\.geometry\)/);
    expect(scrollbars).not.toContain("new ResizeObserver");
    expect(read("platform/themed-scrollbars.css")).not.toContain("noContent");
    const ruler = read("components/source/annotations/overview-ruler.ts");
    expect(ruler).toContain("requestIdleCallback");
    const host = extractCssRuleBlock(
      read("files-domain.css"),
      ".den-files-editor__host-wrap",
    );
    expect(host).toMatch(/contain:\s*size layout style/);
    expect(host).not.toMatch(/contain:\s*[^;]*paint/);
    expect(read("files-utilities.css")).toMatch(
      /@utility den-files-tree-scroll \{[\s\S]*?overflow-anchor:\s*none[\s\S]*?overscroll-behavior:\s*none/,
    );
    const fileUtilities = read("files-utilities.css");
    expect(fileUtilities).toMatch(
      /@utility project-files-stage-boundary \{[\s\S]*?overflow:\s*hidden/,
    );
    expect(fileUtilities).toMatch(
      /@utility project-files-stage-boundary__content \{[\s\S]*?overflow:\s*hidden/,
    );
    const files = read("files-domain.css");
    expect(files).not.toMatch(
      /\.den-shell-main:has\(\.project-files-view\)[\s\S]*?overflow:\s*visible/,
    );

  });

  it("measures the transcript from the shared content box", () => {
    const transcript = read("chat/transcript/layout/transcript-geometry.ts");
    expect(transcript).toContain("observeSharedContentBox");
    expect(transcript).not.toMatch(
      /observeSharedContentBox\([\s\S]{0,200}?clientWidth/,
    );
    expect(read("chat/transcript/layout/transcript-virtualizer.ts")).toContain(
      "observeTranscriptViewportRect",
    );
  });
});
