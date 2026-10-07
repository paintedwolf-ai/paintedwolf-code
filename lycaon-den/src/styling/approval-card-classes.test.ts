import { describe, expect, it } from "vitest";
import { join } from "node:path";
import { denSourceRoot, loadSourceCorpus, loadTypeScriptCorpus } from "../test/source-corpus.ts";
import { readSourceText } from "../test/stylesheet-source.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const sources = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true });
const styles = loadSourceCorpus(denSourceRoot, { extensions: [".css"] });

function definedClasses(): Set<string> {
  const defined = new Set<string>();
  for (const file of styles.files.filter((candidate) => !candidate.rel.includes("/"))) {
    const css = readSourceText(file.path);
    for (const m of css.matchAll(/@utility\s+([A-Za-z0-9_-]+)/g)) {
      if (m[1]) defined.add(m[1]);
    }
    for (const m of css.matchAll(/\.([a-z][a-z0-9_-]*)/g)) {
      if (m[1]) defined.add(m[1]);
    }
  }
  return defined;
}

/** Returns the body of one `@utility <name>` block, braces balanced. */
function utilityBlock(css: string, name: string): string {
  const start = css.indexOf(`@utility ${name} {`);
  expect(start, `@utility ${name} is missing`).toBeGreaterThan(-1);
  let depth = 0;
  for (let i = css.indexOf("{", start); i < css.length; i += 1) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}" && (depth -= 1) === 0) return css.slice(start, i);
  }
  expect.fail(`@utility ${name} never closes`);
}

/** Member previews wrap because the card clips horizontal overflow. */
describe("approval card members never hide behind the clipped axis", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("target previews wrap while the scroll region clips overflow-x", () => {
    const css = readSourceText(join(denSourceRoot, "tool-utilities.css"));
    // A vertical scrollport clips overflow-x.
    const shell = sources.file("components/checkpoint/ApprovalShell.tsx")?.text ?? "";
    expect(shell).toMatch(/<Scrollport class="den-approval-card-scroll">/);
    expect(utilityBlock(css, "den-approval-card-declared")).toMatch(
      /&\s*pre\s*\{[^}]*white-space:\s*pre-wrap/,
    );
  });

  it("sizes member text with the card scale rather than user-agent defaults", () => {
    const css = readSourceText(join(denSourceRoot, "tool-utilities.css"));
    const declared = utilityBlock(css, "den-approval-card-declared");
    // A bare code/pre takes the browser's 16px monospace and dwarfs the card.
    expect(declared).toMatch(/font-size:\s*var\(--text-den-label\)/);
    expect(declared).toMatch(/font-family:\s*var\(--den-font-mono\)/);
    expect(declared).toMatch(
      /&\s*code,\s*\n?\s*&\s*pre\s*\{[^}]*font-size:\s*inherit/,
    );
  });
});

describe("approval card CSS hooks resolve to real rules", () => {
  it("every den-approval-card-* class used in TSX has a CSS rule", () => {
    const defined = definedClasses();
    const missing = new Map<string, string[]>();

    for (const file of sources.files) {
      for (const m of file.text.matchAll(/den-(?:approval|checkpoint)-card[a-z0-9-]*/g)) {
        const cls = m[0];
        if (defined.has(cls)) continue;
        missing.set(cls, [...(missing.get(cls) ?? []), file.rel]);
      }
    }

    expect(
      [...missing].map(([cls, files]) => `${cls} (${[...new Set(files)].join(", ")})`),
    ).toEqual([]);
  });
});
