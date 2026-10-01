import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const denRoot = join(import.meta.dirname, "..", "..");
const patchPath = "patches/@codemirror+view@6.43.6-scroll-and-wrap-heights.patch";

describe("CodeMirror view patch", () => {
  it("registers the scroll and wrapped-height patch", () => {
    const packageJson = JSON.parse(
      readFileSync(join(denRoot, "package.json"), "utf8"),
    ) as { patchedDependencies?: Record<string, string> };

    expect(
      packageJson.patchedDependencies?.["@codemirror/view@6.43.6"],
    ).toBe(patchPath);
  });

  it("measures once per scroll offset", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // The native scroll event after a pre-paint notification measures nothing new.
    expect(
      patch.match(
        /e\.target == scrollDOM && this\.measuredScrollTop == scrollDOM\.scrollTop/g,
      ),
    ).toHaveLength(2);
    expect(
      patch.match(/this\.measuredScrollLeft = scrollDOM\.scrollLeft;/g),
    ).toHaveLength(2);
  });

  it("narrows the viewport margin only for scrollbar jumps, and leans it with the measured delta", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // Only a marked scrollbar jump renders a narrow lookahead; other motion widens with speed and leans toward it.
    expect(patch.match(/this\.scrollJump && Math\.abs\(delta\) > \(visibleBottom - visibleTop\) \/ 2 \? 100 \/\* VP\.JumpMargin \*\//g)).toHaveLength(2);
    expect(patch.match(/Math\.abs\(delta\) \* 9 \/\* VP\.MarginFrames \*\//g)).toHaveLength(2);
    expect(patch.match(/\(bias \|\| delta\) \/ margin/g)).toHaveLength(2);
    // The delta and the jump mark belong to one measure pass.
    expect(patch.match(/\+\s*this\.scrollDelta = dTop;/g)).toHaveLength(2);
    expect(patch.match(/\+\s*this\.scrollJump = false;/g)).toHaveLength(6);
    expect(patch).toContain("noteScrollJump(): void;");
  });

  it("rests with both margins once scrolling settles and reports measure timing", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    expect(patch.match(/settled && !this\.viewportRests\(this\.viewport\)/g)).toHaveLength(2);
    expect(patch.match(/EditorView\.measureTiming = measureTiming;/g)).toHaveLength(2);
    expect(patch).toContain("noteScrollSettled(): void;");
    expect(patch).toContain("static measureTiming: Facet<(ms: number, viewportChanged: boolean) => void>;");
  });

  it("applies scroll-anchor corrections relative to the scroller's current offset", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // A relative scroll keeps travel an asynchronous scrolling thread made after the measure read the offset.
    expect(patch.match(/\+\s*scroll\.scrollBy\(0, diff\);/g)).toHaveLength(2);
    expect(patch.match(/-\s*scroll\.scrollTop \+= diff;/g)).toHaveLength(2);
  });

  it("keeps gutter cells with their lines and detaches only a full rebuild", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    expect(patch.match(/cell && cell\.from == block\.from && cell\.widget == widget/g)).toHaveLength(2);
    expect(patch.match(/: doc\.lineAt\(update\.changes\.mapPos\(elt\.from, 1\)\)\.from;/g)).toHaveLength(2);
    expect(patch.match(/this\.syncGutters\(Math\.min\(vpA\.to, vpB\.to\) <= Math\.max\(vpA\.from, vpB\.from\)\);/g)).toHaveLength(2);
  });

  it("swaps while the rendered lead still covers three frames of travel", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // The cover budget is frames of motion, not a fixed strip, and the swap runs before the lead is exhausted.
    expect(patch.match(/\* 3 \/\* VP\.CoverFrames \*\//g)).toHaveLength(2);
    expect(patch.match(/viewportIsAppropriate\(this\.viewport, lead\)/g)).toHaveLength(2);
    expect(patch.match(/600 \/\* VP\.MaxCoverMargin \*\//g)).toHaveLength(4);
  });

  it("measures synchronously at most once per animation frame", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // Later scroll events in the same frame share the next frame's measurement.
    expect(patch.match(/\+\s*if \(this\.syncMeasureFrame\) \{/g)).toHaveLength(2);
    expect(patch.match(/requestAnimationFrame\(\(\) => \{ this\.syncMeasureFrame = false; \}\);/g)).toHaveLength(2);
  });

  it("keeps print mode reading the whole document", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // Only printing widens the pixel viewport; every editor otherwise renders its range.
    expect(patch.match(/this\.printing \? fullPixelRange : visiblePixelRange/g)).toHaveLength(2);
    expect(patch).not.toContain("renderWholeDocument");
  });

  it("allows suppressing empty cursors and disables cursor blinking when blink rate is non-positive", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    // drawCursor facet option suppresses empty cursors in both index.cjs and index.js
    expect(patch.match(/drawCursor: true,/g)).toHaveLength(2);
    expect(patch.match(/conf\.drawCursor === false/g)).toHaveLength(2);
    expect(patch).toContain("drawCursor?: boolean;");

    // Zero or negative cursorBlinkRate disables blink animation
    expect(patch.match(/dom\.style\.animationName = "none";/g)).toHaveLength(4);
  });

  it("clamps cursor height to defaultLineHeight over tall block widgets", () => {
    const patch = readFileSync(join(denRoot, patchPath), "utf8");

    expect(
      patch.match(/if \(view\.defaultLineHeight && height > view\.defaultLineHeight \* 1\.5\)/g),
    ).toHaveLength(2);
    expect(
      patch.match(/height = view\.defaultLineHeight;/g),
    ).toHaveLength(2);
  });
});
