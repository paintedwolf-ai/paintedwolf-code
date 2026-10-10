// @vitest-environment jsdom
import { readSourceText } from "../test/stylesheet-source.ts";
import { describe, expect, it } from "vitest";
import {
  CHAT_COL_MIN,
  CHAT_WIDTH_DEFAULT_PX,
  DIVIDER_PX,
  SPLIT_MIN_HOST,
  STAGE_COL_MIN,
  clampChatWidthPx,
  resolveStageColumn,
} from "../shell/stage-placement.ts";
import {
  shouldAutomaticallyCollapseNav,
  workspaceStageFloorPx,
} from "../shell/responsive-collapse.ts";
import { join } from "node:path";
import { extractCssRuleBlock } from "./css-contract.ts";
import { denRoot, denSourceRoot, loadSourceCorpus } from "../test/source-corpus.ts";

const corpus = loadSourceCorpus(denSourceRoot, {
  extensions: [".css", ".ts", ".tsx"],
});

function read(rel: string): string {
  // Stylesheets are import manifests; read them in cascade order.
  if (rel.endsWith(".css")) return readSourceText(join(denSourceRoot, rel));
  const file = corpus.file(rel);
  if (!file) throw new Error(`source corpus file not found: ${rel}`);
  return file.text;
}

/** Outside the src corpus, and named rather than scanned. */
function readDenFile(rel: string): string {
  return readSourceText(join(denRoot, rel), "utf8");
}

/** Custom properties registered `inherits: false` in global.css. */
function registeredNonInheriting(css: string): Set<string> {
  const found = new Set<string>();
  const re = /@property\s+(--[\w-]+)\s*\{([^}]*)\}/g;
  for (let m = re.exec(css); m; m = re.exec(css)) {
    if (/inherits:\s*false/.test(m[2]!)) found.add(m[1]!);
  }
  return found;
}

/** Finds px-valued layout properties written by components, as style entries or imperative writes. */
function pxValuedCustomProps(source: string): string[] {
  const found: string[] = [];
  const re = /"(--den-[\w-]+)":\s*(?:`[^`]*px`|[^,\n]*\?[^,\n]*"0px"[^,\n]*)|setProperty\("(--den-[\w-]+)",\s*`[^`]*px`\)/g;
  for (let m = re.exec(source); m; m = re.exec(source)) found.push((m[1] ?? m[2])!);
  return found;
}

