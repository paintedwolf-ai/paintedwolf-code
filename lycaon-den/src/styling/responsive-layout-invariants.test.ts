// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { readSourceText } from "../test/stylesheet-source.ts";
import { denRoot, denSourceRoot } from "../test/source-corpus.ts";

import { CHAT_COL_MIN, CHAT_WIDTH_DEFAULT_PX, DIVIDER_PX, SPLIT_MIN_HOST, STAGE_COL_MIN, clampChatWidthPx } from "../shell/stage-placement.ts";
import { shouldAutomaticallyCollapseNav, workspaceStageFloorPx } from "../shell/responsive-collapse.ts";

const read = (rel: string) => readSourceText(join(denSourceRoot, rel));

const readDenFile = (rel: string) => readSourceText(join(denRoot, rel));

describe("ordered responsive sidebars", () => {
  const navWidth = 280;
  const chatWidth = CHAT_WIDTH_DEFAULT_PX;
  const columnsAt = (host: number) => {
    const chat = clampChatWidthPx(chatWidth, host);
    return { chat, stage: host - DIVIDER_PX - chat };
  };

  it("yields the app nav, then the conversation, then the whole split", () => {
    const appNavCollapseViewport =
      navWidth + STAGE_COL_MIN + chatWidth + DIVIDER_PX;
    const chatNarrowsHost = STAGE_COL_MIN + chatWidth + DIVIDER_PX;
    const split = {
      automaticCollapsed: false,
      navWidthPx: navWidth,
      stageMinWidthPx: STAGE_COL_MIN,
      splitColumns: true,
      chatWidthPx: chatWidth,
    };

    expect(
      shouldAutomaticallyCollapseNav({
        ...split,
        viewportWidthPx: appNavCollapseViewport - 1,
      }),
    ).toBe(true);
    expect(
      shouldAutomaticallyCollapseNav({
        ...split,
        viewportWidthPx: appNavCollapseViewport,
      }),
    ).toBe(false);
    expect(chatNarrowsHost).toBeLessThan(appNavCollapseViewport);
    expect(chatNarrowsHost).toBeGreaterThan(SPLIT_MIN_HOST);

    expect(columnsAt(chatNarrowsHost)).toEqual({
      chat: chatWidth,
      stage: STAGE_COL_MIN,
    });
    expect(columnsAt(chatNarrowsHost - 1).chat).toBe(chatWidth - 1);
    expect(columnsAt(SPLIT_MIN_HOST).chat).toBe(CHAT_COL_MIN);
  });

  it("never folds a pane the next step of the same resize gives back", () => {
    // Stage and split floors share the navigator’s minimum width.
    expect(workspaceStageFloorPx({ splitLive: true, stageId: "files" })).toBe(
      STAGE_COL_MIN,
    );
    expect(workspaceStageFloorPx({ splitLive: true, stageId: "search" })).toBe(
      STAGE_COL_MIN,
    );
    const treeFolds = (stageWidth: number) => stageWidth < STAGE_COL_MIN;
    for (let host = SPLIT_MIN_HOST; host <= SPLIT_MIN_HOST + 600; host += 1) {
      expect(treeFolds(columnsAt(host).stage)).toBe(false);
    }
    // Collapse preserves the surviving column's minimum width.
    expect(SPLIT_MIN_HOST - 1).toBeGreaterThanOrEqual(STAGE_COL_MIN);
  });

  it("keeps both split columns above the native window minimum", () => {
    const configs = [
      "src-tauri/tauri.conf.json",
      "src-tauri/tauri.macos.conf.json",
      "src-tauri/tauri.linux.conf.json",
    ];
    for (const config of configs) {
      const parsed = JSON.parse(readDenFile(config)) as {
        app: { windows: Array<{ minWidth: number }> };
      };
      expect(SPLIT_MIN_HOST).toBeGreaterThan(parsed.app.windows[0]!.minWidth);
    }
  });

  it("binds Files auto-collapse to the live stage column", () => {
    const filesCss = read("files-domain.css");
    expect(read("components/shell/StageSplitHost.tsx")).toMatch(
      /bindLayoutBand\(el, LAYOUT_BAND_SCALES\.stageColumn\)[\s\S]{0,200}?den-split-col--stage/,
    );
    expect(filesCss).toContain(
      `:where(.den-split-col--stage[data-stage-band~="max-${STAGE_COL_MIN - 1}"])`,
    );
    expect(filesCss).toMatch(
      /project-files-view:not\(\.project-files-view--tree-collapsed\):not\([\s\S]*?\.project-files-view--tree-claimed[\s\S]*?den-files-tree-pane[\s\S]*?display:\s*none/,
    );
    expect(filesCss).toMatch(
      /project-files-view:not\(\.project-files-view--tree-collapsed\):not\([\s\S]*?\.project-files-view--tree-claimed[\s\S]*?den-files-split[\s\S]*?display:\s*none/,
    );
  });

  it("compacts the Files pane switch against the resizable tree width", () => {
    const filesCss = read("files-domain.css");
    expect(read("files/components/FilesTreePane.tsx")).toContain("bindLayoutBand(el, LAYOUT_BAND_SCALES.filesTree)");
    expect(filesCss).toMatch(
      /^\.den-files-pane-segment\.den-browse-segmented\s*\{[^}]*flex-wrap:\s*nowrap/m,
    );
    // History controls and the smaller mode switch use the same width band.
    expect(filesCss).toMatch(
      /\[data-tree-band~="compact"\]\) \.den-files-history-controls\s*\{[^}]*display:\s*none/,
    );
    expect(filesCss).toMatch(
      /\[data-tree-band~="compact"\]\) \.den-files-pane-segment\s*\{[^}]*padding:\s*2px/,
    );
    expect(filesCss).toMatch(
      /\[data-tree-band~="compact"\]\) \.den-files-pane-segment \.den-browse-segment\s*\{[^}]*font-size:\s*var\(--text-den-compact\)/,
    );
  });

  it("hands the collapsed app-nav state to chat when split leaves only chat", () => {
    const shellCss = read("shell-domain.css");
    expect(shellCss).toContain(`[data-host-band~="min-${SPLIT_MIN_HOST}"]`);
    expect(shellCss).toMatch(/den-nav-split-fallback[\s\S]*?display:\s*none/);
  });
});
