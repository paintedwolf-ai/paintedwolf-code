// @vitest-environment jsdom
import { readSourceText } from "../test/stylesheet-source.ts";
/** Accessibility source invariants. */

import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { activateFocusTrap } from "./interaction/focus-trap.ts";
import { loadSourceCorpus } from "../test/source-corpus.ts";
import { MAX_TEXT_SCALE, applyTextScale } from "./desktop/accessibility-text-size.ts";

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

describe("macOS a11y scale source", () => {
  it("locks the max harness inject scale", () => {
    expect(MAX_TEXT_SCALE).toBeCloseTo(53 / 17);
  });

  it("sets --den-text-scale only in the accessibility text-size controller", () => {
    const setters = walkFiles(denSrc, (p) => p.endsWith(".ts") || p.endsWith(".tsx")).filter(
      (p) => {
        if (p.includes(".test.")) return false;
        const body = readRepo(p);
        return /setProperty\(\s*"--den-text-scale"/.test(body);
      },
    );
    const rel = setters.map((p) => p.slice(denSrc.length + 1));
    expect(rel).toEqual(["platform/desktop/accessibility-text-size.ts"]);
  });

  it("harness inject routes through applyTextScale (not a parallel CSS setter)", () => {
    const harness = read("platform/harness/harness-driver.ts");
    expect(harness).toMatch(/import\s*\{[^}]*applyTextScale[^}]*\}\s*from\s*"\.\.\/desktop\/accessibility-text-size\.ts"/);
    expect(harness).toMatch(/applyTextScale\(next\)/);
    expect(harness).not.toMatch(/setProperty\("--den-text-scale"/);
  });

  it("applies payload from content-size event shape { category, textScale }", () => {
    document.documentElement.style.removeProperty("--den-text-scale");
    applyTextScale(19 / 17);
    expect(document.documentElement.style.getPropertyValue("--den-text-scale")).toBe(
      String(19 / 17),
    );
    document.documentElement.style.removeProperty("--den-text-scale");
  });
});

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
  it("html root font-size uses calc(14px * var(--den-text-scale))", () => {
    const globalCss = read("global.css");
    expect(globalCss).toMatch(
      /html[\s\S]*?font-size:\s*calc\(14px\s*\*\s*var\(--den-text-scale\)\)/,
    );
    // Root size participates in scale calculation.
    expect(globalCss).not.toMatch(/html\s*,[\s\S]*?\{[^}]*font-size:\s*14px\s*;/);
    expect(globalCss).not.toMatch(/html\s*\{[^}]*font-size:\s*14px\s*;/);
  });

  it("@theme --text-den-body ends with rem", () => {
    const tw = read("tailwind.css");
    expect(tw).toMatch(/--text-den-body:\s*[\d.]+rem\s*;/);
  });

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

  it("no editor theme object declares a px fontSize", () => {
    const offenders: string[] = [];
    const files = ["components", "files"].flatMap((dir) => walkFiles(join(denSrc, dir), (p) => p.endsWith(".ts")));
    expect(files.length).toBeGreaterThan(20);
    for (const path of files) {
      if (path.includes(".test.")) continue;
      readSourceText(path, "utf8")
        .split("\n")
        .forEach((line, i) => {
          if (/fontSize:\s*["'][\d.]+px["']/.test(line)) {
            offenders.push(`${path.slice(denSrc.length + 1)}:${i + 1}`);
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

  it("conversation log and articles use stable role labels", () => {
    const spans = read("components/transcript/ChatSpanBlocks.tsx");
    const transcript = read("components/transcript/SessionTranscript.tsx");
    const userBubble = read("components/transcript/UserBubble.tsx");
    const assistant = read("components/transcript/AssistantChatTurn.tsx");
    expect(spans).toMatch(/role="log"/);
    expect(spans).toMatch(/aria-live="polite"/);
    expect(spans).toMatch(/class="den-chat-transcript-visual" aria-live="off"/);
    expect(spans).toMatch(/aria-label="Conversation"/);
    expect(transcript).not.toMatch(/role="log"/);
    expect(userBubble).toMatch(/aria-label="You"/);
    expect(assistant).toMatch(/aria-label="Assistant"/);
    expect(userBubble).not.toMatch(/aria-label=\{[^}]*\.content/);
    expect(assistant).not.toMatch(/aria-label=\{[^}]*\.content/);
    expect(userBubble).not.toMatch(/aria-label=\{[^}]*includes\(/);
  });

  it("documents dual live-region policy: NoticeRail muted while any session is live", () => {
    const shell = read("components/shell/Shell.tsx");
    const notice = read("components/NoticeRail.tsx");
    expect(shell).toMatch(/announceLive=\{!isAnySessionActivityLive\(props\.appStore\)\}/);
    expect(notice).toMatch(/role="status"/);
    expect(notice).toMatch(/aria-live=\{announceLive\(\) \? "polite" : "off"\}/);
  });
});

describe("macOS a11y focus trap restore", () => {
  it("activateFocusTrap restores prior focus on deactivate", async () => {
    const opener = document.createElement("button");
    opener.type = "button";
    opener.textContent = "opener";
    document.body.append(opener);
    opener.focus();

    const dialog = document.createElement("div");
    const close = document.createElement("button");
    close.type = "button";
    close.textContent = "close";
    dialog.append(close);
    document.body.append(dialog);

    const trap = activateFocusTrap(dialog);
    await Promise.resolve();
    expect(document.activeElement).toBe(close);
    trap.deactivate();
    expect(document.activeElement).toBe(opener);

    dialog.remove();
    opener.remove();
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

describe("macOS a11y target size", () => {
  it("quiet icon buttons use ≥24px floors at scale 1", () => {
    // 1.7143rem is 24px at the 14px root, and grows with the OS text scale.
    expect(read("global.css")).toMatch(
      /--den-icon-target:\s*max\(1\.7143rem,\s*1\.75em\)/,
    );
    const buttons = read("styling/recipes/buttons-utilities.css");
    expect(buttons).toMatch(
      /@utility den-quiet-icon-btn\s*\{[\s\S]*?width:\s*var\(--den-icon-target\)/,
    );
    expect(buttons).toMatch(
      /@utility den-quiet-icon-btn\s*\{[\s\S]*?height:\s*var\(--den-icon-target\)/,
    );
    // The composer's action pad is bounded by the same target its buttons take.
    expect(read("chat-utilities.css")).toMatch(
      /--den-composer-action-col:\s*var\(--den-icon-target\)/,
    );
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
