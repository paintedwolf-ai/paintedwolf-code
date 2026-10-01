import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { readSourceText } from "../../test/stylesheet-source.ts";

const platformDir = join(dirname(fileURLToPath(import.meta.url)), "..");

describe("scrollbar visual contract", () => {
  it("suppresses native chrome without reserving a gutter", () => {
    const css = readSourceText(join(platformDir, "themed-scrollbars.css"), "utf8");

    expect(css).toContain(".den-scrollport__viewport");
    expect(css).toContain("scrollbar-width: none");
    expect(css).toMatch(/::-webkit-scrollbar\s*\{\s*display:\s*none/);
    expect(css).not.toMatch(/scrollbar-width:\s*(auto|thin)/);
    expect(css).not.toMatch(/scrollbar-gutter\s*:/);
  });

  it("lays a frame out the same before and after its scrollbar attaches", () => {
    const css = readSourceText(join(platformDir, "themed-scrollbars.css"), "utf8");
    const rule = (selector: string) =>
      new RegExp(`${selector.replace(/[[\]().>*"=]/g, "\\$&")}\\s*\\{([^}]*)\\}`).exec(css)?.[1] ?? "";

    // The library's host rule is a stretched row; the frame starts as one.
    expect(rule("[data-den-scrollport]")).toMatch(/display:\s*flex[\s\S]*flex-direction:\s*row[\s\S]*align-items:\s*stretch/);
    // The library's viewport rule: filled, positioned, its own stacking context, no box inset.
    const viewport = rule("[data-den-scrollport] > .den-scrollport__viewport");
    expect(viewport).toMatch(/position:\s*relative/);
    expect(viewport).toMatch(/z-index:\s*0/);
    expect(viewport).toMatch(/flex:\s*1 1 auto/);
    expect(viewport).toMatch(/padding:\s*0/);
    // Chrome paints above everything the viewport stacks, sticky headers included.
    expect(rule("[data-den-scrollport] > .os-scrollbar")).toMatch(/z-index:\s*1/);
    for (const axis of ["y", "x", "both"]) {
      expect(rule(`[data-den-scrollport="${axis}"] > .den-scrollport__viewport`)).toMatch(/overflow/);
    }
  });

  it("fades an idle scrollbar through the library transition", () => {
    const css = readSourceText(join(platformDir, "themed-scrollbars.css"), "utf8");

    expect(css).toMatch(
      /\.os-theme-den\.os-scrollbar\.den-scrollbar-idle\s*\{[\s\S]*?opacity:\s*0;[\s\S]*?visibility:\s*hidden;/,
    );
    expect(css).not.toContain("os-scrollbar-auto-hide");
    expect(css).not.toMatch(/\.os-scrollbar\s*\{[^}]*transition/);
  });

  it("keeps editor scrollbars interactive above the editable surface", () => {
    const css = readSourceText(join(platformDir, "themed-scrollbars.css"), "utf8");

    expect(css).toMatch(
      /\.os-scrollbar\.den-scrollbar-handle-interactive \.os-scrollbar-handle[\s\S]*?pointer-events:\s*auto/,
    );
    expect(css).toMatch(
      /\.cm-editor\[data-overlayscrollbars~="host"\] > \.os-scrollbar\s*\{[\s\S]*?z-index:\s*1/,
    );
  });

  it("spaces Blueprints panels from the launcher itself", () => {
    const css = readSourceText(
      join(platformDir, "..", "blueprints-domain.css"),
      "utf8",
    );

    expect(css).toMatch(
      /\.den-blueprints-launcher \+ \.den-blueprints-empty,[\s\S]*?\.den-blueprints-launcher \+ \.den-blueprints-list-panel\s*\{[\s\S]*?margin-top:\s*16px;/,
    );
  });
});

describe("resident surface layout contract", () => {
  /** Declarations from every rule whose selector list names `className`. */
  const declarationsFor = (className: string) => {
    const css = readSourceText(
      join(platformDir, "..", "shell-domain.css"),
      "utf8",
    );
    const named = new RegExp(`\\.${className}(?![\\w-])`);
    return [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
      .filter(([, selector]) => named.test(selector ?? ""))
      .map(([, , body]) => body)
      .join("\n");
  };

  it("keeps pending surfaces unpainted", () => {
    const pending = declarationsFor("den-resident-surface--pending");

    expect(pending).toContain("visibility: hidden");
    expect(pending).toContain("clip-path: inset(50%)");
    expect(pending).toContain("pointer-events: none");
  });

  it("keeps an idle surface laid out so its scroll offsets survive", () => {
    const idle = declarationsFor("den-resident-surface--idle");

    // Hidden surfaces retain layout and scroll geometry.
    expect(idle).toContain("visibility: hidden");
    expect(idle).toContain("clip-path: inset(50%)");
    expect(idle).toContain("pointer-events: none");
    // Layout loss clamps descendant scroll offsets.
    expect(idle).not.toContain("display: none");
    // WebKit never resumes compositor animations inside revealed skipped content.
    expect(idle).not.toContain("content-visibility");
  });
});
