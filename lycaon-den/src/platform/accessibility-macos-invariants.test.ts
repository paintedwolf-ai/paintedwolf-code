// @vitest-environment jsdom
import { readSourceText } from "../test/stylesheet-source.ts";
/** Accessibility source invariants. */

import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { loadSourceCorpus } from "../test/source-corpus.ts";

const denSrc = join(import.meta.dirname, "..");
const denRoot = join(denSrc, "..");
const tauriSrc = join(denRoot, "src-tauri", "src");

function read(relFromSrc: string): string {
  return readSourceText(join(denSrc, relFromSrc), "utf8");
}

function readRepo(abs: string): string {
  return readSourceText(abs, "utf8");
}

function walkFiles(dir: string, pred: (p: string) => boolean): string[] {
  return loadSourceCorpus(dir).files.map((file) => file.path).filter(pred);
}

describe("macOS a11y forbidden preferredFont path", () => {
  it("Rust bridge and Den scale module never call preferredFont APIs", () => {
    const rust = walkFiles(tauriSrc, (p) => p.endsWith(".rs"))
      .map(readRepo)
      .join("\n");
    // Ignore comments when checking live calls.
    const rustCode = rust
      .split("\n")
      .filter((line) => !/^\s*\/\/|^\s*\/\*|^\s*\*/.test(line))
      .join("\n");
    expect(rustCode).not.toMatch(/preferredFont\s*\(/);
    expect(rustCode).not.toMatch(/NSAccessibilityPreferredTextAttributesChanged/);
    expect(rust).toMatch(/UIPreferredContentSizeCategoryName|PreferredContentSizeCategory/);

    const ts = read("platform/desktop/accessibility-text-size.ts");
    const tsCode = ts
      .split("\n")
      .filter((line) => !/^\s*\/\//.test(line))
      .join("\n");
    expect(tsCode).not.toMatch(/preferredFont/);
  });
});

describe("macOS a11y root + rem type scale", () => {
  it("every --text-den rung ends with rem", () => {
    const tw = read("tailwind.css");
    const rungs = [...tw.matchAll(/--text-den-[a-z]+:\s*([^;]+);/g)].map((m) => m[1]?.trim() ?? "");
    expect(rungs.length).toBeGreaterThan(0);
    for (const value of rungs) expect(value).toMatch(/^[\d.]+rem$/);
  });

  it("every CSS font size resolves through the Den type ladder", () => {
    const offenders: string[] = [];
    const files = walkFiles(denSrc, (p) => p.endsWith(".css"));
    // An empty sweep would pass this vacuously.
    expect(files.length).toBeGreaterThan(20);
    for (const file of files) {
      readSourceText(file, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (
            /font-size:/.test(line) &&
            !/font-size:\s*(?:var\(--text-den-[a-z]+\)|var\(--den-brand-label-size\)|inherit|calc\(14px\s*\*\s*var\(--den-text-scale\)\))\s*;/.test(line)
          ) {
            offenders.push(`${file.slice(denSrc.length + 1)}:${i + 1}`);
          }
        });
    }
    expect(offenders).toEqual([]);
  });
});

describe("macOS a11y transcript + live regions", () => {
  it("keeps transcript prose selectable (not user-select:none)", () => {
    const bubbles = read("global-components.css");
    const md = read("markdown.css");
    const citations = read("citation-utilities.css");
    expect(bubbles).toMatch(/\.bubble--user\s*\{[\s\S]*?user-select:\s*text/);
    expect(bubbles).toMatch(/\.bubble--assistant\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).toMatch(/\.markdown-body\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).not.toMatch(/\.markdown-body\s*\{[\s\S]*?user-select:\s*none/);
    expect(md).toMatch(/\.markdown-body\s+a\s*\{[\s\S]*?user-select:\s*text/);
    expect(md).toMatch(/\.markdown-body\s+\.den-source-path-link\s*\{[\s\S]*?user-select:\s*text/);
    expect(citations).toMatch(/\.den-source-path-link\s*\{[\s\S]*?user-select:\s*text/);
    expect(citations).toMatch(/\.den-source-url-link\s*\{[\s\S]*?user-select:\s*text/);
  });
});

describe("macOS a11y modal handling", () => {
  it("declares dialog backdrops as registered modals", () => {
    const offenders = ["components", "files"].flatMap((dir) => walkFiles(
      join(denSrc, dir),
      (p) => p.endsWith(".tsx") && !p.includes(".test."),
    ))
      .filter((p) => readRepo(p).includes('class="den-dialog-backdrop'))
      .filter((p) => {
        const body = readRepo(p);
        return !(
          body.includes('aria-modal="true"') &&
          (body.includes("createModalFocusTrap(") ||
            body.includes("createOverlayScopeFocusTrap("))
        );
      })
      .map((p) => p.slice(denSrc.length + 1));

    expect(offenders).toEqual([]);
  });

  it("routes every aria-modal surface through shared focus control", () => {
    const offenders = walkFiles(
      denSrc,
      (p) => (p.endsWith(".tsx") || p.endsWith(".ts")) && !p.includes(".test."),
    )
      .filter((p) => readRepo(p).includes('aria-modal="true"'))
      .filter((p) => {
        const body = readRepo(p);
        return !(
          body.includes("createModalFocusTrap(") ||
            body.includes("createOverlayScopeFocusTrap(") ||
          body.includes("activateFocusTrap(") ||
          body.includes("<DenOverlay")
        );
      })
      .map((p) => p.slice(denSrc.length + 1));

    expect(offenders).toEqual([]);
  });
});

describe("macOS a11y reduced motion", () => {
  it("honors prefers-reduced-motion for splash/drawer/scroll chrome", () => {
    const globalCss = read("global.css");
    const drawers = read("drawer-utilities.css");
    expect(globalCss).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)/);
    expect(globalCss).toMatch(/scroll-behavior:\s*auto/);
    expect(drawers).toMatch(/@media\s*\(prefers-reduced-motion:\s*reduce\)/);
  });
});
