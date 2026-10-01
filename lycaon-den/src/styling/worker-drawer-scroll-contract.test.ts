import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { extractCssRuleBlock } from "./css-contract.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

function readText(rel: string): string {
  return readFileSync(join(denSrc, rel), "utf8");
}

describe("worker drawer scroll clearance", () => {
  const drawerUtils = readText("drawer-utilities.css");
  const drawer = readText("components/shell/ContextDrawer.tsx");
  const workerUtils = readText("worker-utilities.css");
  const workerDomain = readText("worker-domain.css");

  it("keeps the absolute drawer from growing past its stage insets", () => {
    expect(extractCssRuleBlock(drawerUtils, ".den-context-drawer")).toMatch(
      /min-height:\s*0/,
    );
  });

  it("scrolls the sections in a scrollport frame, so the thumb never rides the scrolled content", () => {
    expect(drawer).toMatch(/<Scrollport\s+class="den-context-drawer-scroll"/);
    expect(extractCssRuleBlock(drawerUtils, "@utility den-context-drawer-scroll")).not.toMatch(/overflow/);
  });

  it("lets the last section scroll into its slot so the drawer ends with every header stacked", () => {
    expect(extractCssRuleBlock(workerUtils, ".den-worker-transcript-pane")).not.toMatch(/padding-bottom/);
    expect(
      extractCssRuleBlock(workerDomain, ".den-worker-transcript-section:last-child .den-worker-transcript-section-body"),
    ).toMatch(/min-height:\s*max\(0px,\s*calc\(var\(--worker-reading-h,\s*0px\)\s*-\s*var\(--worker-sticky-stack\)\)\)/);
    expect(workerDomain).toMatch(
      /\.den-worker-transcript-pane:has\(>\s*\.den-worker-transcript-section:nth-child\(4\)\)\s*\{[\s\S]*?--worker-sticky-stack:\s*calc\(\s*4 \* var\(--worker-header-h\)\s*-\s*3px\s*\)/,
    );
  });
});
