// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { readSourceText } from "../test/stylesheet-source.ts";
import { denSourceRoot } from "../test/source-corpus.ts";

import { SPLIT_MIN_HOST } from "../shell/stage-placement.ts";

const read = (rel: string) => readSourceText(join(denSourceRoot, rel));

describe("split responsive authority", () => {
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
});
