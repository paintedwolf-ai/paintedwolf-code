// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { readSourceText } from "../test/stylesheet-source.ts";
import { denSourceRoot } from "../test/source-corpus.ts";

import { SPLIT_MIN_HOST, resolveStageColumn } from "../shell/stage-placement.ts";

const read = (rel: string) => readSourceText(join(denSourceRoot, rel));

describe("split responsive authority", () => {
  it("resolves the stage column without any measured width", () => {
    const placement = read("shell/stage-placement.ts");
    const resolver =
      /export function resolveStageColumn\([\s\S]*?\n\}/.exec(placement)?.[0] ?? "";
    expect(resolver).not.toBe("");
    expect(resolver).not.toMatch(/hostWidth|WidthPx/);
    expect(read("shell/workspace-layout.ts")).not.toMatch(/isSplitAvailable/);
  });

  it("lets the split divider claim the viewport width as the sidebar yields", () => {
    expect(read("components/shell/shell-stage-placement.ts")).toMatch(/widenWindowBy\(stageChromeDeficitPx\(/);
    expect(read("components/shell/Shell.tsx")).toMatch(/availableWidthPx=\{layoutViewportWidthPx\}/);
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
