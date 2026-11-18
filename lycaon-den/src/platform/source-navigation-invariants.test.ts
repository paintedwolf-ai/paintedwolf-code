import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { loadSourceCorpus } from "../test/source-corpus.ts";
import { readSourceText } from "../test/stylesheet-source.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");

describe("source navigation invariants", () => {
  it("INV-SRC-12 — only the base rule colours a path link", () => {
    // Density is a closed variant set; colour is the link's own, so a surface
    // cannot style a path out of reading as a link.
    const offenders: string[] = [];
    const styles = loadSourceCorpus(denSrc, { extensions: [".css"] }).files.filter(
      (file) => !file.rel.includes("/"),
    );
    for (const file of styles) {
      // Root sheets are import manifests; read them in cascade order.
      const css = readSourceText(file.path).replace(/\/\*[\s\S]*?\*\//g, "");
      for (const match of css.matchAll(
        /([^{}]*den-source-path[^{}]*)\{([^{}]*)\}/g,
      )) {
        const selector = match[1] ?? "";
        const body = match[2] ?? "";
        const lines = selector.trim().split("\n");
        const sel = (lines[lines.length - 1] ?? "").trim();
        if (sel === ".den-source-path-link") continue;
        if (/(^|[^-\w])color\s*:/.test(body)) offenders.push(`${file.rel}: ${sel}`);
      }
    }
    expect(offenders).toEqual([]);
  });
});
