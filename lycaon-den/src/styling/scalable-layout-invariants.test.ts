import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { MAX_TEXT_SCALE } from "../platform/desktop/accessibility-text-size.ts";
import { CHAT_COL_MIN } from "../shell/stage-placement.ts";
import { DEFAULT_TRANSCRIPT_SPACING, TRANSCRIPT_SPACING } from "../chat/transcript/layout/transcript-spacing.ts";

const denSrc = join(import.meta.dirname, "..");

function read(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

describe("scalable layout chrome", () => {
  it("carries every transcript seam on one rung set, in rem", () => {
    const chat = read("chat-utilities.css");
    const row = chat.match(/\.den-chat-stream-inner \.transcript-viewport-row\s*\{([^}]+)\}/)?.[1];
    // The row spaces its own parts; the seam above it belongs to the seam rule.
    expect(row).toMatch(/gap:\s*var\(--transcript-part-gap\)/);
    expect(row).not.toMatch(/margin/);
    expect(chat).toMatch(/\.den-seam\s*\{[^}]*box-sizing:\s*border-box/);
    for (const rung of ["row", "section", "turn"]) {
      expect(chat, rung).toMatch(
        new RegExp(`\\.den-seam\\[data-seam="${rung}"\\]\\s*\\{\\s*padding-top:\\s*var\\(--transcript-${rung}-gap\\)`),
      );
    }
    // A px seam would freeze at scale 1 while the text around it grew.
    for (const rung of ["partGap", "rowGap", "sectionGap", "turnGap"] as const) {
      expect(TRANSCRIPT_SPACING[rung].unit, rung).toBe("rem");
    }
    expect(chat.match(/@utility den-chat-transcript-visual\s*\{([^}]+)\}/)?.[1]).not.toMatch(/(?:^|[;\s])gap:/);
  });

  it("leaves the worker drawer's seams to the rows, so both its layouts agree", () => {
    const workers = read("worker-utilities.css");
    // VirtualCardList stacks large lists absolutely, where a container gap reaches nothing.
    expect(workers).toMatch(/@utility den-worker-transcript \{[^}]*\}/);
    expect(workers.match(/@utility den-worker-transcript \{([^}]+)\}/)?.[1])
      .not.toMatch(/(?:^|[;\s])gap:/);
  });

  it("spaces stray elements outside the rung set", () => {
    const chat = read("chat-utilities.css");
    const components = read("global-components.css");
    expect(chat.match(/@utility den-workflow-boundary \{([^}]+)\}/)?.[1]).not.toMatch(/margin/);
    expect(components.match(/\.den-turn-tail \{([^}]+)\}/)?.[1]).toMatch(/margin:\s*0/);
    expect(components.match(/\.den-turn-walk \{([^}]+)\}/)?.[1]).not.toMatch(/margin/);
    expect(components.match(/\.den-transcript-day \{([^}]+)\}/)?.[1]).not.toMatch(/padding-top/);
  });

  it("locks max harness inject to AccessibilityXXXL scale", () => {
    expect(MAX_TEXT_SCALE).toBeCloseTo(53 / 17);
  });

  it("composer and nav use em floors / max(24px, …) hit targets", () => {
    const components = read("global-components.css");
    const globalCss = read("global.css");
    const chat = read("chat-utilities.css");
    const shell = read("shell-utilities.css");
    const tailwind = read("tailwind.css");

    expect(globalCss).toMatch(/--den-composer-input-min-height:\s*4em/);
    // Row actions keep a 24px hit target and grow with the text above it.
    expect(tailwind).toMatch(
      /@utility den-row-action\s*\{[\s\S]*?height:\s*max\(1\.7143rem,\s*1\.5em\)/,
    );
    expect(chat).toMatch(/--den-composer-action-col:\s*var\(--den-icon-target\)/);
    expect(read("styling/recipes/buttons-utilities.css")).toMatch(
      /@utility den-quiet-icon-btn\s*\{[\s\S]*?width:\s*var\(--den-icon-target\)/,
    );
    expect(chat).toMatch(/scroll-padding-bottom:\s*calc\(/);
    expect(chat).not.toMatch(
      /scroll-padding-bottom:[\s\S]*var\(--den-composer-dock-height/,
    );
    expect(chat).toMatch(
      /scroll-padding-bottom:\s*calc\(var\(--chat-stream-edge-fade\)/,
    );
    // The stream is its own scrollport, so reveal clearance lands on it directly.
    expect(chat).toMatch(
      /scroll-padding-top:\s*calc\(var\(--chat-stream-edge-fade\)/,
    );
    expect(chat).not.toMatch(/\[data-overlayscrollbars-viewport\]/);
    expect(chat).toMatch(
      /@utility den-chat-stream\s*\{[\s\S]*?overflow-anchor:\s*none;/,
    );
    expect(globalCss).toMatch(/--chat-stream-edge-fade:\s*20px/);
    expect(globalCss).toMatch(/--chat-stream-edge-solid-bottom:\s*10px/);
    // Clearance derives from the band so a band change carries it along.
    expect(globalCss).toMatch(
      /--chat-stream-tail-clearance:\s*calc\(var\(--chat-stream-edge-fade\)\s*\+/,
    );
    expect(chat).toMatch(
      /@utility den-chat-stream-body\s*\{[\s\S]*?padding-bottom:\s*var\(--chat-stream-tail-clearance\)/,
    );
    // Rail rows grow with the text rather than clipping it.
    expect(shell).toMatch(
      /@utility den-shell-nav-sub-link\s*\{[\s\S]*?min-height:\s*max\(24px,\s*calc\(var\(--text-den-hint\)/,
    );
    expect(components).toMatch(
      /\.project-cluster\s*\{[\s\S]*?--project-cluster-action-size:\s*max\(1\.8571rem,\s*1\.75em\)/,
    );
    expect(components).toMatch(
      /\.project-cluster__icon-btn\s*\{[\s\S]*?width:\s*var\(--project-cluster-action-size\)[\s\S]*?height:\s*var\(--project-cluster-action-size\)/,
    );
    expect(components).toMatch(
      /\.project-folders__summary-row\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\) var\(--project-cluster-action-size\)/,
    );
    expect(components).toMatch(
      /\.project-folders__add-icon\s*\{[\s\S]*?width:\s*var\(--project-cluster-action-size\)[\s\S]*?height:\s*var\(--project-cluster-action-size\)/,
    );
  });

  it("markdown code/tables use rem tokens and em padding", () => {
    const md = read("markdown.css");
    expect(md).toMatch(/pre code[\s\S]*?font-size:\s*var\(--text-den-label\)/);
    expect(md).toMatch(/\.markdown-body table[\s\S]*?font-size:\s*var\(--text-den-body\)/);
    expect(md).toMatch(/pre \{[\s\S]*?padding:\s*var\(--transcript-code-padding-y\)/);
    // The fence frame paints; the code inside it scrolls.
    expect(md).toMatch(/\.markdown-code-scroll \{[\s\S]*?background:\s*var\(--den-tint-1\)/);
  });

  it("chat bubbles take their size from the body token", () => {
    const components = read("global-components.css");
    expect(components).toMatch(
      /\.bubble--assistant\s*\{[\s\S]*?font-size:\s*var\(--text-den-body\)/,
    );
    expect(components).toMatch(
      /\.bubble--user\s*\{[\s\S]*?font-size:\s*var\(--text-den-body\)/,
    );
  });

  it("anchors message actions to their chat bubble", () => {
    const components = read("global-components.css");
    expect(components).toMatch(/\.bubble\s*\{[\s\S]*?position:\s*relative/);
  });

  it("uses one closed width and full open width for transcript cards", () => {
    const chat = read("chat-utilities.css");
    const components = read("global-components.css");
    const tools = read("tool-utilities.css");
    const diffs = read("file-edit-diff.css");
    const disclosureHosts = [
      "components/checkpoint/CheckpointDecisionChicklet.tsx",
      "components/workflow/WorkflowFeedbackChicklet.tsx",
      "components/tool/ActivitySpanCard.tsx",
      "components/tool/NetworkChicklet.tsx",
      "components/tool/ToolPartShell.tsx",
      "components/citation/CitationEvidenceChicklet.tsx",
      "components/transcript/IndexWarmingChicklet.tsx",
      "components/transcript/TranscriptDiffGroup.tsx",
      // The row a reply and the turn diffs page both render.
      "components/source/diff/SourceDiffRow.tsx",
    ];

    expect(chat).toMatch(/--den-transcript-card-width:\s*min\(100%,\s*32rem\)/);
    expect(chat).toMatch(
      /@utility den-transcript-disclosure-card\s*\{[\s\S]*?width:\s*var\(--den-transcript-card-width,\s*100%\)[\s\S]*?&\[open\],\s*&\[data-expanded="true"\]\s*\{[\s\S]*?width:\s*100%/,
    );
    expect(components).toMatch(
      /\.den-turn-walk\s*\{[\s\S]*?width:\s*var\(--den-transcript-card-width\)/,
    );
    for (const host of disclosureHosts) {
      expect(read(host), host).toContain("den-transcript-disclosure-card");
    }
    expect(tools).not.toContain("width: var(--den-transcript-card-width)");
    expect(diffs).not.toContain("width: var(--den-transcript-card-width)");
  });

  it("keeps activity summaries on two responsive lines", () => {
    const tools = read("tool-utilities.css");
    const workers = read("worker-utilities.css");

    expect(tools).toMatch(
      /@utility den-activity-span\s*\{[\s\S]*?container-name:\s*den-activity-span/,
    );
    expect(tools).toMatch(
      /@utility den-activity-span-chicklet\s*\{[\s\S]*?display:\s*grid;[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\) auto;[\s\S]*?min-height:\s*max\(48px,\s*calc\(var\(--text-den-compact\) \* 2\.7 \+ 1em\)\)/,
    );
    expect(tools).toMatch(
      /@utility den-activity-span-name\s*\{[\s\S]*?text-overflow:\s*ellipsis/,
    );
    expect(tools).toMatch(
      /@utility den-activity-span-hint\s*\{[\s\S]*?grid-column:\s*1 \/ -1;[\s\S]*?text-overflow:\s*ellipsis/,
    );
    expect(tools).not.toMatch(/\.den-activity-span-hint\s*\{\s*display:\s*none/);
    expect(tools).toMatch(
      /@container den-activity-span \(max-width:\s*15rem\)\s*\{[\s\S]*?\.den-activity-span-count\s*\{[\s\S]*?display:\s*none/,
    );
    expect(tools).toMatch(
      /\.den-tool-part\[data-accordion-row\] \.den-row-mark\s*\{\s*margin-left:\s*auto/,
    );
    expect(workers).toMatch(
      /\.den-worker-part--tool-card\s+\.den-tool-part:not\(\[open\]\):not\(\[data-accordion-row\]\)\s*\{[\s\S]*?width:\s*auto/,
    );
  });

  // The chat column bottoms out at CHAT_COL_MIN, where the rail is ~328px wide.
  it("wraps agent-presented visuals at the narrowest split chat width", () => {
    const chat = read("chat-utilities.css");

    expect(DEFAULT_TRANSCRIPT_SPACING.stripColumn).toBe(CHAT_COL_MIN);
    expect(chat).toMatch(/\.den-visual-present--strip\s*\{[\s\S]*?minmax\(min\(100%,\s*var\(--transcript-strip-column\)\),\s*1fr\)/);
  });

  it("keeps notification cards readable at the narrowest chat column", () => {
    const chat = read("chat-utilities.css");
    const components = read("global-components.css");

    // Ids, endpoints and paths wrap rather than widen the card past the rail.
    for (const util of [
      "den-notice-title",
      "den-notice-teaser",
      "den-notice-message",
      "den-notice-action",
    ]) {
      expect(chat, util).toMatch(
        new RegExp(`@utility ${util}\\s*\\{[\\s\\S]*?overflow-wrap:\\s*anywhere`),
      );
    }

    // A rem cap scales with text; one expanded notice fits whole.
    expect(chat).toMatch(
      /@utility den-notice-rail-list\s*\{[\s\S]*?max-height:\s*min\(22rem,\s*40vh\)/,
    );
    // Cards size to the list, not the chat column.
    expect(chat).toMatch(
      /@utility den-notice-rail-list__content\s*\{[\s\S]*?container-name:\s*den-notice-rail/,
    );
    // Toggling Details/Less leaves adjacent text stable.
    expect(chat).toMatch(
      /@utility den-notice-toggle\s*\{[\s\S]*?min-width:\s*4rem/,
    );
    // The chevron replaces the word; the label stays for assistive tech.
    expect(chat).toMatch(
      /@container den-notice-rail \(max-width:\s*24rem\)\s*\{[\s\S]*?\.den-notice-toggle-label\s*\{[\s\S]*?clip-path:\s*inset\(50%\)/,
    );
    expect(chat).toMatch(
      /@container den-notice-rail \(max-width:\s*24rem\)\s*\{[\s\S]*?\.den-notice-toggle-glyph\s*\{\s*display:\s*inline-flex/,
    );

    // Nudge actions shrink and wrap their labels rather than spilling the card.
    expect(components).toMatch(
      /\.system-nudge__actions\s*\{[\s\S]*?flex:\s*1 1 auto;[\s\S]*?min-width:\s*0/,
    );
    expect(components).toMatch(
      /\.system-nudge__actions > \*\s*\{\s*max-width:\s*100%/,
    );
  });

  it("choice marks share a rem floor; text inputs keep a 24px floor", () => {
    const toolDomain = read("tool-domain.css");
    const tw = read("tailwind.css");
    expect(toolDomain).toMatch(/\.den-task-card-status[\s\S]*?min-height:\s*max\(24px/);
    const radioWidth = tw.match(/@utility den-radio\s*\{[\s\S]*?width:\s*([^;]+)/)?.[1];
    const checkboxWidth = tw.match(/@utility den-checkbox\s*\{[\s\S]*?width:\s*([^;]+)/)?.[1];
    expect(radioWidth).toBe(checkboxWidth);
    expect(radioWidth).toMatch(/max\(1\.7143rem/);
    expect(tw).toMatch(/@utility den-input[\s\S]*?min-height:\s*max\(24px/);
  });
});