describe("resize invalidation", () => {
  const globalCss = read("global.css");
  const registered = registeredNonInheriting(globalCss);

  it("registers every live layout-geometry property as non-inheriting", () => {
    // `inherits: false` only holds if every reader is the declaring element.
    expect(registered).toContain("--den-nav-slot-width");
    expect(registered).toContain("--den-split-chat-w");
    expect(registered).toContain("--den-detail-w");
    expect(registered).toContain("--den-detail-h");
    expect(registered).toContain("--den-list-cols");
    expect(registered).toContain("--den-editor-block-left");
    expect(registered).toContain("--den-editor-block-w");
  });

  it("keeps registrations on the universal syntax so var() fallbacks survive", () => {
    // A typed syntax needs `initial-value`, which disables `var()` fallbacks.
    const re = /@property\s+--den-[\w-]+\s*\{([^}]*)\}/g;
    for (let m = re.exec(globalCss); m; m = re.exec(globalCss)) {
      expect(m[1]).toMatch(/syntax:\s*"\*"/);
      expect(m[1]).not.toMatch(/initial-value/);
    }
  });

  it("declares no live geometry property that is not registered", () => {
    const unregistered: string[] = [];
    for (const file of corpus.files) {
      if (!/\.tsx?$/.test(file.rel) || /\.test\.tsx?$/.test(file.rel)) continue;
      for (const prop of pxValuedCustomProps(file.text)) {
        if (registered.has(prop)) continue;
        unregistered.push(`${file.rel} → ${prop}`);
      }
    }
    // Live geometry is registered and read on its declaring element.
    expect(unregistered).toEqual([]);
  });

  it("frames editor block chrome on the block that reads it", () => {
    // The frame is stamped per block, so the write never restyles the editor's lines.
    const widgets = read("components/source/editor/solid-block-widget.ts");
    expect(widgets).toMatch(/host\.style\.setProperty\("--den-editor-block-left"/);
    expect(widgets).toMatch(/host\.style\.setProperty\("--den-editor-block-w"/);
    const block = extractCssRuleBlock(read("components/source/reader/source-reader.css"), ".cm-den-block");
    expect(block).toMatch(/position:\s*sticky/);
    expect(block).toMatch(/left:\s*var\(--den-editor-block-left, 0px\)/);
    expect(block).toMatch(/width:\s*var\(--den-editor-block-w, 100%\)/);
  });

  it("declares tree geometry on the pane that reads it", () => {
    expect(registered).toContain("--den-files-tree-w");
    expect(read("files/components/ProjectFilesView.tsx")).toMatch(
      /<FilesTreePane[\s\S]*?style=\{treeStyle\(\)\}/,
    );
    expect(read("files/components/FilesTreePane.tsx")).toMatch(/class="den-files-tree-pane"[\s\S]*?style=\{props\.style\}/);
  });

  it("keeps the nav slot width off the shell root", () => {
    const shell = read("components/shell/Shell.tsx");
    const shellRoot = /class="den-shell"[\s\S]{0,400}?>/.exec(shell)?.[0] ?? "";
    expect(shellRoot).not.toContain("--den-nav-slot-width");
    // Both readers declare it themselves.
    expect(shell).toMatch(
      /class="den-shell-aside"\s*\n\s*style=\{\{ "--den-nav-slot-width"/,
    );
    expect(shell).toMatch(
      /class="den-find-bar-host"[\s\S]{0,160}?"--den-nav-slot-width"/,
    );
  });

  it("gives the stage whatever the conversation leaves of the live host box", () => {
    const shellCss = read("shell-domain.css");
    const split = extractCssRuleBlock(shellCss, ".den-shell-stage-host--split");

    // The live host width resolves the stage entirely in CSS.
    expect(split).toMatch(/--den-split-chat-w/);
    expect(split).not.toMatch(/--den-split-stage-width/);
    expect(split).toMatch(
      new RegExp(`--den-split-chat-min:\\s*${CHAT_COL_MIN}px`),
    );
    expect(split).toMatch(
      new RegExp(`--den-split-stage-min:\\s*${STAGE_COL_MIN}px`),
    );

    // Both floors, and the collapse below the split minimum, live in the track.
    expect(split).toMatch(/clamp\(\s*var\(--den-split-chat-min\)/);
    expect(split).toMatch(
      /calc\(100% - var\(--den-split-stage-min\) - var\(--den-split-divider-px\)\)/,
    );
    expect(split).toMatch(
      new RegExp(`max\\(0px, calc\\(\\(100% - ${SPLIT_MIN_HOST - 1}px\\) \\* 1000\\)\\)`),
    );

    // Both topologies size the conversation from the same track; the stage flexes.
    expect(split).toMatch(
      /grid-template-columns:\s*minmax\(0, 1fr\) var\(--den-split-divider-px\)\s*var\(--den-split-chat-track\)/,
    );
    const mirrored = extractCssRuleBlock(
      shellCss,
      '.den-shell-stage-host--split[data-stage-leading="false"]',
    );
    expect(mirrored).toMatch(
      /grid-template-columns:\s*var\(--den-split-chat-track\) var\(--den-split-divider-px\)\s*minmax\(0, 1fr\)/,
    );
  });

  it("stops rendering the stage column and divider below the split minimum", () => {
    // Hidden stages skip layout work.
    const shellCss = read("shell-domain.css");
    const query = new RegExp(
      `\\[data-host-band~="max-${SPLIT_MIN_HOST - 1}"\\][\\s\\S]*?display: none;`,
    );
    expect(shellCss).toMatch(query);
  });

  it("sizes the split divider track from the same constant as the column math", () => {
    // The grid host controls the divider-width variable.
    const split = extractCssRuleBlock(
      read("shell-domain.css"),
      ".den-shell-stage-host--split",
    );
    expect(split).toMatch(
      new RegExp(`--den-split-divider-px:\\s*${DIVIDER_PX}px`),
    );
    expect(split).not.toMatch(/var\(--den-split-divider-px,/);
    expect(read("components/shell/SplitDivider.tsx")).not.toContain(
      '"--den-split-divider-px"',
    );
  });

  it("keeps resize hit areas independent of pane widths", () => {
    const shellCss = read("shell-domain.css");
    const nav = extractCssRuleBlock(shellCss, ".den-shell-nav-resize");
    const splitTarget = extractCssRuleBlock(
      shellCss,
      ".den-split-divider::before",
    );
    const files = extractCssRuleBlock(
      read("files-domain.css"),
      ".den-files-split",
    );

    expect(nav).toMatch(/width:\s*4px/);
    expect(splitTarget).toMatch(/inset:\s*0 -2px/);
    expect(files).toMatch(/width:\s*4px/);
    expect(files).toMatch(/margin-inline:\s*-2px/);
  });

  it("leaves the boot splash name sized by the live viewport", () => {
    const html = readDenFile("index.html");
    const name = extractCssRuleBlock(
      html,
      "#den-boot-fallback .den-boot-fallback-name",
    );

    // The name is nowrap: a size written back in px holds the launch geometry
    // and overruns the window once it narrows.
    expect(name).toMatch(/font-size:\s*clamp\(40px,\s*10vw,\s*120px\)/);
    expect(html).not.toMatch(/style\.fontSize\s*=/);
  });

  it("truncates the peer window label rather than running it off the edge", () => {
    const html = readDenFile("index.html");
    const label = extractCssRuleBlock(
      html,
      "body.den-peer-boot #den-boot-fallback .den-boot-fallback-name",
    );

    // A file name has no length bound, and the rule it inherits is nowrap.
    expect(label).toMatch(/overflow:\s*hidden/);
    expect(label).toMatch(/text-overflow:\s*ellipsis/);
    // Truncation is visual only; the reader still gets the whole label.
    expect(html).toMatch(/setAttribute\("aria-label", peerLabel\)/);
  });
});

describe("resize measurement", () => {
  it("measures pane containers through an observer, not on demand", () => {
    // Pointer handlers avoid forced layout reads.
    for (const rel of [
      "components/browse/BrowseStagePanel.tsx",
      "files/components/ProjectFilesView.tsx",
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

describe("split responsive authority", () => {
  it("resolves the stage column without any measured width", () => {
    const placement = read("shell/stage-placement.ts");
    const resolver =
      /export function resolveStageColumn\([\s\S]*?\n\}/.exec(placement)?.[0] ?? "";
    expect(resolver).not.toBe("");
    expect(resolver).not.toMatch(/hostWidth|WidthPx/);
    expect(read("shell/layout-store.ts")).not.toMatch(/isSplitAvailable/);
  });

  it("lets the split divider claim the viewport width as the sidebar yields", () => {
    expect(read("components/shell/shell-stage-placement.ts")).toMatch(/widenWindowBy\(stageChromeDeficitPx\(/);
    expect(read("components/shell/Shell.tsx")).toMatch(/availableWidthPx=\{layoutViewportWidthPx\}/);
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

describe("split collapse survivor", () => {
  const shellCss = () => read("shell-domain.css");
  const escape = (literal: string) =>
    literal.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const narrowBand = escape(`[data-host-band~="max-${SPLIT_MIN_HOST - 1}"]`);
  const wideBand = escape(`[data-host-band~="min-${SPLIT_MIN_HOST}"]`);

  /** Each survivor drops the column the other one keeps. */
  const collapseRule = (survivor: "conversation" | "stage", column: string) =>
    new RegExp(
      `:where\\(\\s*${narrowBand}\\[data-narrow-survivor="${survivor}"\\]\\s*\\)` +
        `[\\s\\S]{0,120}?\\.den-split-col--${column} \\{\\s*display: none;`,
    );

  it("keeps one column at the band, and which one is a preference", () => {
    expect(shellCss()).toMatch(collapseRule("conversation", "stage"));
    expect(shellCss()).toMatch(collapseRule("stage", "chat"));
    // Stage-only layouts share the same tracks.
    expect(shellCss()).toMatch(
      /\[data-narrow-survivor="stage"\][\s\S]{0,160}?grid-template-columns: minmax\(0, 1fr\) 0 0;/,
    );
  });

  it("leaves explicitly hidden panes out of the band's reach", () => {
    // Width bands preserve either explicit hide.
    for (const [survivor, column] of [
      ["conversation", "stage"],
      ["stage", "chat"],
    ] as const) {
      const rule = shellCss().match(collapseRule(survivor, column))?.[0] ?? "";
      expect(rule).toContain(":not([data-conversation-hidden], [data-context-hidden])");
    }
  });

  it("measures when the band is crossed and declares which column survives", () => {
    // The observed content box sets the threshold; a stable attribute selects the survivor.
    expect(shellCss()).not.toMatch(/data-split-collapse/);
    for (const file of [
      "components/shell/StageSplitHost.tsx",
      "components/shell/Shell.tsx",
      "shell/stage-placement.ts",
      "components/nav/ChatTabRail.tsx",
      "components/nav/StageEdgeControls.tsx",
    ]) {
      expect(read(file)).not.toMatch(
        /collapseTarget|data-split-collapse|data-host-band/,
      );
    }
    expect(read("components/shell/StageSplitHost.tsx")).toContain(
      "data-narrow-survivor={props.splitLive ? props.narrowSurvivor : undefined}",
    );
    // One place compares a width to the seam; the shell passes it a width.
    expect(read("components/shell/Shell.tsx")).not.toMatch(/SPLIT_MIN_HOST/);
    expect(read("shell/stage-placement.ts")).toContain("splitColumnsOnScreen");
    const bands = read("layout/layout-bands.ts");
    expect(bands).toContain("observeSharedContentBox");
    expect(bands).not.toMatch(/innerWidth|getBoundingClientRect|clientWidth/);
  });

  it("hands the window's leading edge to whichever column the band keeps", () => {
    // The surviving stage inherits clearance for the window controls.
    expect(shellCss()).toMatch(
      new RegExp(
        `${narrowBand}\\[data-narrow-survivor="stage"\\]\\s*\\)` +
          `[\\s\\S]{0,200}?\\[data-stage-leading="false"\\]` +
          `[\\s\\S]{0,200}?padding-left: var\\(--den-traffic-light-clearance`,
      ),
    );
  });

  it("answers a refused width claim with motion that reduced motion drops", () => {
    // One directional push, and nothing where motion is reduced.
    const utilities = read("shell-utilities.css");
    expect(read("shell-domain.css")).toMatch(
      /@keyframes den-fit-refused \{[\s\S]{0,120}?translateX\(var\(--den-fit-refused-shift\)\)/,
    );
    expect(utilities).toMatch(
      /\.den-split-fit-restore\[data-refused\] \{[\s\S]{0,160}?animation: den-fit-refused/,
    );
    expect(utilities).toMatch(
      /\.den-split-fit-restore\[data-refused="left"\] \{\s*--den-fit-refused-shift: -/,
    );
    expect(utilities).toMatch(
      /@media \(prefers-reduced-motion: reduce\) \{\s*\.den-split-fit-restore\[data-refused\] \{\s*animation: none;/,
    );
  });

  it("swaps the seam's controls at the same band, without a second source", () => {
    // The survivor band also selects the visible control.
    expect(shellCss()).toMatch(
      new RegExp(
        `:where\\(${narrowBand}\\)\\s* :is\\(\\.den-pane-toggle--hide-conversation, \\.den-pane-toggle--hide-context\\),` +
          `\\s*[^{]*?:where\\(${wideBand}\\)\\s*\\.den-split-fit-restore \\{\\s*display: none;`,
      ),
    );
    expect(read("components/nav/ChatTabRail.tsx")).toContain(
      "SplitFitRestoreButton",
    );
    expect(read("components/nav/StageEdgeControls.tsx")).toContain(
      "SplitFitRestoreButton",
    );
  });

  it("resolves the stage column without naming a survivor at all", () => {
    const base = {
      foregroundIsChat: false,
      hasChat: true,
      companion: null,
      splitMode: true,
    } as const;
    // Navigation changes stage occupancy, not the split survivor.
    expect(resolveStageColumn({ ...base, navStage: "files" })).toEqual({
      splitLive: true,
      stageId: "files",
    });
    expect(
      resolveStageColumn({
        ...base,
        navStage: null,
        foregroundIsChat: true,
        companion: "files",
      }),
    ).toEqual({ splitLive: true, stageId: "files" });
  });
});

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
