import { readSourceText } from "../test/stylesheet-source.ts";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { loadSourceCorpus } from "../test/source-corpus.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

function readCss(name: string): string {
  return readSourceText(join(denSrc, name), "utf8");
}

describe("diff text selection stays visible", () => {
  it("paints change fills from the theme plane, translucent so the selection layer shows", () => {
    const fills = ["add-line", "add-word", "delete-line", "delete-word"].map((id) => `--den-diff-${id}`);
    const generated = readCss("tokens.generated.css");
    for (const token of fills) {
      const values = [...generated.matchAll(new RegExp(`${token}:\\s*(#[0-9a-f]+);`, "g"))].map((m) => m[1]!);
      expect(values.length, `${token} missing from the stock planes`).toBeGreaterThan(0);
      for (const value of values) {
        expect(value, `${token} must carry alpha`).toMatch(/^#[0-9a-f]{8}$/);
        expect(value.slice(7), `${token} must not be opaque`).not.toBe("ff");
      }
    }

    const redefined = loadSourceCorpus(denSrc, { extensions: [".css", ".ts", ".tsx"], excludeTests: true })
      .select((file) => file.rel !== "tokens.generated.css" && !file.rel.endsWith(".generated.ts"))
      .filter((file) => fills.some((token) => new RegExp(`${token}\\s*:`).test(file.text)))
      .map((file) => file.rel);
    expect(redefined, "change fills are fitted by the theme compiler; restyle them there").toEqual([]);
  });

  it("uses the editor selection layer for paged diffs and a range fill for replacement previews", () => {
    const derived = readCss("tokens-derived.css");
    expect(derived).toMatch(
      /--den-diff-selection:\s*color-mix\(in srgb, var\(--den-text\) 18%, var\(--den-selection-strong\)\)/,
    );

    const reader = readCss("components/source/reader/source-reader-editor.ts");
    expect(reader).toContain("createSourceEditorState(");
    const editorTheme = readCss("components/source/editor/editor-theme-rules.ts");
    expect(editorTheme).toContain(".cm-selectionBackground");
    expect(editorTheme).toContain("--den-current-window-selection-strong");

    expect(readCss("search-domain.css")).toMatch(
      /\.den-search-replace__diff ::selection \{[\s\S]*?background-color:\s*var\(--den-diff-selection\)/,
    );
  });
});
