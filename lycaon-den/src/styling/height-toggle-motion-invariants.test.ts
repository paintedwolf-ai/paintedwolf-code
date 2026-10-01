import { readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";
import { splitStylesheetRules } from "../test/style-contracts/stylesheet-parser.ts";
import { walkFiles } from "../test/style-contracts/inventory.ts";

const denSrc = join(import.meta.dirname, "..");
const motionModule = "ui/height-toggle-motion.ts";
// Tab panels animate an existing clip.
const nonToggleHeightAnimations = new Set(["components/nav/TabBar.tsx"]);

const STATEFUL_SELECTOR = /\[open\]|\[data-animating\]|\[data-expanded="true"\]/;

/** Returns top-level declarations without nested blocks. */
function topLevelDeclarations(body: string): string {
  let depth = 0;
  let declarations = "";
  for (const char of body) {
    if (char === "{") depth++;
    else if (char === "}") depth--;
    else if (depth === 0) declarations += char;
  }
  return declarations;
}

function cssStateViolations(
  declarationPattern: RegExp,
  selectorMatches: (selector: string) => boolean,
): string[] {
  const hits: string[] = [];
  const scan = (rel: string, css: string) => {
    for (const { selector, body } of splitStylesheetRules(css)) {
      if (body.includes("{")) scan(rel, body);
      if (!STATEFUL_SELECTOR.test(selector)) continue;
      if (!selectorMatches(selector)) continue;
      if (declarationPattern.test(topLevelDeclarations(body))) hits.push(`${rel}: ${selector}`);
    }
  };
  for (const path of walkFiles(denSrc, (p) => p.endsWith(".css"))) {
    scan(relative(denSrc, path), readFileSync(path, "utf8"));
  }
  return hits;
}

function targetsStatefulShell(selector: string): boolean {
  return selector.split(",").some((part) =>
    /(?:&|\.[a-zA-Z][\w-]*)(?:\[open\]|\[data-animating\]|\[data-expanded="true"\])\s*$/.test(
      part.trim(),
    ),
  );
}

describe("height toggle motion", () => {
  it("keeps direct measured-height WAAPI in the shared UI module", () => {
    const violations = walkFiles(
      denSrc,
      (path) =>
        (path.endsWith(".ts") || path.endsWith(".tsx")) &&
        !path.endsWith(".test.ts") &&
        !path.endsWith(".test.tsx"),
    ).flatMap((path) => {
      const rel = relative(denSrc, path);
      if (rel === motionModule || nonToggleHeightAnimations.has(rel)) return [];
      const source = readFileSync(path, "utf8");
      const directAnimations = [...source.matchAll(/\.animate\s*\(\s*[\[{]/g)];
      return directAnimations
        .filter((match) =>
          /\bheight\s*:/.test(source.slice(match.index, match.index + 800)),
        )
        .map(() => rel);
    });

    expect([...new Set(violations)].sort()).toEqual([]);
  });

  it("keeps disclosure corner geometry independent of open state", () => {
    const violations = cssStateViolations(
      /\bborder-radius\s*:/,
      () => true,
    );

    expect(violations).toEqual([]);
  });

  it("keeps disclosure shadows independent of open state", () => {
    const violations = cssStateViolations(
      /\bbox-shadow\s*:/,
      () => true,
    );

    expect(violations).toEqual([]);
  });

  it("keeps disclosure shell paint independent of open state", () => {
    const violations = cssStateViolations(
      /\b(?:background|border(?:-[a-z-]+)?)\s*:/,
      targetsStatefulShell,
    );

    expect(violations).toEqual([]);
  });
});
