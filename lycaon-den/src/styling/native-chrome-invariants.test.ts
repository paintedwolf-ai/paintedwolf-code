import { readSourceText } from "../test/stylesheet-source.ts";

import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { walkFiles } from "../test/style-contracts/inventory.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");
const productionTsx = walkFiles(
  denSrc,
  (path) => path.endsWith(".tsx") && !path.endsWith(".test.tsx"),
);

function source(path: string): string {
  return readSourceText(path, "utf8");
}

function matchingFiles(pattern: RegExp, allowed: readonly string[]): string[] {
  return productionTsx
    .filter((path) => pattern.test(source(path)))
    .map((path) => relative(denSrc, path))
    .filter((path) => !allowed.includes(path))
    .sort();
}

describe("visible native chrome", () => {
  it("renders no native select or option DOM", () => {
    expect(matchingFiles(/<(?:select|option|optgroup)\b/, [])).toEqual([]);
  });

  it("uses a direct value API for the custom select", () => {
    const select = source(join(denSrc, "components/primitives/DenSelect.tsx"));
    expect(select).toContain("onValueChange");
    expect(select).not.toContain("onChange");
    expect(select).not.toContain("dispatchEvent");
  });

  it("centralizes checkbox and radio inputs in their primitives", () => {
    expect(
      matchingFiles(/<input\b[^>]*\btype=["'](?:checkbox|radio)["']/s, [
        "components/primitives/DenCheckbox.tsx",
        "components/primitives/DenRadio.tsx",
      ]),
    ).toEqual([]);
  });

  it("centralizes numeric input and keeps file input invisible", () => {
    expect(
      matchingFiles(/<input\b[^>]*\btype=["']number["']/s, [
        "components/primitives/DenNumberInput.tsx",
      ]),
    ).toEqual([]);

    const visibleFileInputs = productionTsx
      .filter((path) => /<input\b[^>]*\btype=["']file["']/s.test(source(path)))
      .filter((path) => !/<input\b(?=[^>]*\btype=["']file["'])(?=[^>]*\bhidden\b)[^>]*>/s.test(source(path)))
      .map((path) => relative(denSrc, path));
    expect(visibleFileInputs).toEqual([]);
  });

  it("suppresses browser search, number, autofill, and resizer paint", () => {
    const css = source(join(denSrc, "global.css"));
    expect(css).toMatch(/input\[type="search"\]::\-webkit-search-cancel-button/);
    expect(css).toMatch(/input\[type="number"\]::\-webkit-inner-spin-button/);
    expect(css).toMatch(/input:\-webkit-autofill/);
    expect(css).toMatch(/textarea::\-webkit-resizer/);
    expect(css).toMatch(/summary::\-webkit-details-marker\s*\{[^}]*display:\s*none/s);
  });

  it("gives every disclosure summary a themed marker", () => {
    const missing = productionTsx.flatMap((path) =>
      [...source(path).matchAll(/<summary\b[\s\S]*?<\/summary>/g)]
        .filter(
          (match) =>
            !/den-(?:disclosure-caret|tool-chicklet-caret|tool-row-mark)/.test(
              match[0],
            ),
        )
        .map((_, index) => `${relative(denSrc, path)}#${index + 1}`),
    );
    expect(missing).toEqual([]);
  });

  it("measures listboxes at intrinsic width before shared placement", () => {
    const css = source(join(denSrc, "tailwind.css"));
    expect(css).toMatch(
      /@utility den-select__listbox\s*\{[^}]*width:\s*max-content[^}]*max-width:\s*calc\(100vw - 16px\)/s,
    );
    const select = source(join(denSrc, "components/primitives/DenSelect.tsx"));
    expect(select).toMatch(/<AnchoredSurface[\s\S]*width="min-anchor"/);
  });

  it("routes anchored popups through the shared surface", () => {
    const structuralListboxes = [
      "components/transcript/TranscriptVisualFilmstrip.tsx",
    ];
    const bypasses = matchingFiles(
      /role=["'](?:menu|listbox)["']/,
      structuralListboxes,
    ).filter((path) => !source(join(denSrc, path)).includes("AnchoredSurface"));
    expect(bypasses).toEqual([]);

    for (const surface of [
      {
        sourcePath: "files/tabs/FilesOpenList.tsx",
        className: "den-files-open-list",
      },
      {
        sourcePath: "components/search/SearchFilterChip.tsx",
        className: "den-search-filter__panel",
      },
    ] as const) {
      expect(source(join(denSrc, surface.sourcePath))).toMatch(
        new RegExp(`<AnchoredSurface[\\s\\S]*${surface.className}`),
      );
    }
  });
});
