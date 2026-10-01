import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { extractCssRuleBlock } from "./css-contract.ts";
import { extractClassHooksFromSource } from "../test/style-contracts/jsx-classes.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

function readText(rel: string): string {
  return readSourceText(join(denSrc, rel), "utf8");
}

// Match co-located hooks without relying on class order.
function classesBesideHook(source: string, hook: string): string[] {
  const attribute = source.match(new RegExp(`class="([^"]*\\b${hook}\\b[^"]*)"`));
  return attribute?.[1]?.trim().split(/\s+/) ?? [];
}

describe("domain layout CSS contracts", () => {
  describe("citation grounding chicklet", () => {
    const citationCss = readText("citation-utilities.css");
    const chicklet = readText("components/citation/CitationGroundingChicklet.tsx");

    it("styles the traced pill with muted token colors", () => {
      const base = extractCssRuleBlock(citationCss, ".den-citation-grounding-chicklet");
      const traced = extractCssRuleBlock(citationCss, ".den-citation-grounding-chicklet--traced");
      expect(base).toMatch(/display:\s*inline-flex/);
      // The transparent button around it makes this the control's visible body.
      expect(base).toMatch(/border-radius:\s*var\(--den-radius-pill\)/);
      expect(traced).toMatch(/var\(--den-text-muted\)/);
      expect(traced).toMatch(/var\(--den-line\)/);
    });

    it("component applies the den-* base hook matching domain CSS", () => {
      const hooks = extractClassHooksFromSource(chicklet);
      expect(hooks).toContain("den-citation-grounding-chicklet");
      expect(hooks).toContain("den-citation-grounding-chicklet--traced");
      expect(hooks).not.toContain("citation-grounding-chicklet");
    });
  });

  describe("chat composer send art", () => {
    const chatArt = readText("chat-art.css");
    const chatUtils = readText("chat-utilities.css");
    const composer = readText("components/chatview/Composer.tsx");

    it("uses theme icon slots for every composer action", () => {
      expect(composer).toMatch(/<ThemeIcon slot="plus"/);
      expect(composer).toMatch(/<ThemeIcon slot="arrow-up"/);
      expect(composer).toMatch(/<ThemeIcon slot="stop"/);
    });

    it("keeps every composer action on the shared quiet icon recipe", () => {
      const activity = readText("components/chatview/ComposerActivityIndicator.tsx");
      expect(readText("styling/recipes/buttons-utilities.css")).toMatch(
        /@utility den-quiet-icon-btn/,
      );
      expect(chatUtils).toMatch(/@utility den-composer-action-pad/);
      expect(chatUtils).toMatch(/@utility den-composer-scroll/);
      expect(chatUtils).toMatch(/@utility den-composer-activity\b/);
      expect(chatUtils).toMatch(/@utility den-composer-activity-label/);
      expect(chatUtils).toMatch(
        /\.den-composer-scroll \.os-scrollbar\.os-scrollbar-vertical/,
      );
      expect(composer).toMatch(/<Scrollport\b[^\n]*class="den-composer-scroll"/);
      expect(composer).toMatch(/den-composer-action-pad/);
      expect(composer).toMatch(/den-composer-scroll/);
      expect(classesBesideHook(composer, "den-composer-send"))
        .toContain("den-quiet-icon-btn");
      expect(classesBesideHook(composer, "den-composer-stop"))
        .toContain("den-quiet-icon-btn");
      expect(activity).toMatch(/den-composer-activity/);
      expect(activity).not.toMatch(/<button/);
    });

    it("renders composer actions as rounded-square buttons with clear roles", () => {
      const button = extractCssRuleBlock(
        readText("styling/recipes/buttons-utilities.css"),
        "den-quiet-icon-btn",
      );
      const send = extractCssRuleBlock(chatArt, ".den-composer-send");
      const stop = extractCssRuleBlock(chatArt, ".den-composer-stop");
      expect(button).toMatch(/border-radius:\s*var\(--den-radius-sm\)/);
      expect(button).not.toMatch(/var\(--den-radius-pill\)/);
      expect(button).toMatch(/display:\s*inline-grid/);
      expect(button).toMatch(/place-items:\s*center/);
      expect(button).not.toMatch(/box-shadow/);
      // Quiet at rest: only send, the one primary action, carries a fill.
      expect(button).toMatch(/border:\s*1px solid transparent/);
      expect(button).toMatch(/background:\s*transparent/);
      expect(send).toMatch(/background:\s*var\(--den-accent\)/);
      expect(send).toMatch(/color:\s*var\(--den-on-accent\)/);
      expect(send).not.toMatch(/box-shadow/);
      // A turn in flight is live, not dangerous.
      expect(stop).toMatch(/color:\s*var\(--den-status-running\)/);
      expect(stop).not.toMatch(/var\(--den-danger\)/);
      expect(chatArt).not.toMatch(/box-shadow/);
    });

    it("Composer references den-composer-send hooks", () => {
      const hooks = extractClassHooksFromSource(composer);
      expect(hooks.some((h) => h.startsWith("den-composer"))).toBe(true);
    });
  });

  describe("task card running status", () => {
    const toolDomain = readText("tool-domain.css");
    const taskCard = readText("components/TaskCard.tsx");

    it("paints the live running dot with accent in tool-domain.css", () => {
      expect(extractCssRuleBlock(toolDomain, ".den-task-card-status--live::before")).toMatch(
        /background:\s*var\(--den-accent-signal\)/,
      );
      expect(extractCssRuleBlock(toolDomain, ".den-task-card-status--running")).toMatch(
        /color:\s*var\(--den-accent-text\)/,
      );
    });

    it("TaskCard applies dynamic status hooks backed by tool-domain.css", () => {
      expect(taskCard).toMatch(/den-task-card-status--\$\{/);
      expect(toolDomain).toMatch(/\.den-task-card-status--running/);
      expect(toolDomain).toMatch(/\.den-task-card-status--done/);
    });
  });

  describe("worker transcript section status", () => {
    const workerDomain = readText("worker-domain.css");
    const section = readText("components/worker/WorkerTranscriptSection.tsx");

    it("paints working status with accent in worker-domain.css", () => {
      expect(extractCssRuleBlock(workerDomain, ".den-worker-transcript-section-status--working")).toMatch(
        /color:\s*var\(--den-accent-text\)/,
      );
    });

    it("WorkerTranscriptSection applies dynamic status hooks backed by worker-domain.css", () => {
      expect(section).toMatch(/den-worker-transcript-section-status--\$\{/);
      expect(workerDomain).toMatch(/\.den-worker-transcript-section-status--complete/);
      expect(workerDomain).toMatch(/\.den-worker-transcript-section-status--failed/);
    });
  });

  describe("drawer elevation vs shell header", () => {
    const drawerCss = readText("drawer-domain.css") + readText("drawer-utilities.css");
    const shellCss = readText("shell-domain.css");
    const shellUtils = readText("shell-utilities.css");

    it("context drawers stack above their chat content", () => {
      const drawer = extractCssRuleBlock(drawerCss, ".den-context-drawer");
      expect(drawer).toMatch(/z-index:\s*var\(--den-z-context-drawer\)/);
      expect(drawer).toMatch(/position:\s*absolute/);
      expect(shellCss + shellUtils).toMatch(
        /\.den-shell-header[\s\S]*z-index:\s*var\(--den-z-stage-header\)/,
      );
    });

    it("chat-stage paint layers keep chat chrome above the chat column", () => {
      const globalCss = readText("global.css");
      const chatCss = readText("chat-utilities.css");
      expect(globalCss).toMatch(
        /--den-z-chat-chrome:\s*7[\s\S]*--den-z-composer-dock:\s*6[\s\S]*--den-z-context-drawer:\s*20/,
      );
      expect(shellCss).toMatch(
        /\.den-shell-stage--chat\s+\.den-shell-main\s*\{[\s\S]*?overflow:\s*visible/,
      );
      expect(shellCss).toMatch(
        /\.den-shell-stage--chat\s+\.den-shell-main\s+>\s+\.den-chat\s*\{[\s\S]*?z-index:\s*var\(--den-z-composer-dock\)[\s\S]*?overflow:\s*visible/,
      );
      expect(chatCss).toMatch(
        /@utility den-chat-composer-dock\s*\{[\s\S]*?z-index:\s*var\(--den-z-composer-dock\)/,
      );
      expect(chatCss).toMatch(
        /@utility den-chat-conversation\s*\{[\s\S]*?z-index:\s*var\(--den-z-content\)/,
      );
      expect(shellCss).toMatch(
        /\.den-shell-header-chat\s*\{[\s\S]*?z-index:\s*var\(--den-z-chat-chrome\)/,
      );
      expect(chatCss).not.toMatch(
        /\.den-chat:not\(\.den-chat--context-drawer-open\)/,
      );
    });

    it("ContextDrawer mounts in its host", () => {
      const src = readText("components/shell/ContextDrawer.tsx");
      expect(src).not.toMatch(/Portal/);
      expect(src).toMatch(/class="den-context-drawer"/);
    });
  });

  describe("anchored surface elevation", () => {
    it("portals dropdowns above drawers and gives search suggestions the shared host", () => {
      const globalCss = readText("global.css");
      const anchored = readText("components/primitives/AnchoredSurface.tsx");
      const search = readText("components/search/SearchQueryBar.tsx");
      const anchoredLayer = Number(
        globalCss.match(/--den-z-anchored-surface:\s*(\d+)/)?.[1],
      );
      const drawerLayer = Number(
        globalCss.match(/--den-z-context-drawer:\s*(\d+)/)?.[1],
      );
      expect(anchoredLayer).toBeGreaterThan(drawerLayer);
      expect(anchoredLayer).toBeGreaterThan(90);
      expect(anchored).toMatch(/<ResidentPortal mount=\{document\.body\}>/);
      expect(anchored).toMatch(/"z-index":\s*"var\(--den-z-anchored-surface\)"/);
      expect(search).toMatch(/<AnchoredSurface[\s\S]*den-search-query__suggestions-wrap/);
    });
  });

  describe("desktop viewport boundary", () => {
    it("clips document overflow without taking the app root out of layout", () => {
      const globalCss = readText("global.css");
      const documentBoundary = globalCss.match(
        /html,\s*body\s*\{([^}]*)\}/,
      )?.[1] ?? "";
      const rootRules = globalCss.match(/#root\s*\{([^}]*)\}/g) ?? [];
      const root = rootRules[rootRules.length - 1] ?? "";
      expect(documentBoundary).toMatch(/overflow:\s*clip/);
      expect(root).toMatch(/height:\s*100%/);
      expect(root).not.toMatch(/position:\s*fixed/);
    });
  });

  describe("file version history", () => {
    // Scroll viewports need a definite height inside capped columns.
    it("gives the scroll host a definite height inside a capped column", () => {
      const filesCss = readText("files-domain.css");
      const menu = extractCssRuleBlock(filesCss, ".den-file-version__menu");
      expect(menu).toMatch(/display:\s*flex/);
      expect(menu).toMatch(/flex-direction:\s*column/);
      expect(menu).toMatch(/max-height:/);

      const utilities = readText("files-utilities.css");
      const scroll = extractCssRuleBlock(utilities, "@utility den-file-version__scroll");
      expect(scroll).toMatch(/flex:\s*1 1 auto/);
      expect(scroll).toMatch(/min-height:\s*0/);
    });

    it("scrolls the version list on its own vertical scrollport", () => {
      const picker = readText("files/history/FileVersionPicker.tsx");
      expect(picker).toMatch(/<Scrollport class="den-file-version__scroll">/);
    });
  });

  describe("file summary", () => {
    it("wraps long explanation tokens without a horizontal scrollport", () => {
      const utilities = readText("files-utilities.css");
      const drawer = readText("files/components/FileSummaryDrawer.tsx");
      const scroll = drawer.match(/<Scrollport\s[^>]*class="den-file-summary-drawer__body"[^>]*>/)?.[0] ?? "";
      const section = extractCssRuleBlock(
        utilities,
        "@utility den-file-summary__section",
      );
      const symbol = extractCssRuleBlock(
        utilities,
        "@utility den-file-summary__symbol",
      );

      // A vertical scrollport: the default axis, never "x" or "both".
      expect(scroll).not.toBe("");
      expect(scroll).not.toMatch(/\baxis=/);
      expect(section).toMatch(/overflow-wrap:\s*anywhere/);
      expect(symbol).toMatch(/max-width:\s*100%/);
      expect(symbol).toMatch(/white-space:\s*normal/);
      expect(symbol).toMatch(/overflow-wrap:\s*anywhere/);
    });
  });

  describe("editor chrome controls", () => {
    // The picker and the toggle share a row, so they take one height.
    it("sizes pickers and the view toggle from one token", () => {
      const filesCss = readText("files-domain.css");
      expect(extractCssRuleBlock(filesCss, ".project-files-view"))
        .toMatch(/--den-files-ctl-h:/);
      for (const selector of [
        ".den-file-version__trigger",
        ".den-browse-segmented.den-files-editor__seg",
      ]) {
        expect(extractCssRuleBlock(filesCss, selector), selector)
          .toMatch(/height:\s*var\(--den-files-ctl-h\)/);
      }
    });
  });

  describe("project cost view", () => {
    const costCss = readText("cost-domain.css");
    const view = readText("components/project/ProjectCostView.tsx");

    // Unlayered descendant selectors outrank CostAmount's utility and
    // den-status-mark, so the caption treatment must stop at the KPI's own
    // label and note.
    it("keeps the KPI caption off the spend amount", () => {
      expect(view).toMatch(/<strong><CostAmount /);
      expect(costCss).not.toMatch(/\.project-cost-kpis\s+(span|small)\b/);
      const caption = extractCssRuleBlock(
        costCss,
        ".project-cost-kpis article > span,\n.project-cost-kpis article > small",
      );
      expect(caption).toMatch(/font-size:\s*var\(--text-den-caption\)/);
    });

    it("leaves card header status marks at their own size", () => {
      expect(view).toMatch(/<\/div>\s*<span class="den-status-mark"/);
      expect(costCss).not.toMatch(/\.project-cost-card\s*>\s*header\s*>\s*span\b/);
    });
  });

  describe("segmented control", () => {
    // Segment selection leaves the track width stable.
    it("reserves the chosen-state width on every label", () => {
      const browse = readText("browse-stage-domain.css");
      expect(extractCssRuleBlock(browse, ".den-browse-segmented"))
        .toMatch(/--den-segment-weight-on:/);
      const reserved = extractCssRuleBlock(browse, ".den-browse-segment__label::after");
      expect(reserved).toMatch(/content:\s*attr\(data-label\)/);
      expect(reserved).toMatch(/font-weight:\s*var\(--den-segment-weight-on\)/);
      expect(reserved).toMatch(/height:\s*0/);
      expect(readText("components/browse/BrowseSegmented.tsx"))
        .toMatch(/data-label=\{opt\.label\}/);
    });
  });
});
