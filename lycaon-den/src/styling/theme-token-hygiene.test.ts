import { readSourceText } from "../test/stylesheet-source.ts";
/** Structural token coverage. */
import { describe, expect, it } from "vitest";

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  SYNTAX_SCOPE_VARS,
  THEME_TOKEN_VARS,
} from "../contributions/theme-vocabulary.generated.ts";
import { extractCssRuleBlock } from "./css-contract.ts";
import { loadSourceCorpus } from "../test/source-corpus.ts";

const DEN_SRC = join(dirname(fileURLToPath(import.meta.url)), "..");

/** Sole color-literal source. */
const GENERATED_TOKEN_SHEET = "tokens.generated.css";

function walk(dir: string, ext: readonly string[]): string[] {
  return loadSourceCorpus(dir, { extensions: ext }).files.map((file) => file.path);
}

function rel(path: string): string {
  return path.slice(DEN_SRC.length + 1);
}

/** Match color literals while ignoring token fallbacks. */
const COLOR_LITERAL =
  /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(|(?<![-\w])(?:white|black|red|green|blue|gray|grey|silver|maroon|navy|olive|teal|purple|fuchsia|aqua|lime|yellow|orange)(?![-\w])/;

function colorLiteralLines(body: string, label: string): string[] {
  const hits: string[] = [];
  // Blank comments before scanning to avoid prose matches.
  const lines = body.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
  lines.split("\n").forEach((line, i) => {
    // Exclude derivation syntax and nonliteral keywords.
    const stripped = line
      .replace(/var\([^)]*\)/g, " ")
      .replace(/color-mix\(/g, " ")
      .replace(/\bin (?:srgb|oklab|oklch|hsl)\b/g, " ");
    if (COLOR_LITERAL.test(stripped)) {
      hits.push(`${label}:${i + 1}: ${line.trim()}`);
    }
  });
  return hits;
}

/** Blank comments while preserving source offsets. */
function withoutComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, " "));
}

/** Find the enclosing rule by walking balanced braces. */
function enclosingRule(
  css: string,
  at: number,
): { selector: string; block: string } {
  let depth = 0;
  let open = -1;
  for (let i = at; i >= 0; i--) {
    if (css[i] === "}") depth++;
    else if (css[i] === "{") {
      if (depth === 0) {
        open = i;
        break;
      }
      depth--;
    }
  }
  if (open < 0) return { selector: "<file>", block: css };
  const head = css.slice(0, open);
  const selector = head.slice(Math.max(...[";", "{", "}"].map((c) => head.lastIndexOf(c))) + 1);

  depth = 0;
  let close = css.length;
  for (let i = open; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}" && --depth === 0) {
      close = i;
      break;
    }
  }
  return {
    selector: selector.trim().replace(/\s+/g, " "),
    block: css.slice(open, close),
  };
}

/** A rule by exact selector, for pinning one recipe. */
function ruleBlock(css: string, selector: string): string | null {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const at = new RegExp(`^${escaped}\\s*\\{`, "m").exec(withoutComments(css));
  return at ? enclosingRule(withoutComments(css), at.index + at[0].length).block : null;
}

/** Enforce explicit background planes for theme-independent blending. */
describe("plane discipline", () => {
  // Surfaces a region may sit on. Marks blend into whichever one they land on.
  const PLANES = [
    "var(--den-background)",
    "var(--den-surface-chrome)",
    "var(--den-surface-offset)",
  ];

  it("publishes the plane wherever chrome is painted", () => {
    // Painting a plane also declares it for descendants.
    const offenders: string[] = [];
    let painted = 0;
    for (const file of walk(DEN_SRC, [".css"])) {
      const css = withoutComments(readSourceText(file, "utf8"));
      for (const m of css.matchAll(
        /background:\s*var\(--den-surface-chrome\)\s*;/g,
      )) {
        painted++;
        const rule = enclosingRule(css, m.index);
        if (rule.block.includes("--den-plane: var(--den-surface-chrome)")) continue;
        offenders.push(`${rel(file)}: ${rule.selector}`);
      }
    }
    // Keep a minimum scan inventory.
    expect(painted, "no chrome-painting rule found — the scan is broken").toBeGreaterThanOrEqual(4);
    expect(
      offenders,
      "these rules paint the chrome plane without declaring it, so marks " +
        "inside them blend toward the page instead:\n" + offenders.join("\n"),
    ).toEqual([]);
  });

  it("sets --den-plane to a plane and nothing else", () => {
    // Plane declarations accept only page or chrome surfaces.
    const offenders: string[] = [];
    let declared = 0;
    for (const file of walk(DEN_SRC, [".css", ".ts", ".tsx"])) {
      if (file.includes(".test.")) continue;
      const body = readSourceText(file, "utf8");
      for (const m of body.matchAll(/--den-plane:\s*([^;\n]+)/g)) {
        declared++;
        const value = m[1]!.trim();
        if (PLANES.includes(value)) continue;
        offenders.push(`${rel(file)}: --den-plane: ${value}`);
      }
    }
    // Count the root, chrome regions, and rail cards.
    expect(declared, "the plane is declared nowhere — the wiring is gone").toBeGreaterThanOrEqual(7);
    expect(offenders, offenders.join("\n")).toEqual([]);
  });

  it("blends every rail mark into the plane, not a fixed surface", () => {
    // Rail marks blend against their containing plane.
    const MARKS: ReadonlyArray<readonly [string, string, string]> = [
      [
        "global-components.css",
        ".den-shell-aside-main > .den-shell-nav-dock::after",
        "scroll wash above the dock",
      ],
      ["global-components.css", ".den-nav-marker", "selection dot cutout ring"],
      ["global-components.css", ".project-folders__tile", "folder pile ring and chip fills"],
      [
        "global-components.css",
        ".den-shell-dock-link:focus-visible",
        "focus halo on the dock links",
      ],
    ];
    for (const [file, selector, what] of MARKS) {
      const block = ruleBlock(readSourceText(join(DEN_SRC, file), "utf8"), selector);
      expect(block, `${selector} is gone — ${what} needs a new home`).toBeTruthy();
      expect(block, `${what} must blend into var(--den-plane)`).toContain(
        "var(--den-plane)",
      );
      for (const plane of PLANES) {
        expect(
          block,
          `${what} names ${plane} directly, so it is wrong on the other plane`,
        ).not.toContain(plane);
      }
    }
  });
});

describe("theme token coverage", () => {
  it("has no color literal of any notation outside the generated token sheet", () => {
    const hits: string[] = [];
    for (const file of walk(DEN_SRC, [".css"])) {
      if (file.endsWith(GENERATED_TOKEN_SHEET)) continue;
      hits.push(...colorLiteralLines(readSourceText(file, "utf8"), rel(file)));
    }
    expect(
      hits,
      "a literal survives a theme swap and paints the old palette onto the " +
        `new one — use a var(--den-*) token:\n${hits.join("\n")}`,
    ).toEqual([]);
  });

  it("has no paint literal in inline SVG", () => {
    // Scan only SVG paint attributes.
    const PAINTS = /\b(?:fill|stroke|stop-color|flood-color|lighting-color)\s*=/;
    const hits: string[] = [];
    for (const file of walk(DEN_SRC, [".tsx", ".ts"])) {
      if (file.includes(".test.")) continue;
      const body = readSourceText(file, "utf8");
      body.split("\n").forEach((line, i) => {
        if (!PAINTS.test(line)) return;
        if (!COLOR_LITERAL.test(line.replace(/var\([^)]*\)/g, " "))) return;
        hits.push(`${rel(file)}:${i + 1}: ${line.trim()}`);
      });
    }
    expect(
      hits,
      "a painted literal survives a theme swap — use currentColor or a " +
        `var(--den-*) token:\n${hits.join("\n")}`,
    ).toEqual([]);
  });

  it("resolves every var(--den-*) fallback to a token that exists", () => {
    // Readers may fall back only to declared tokens.
    const defined = new Set<string>();
    for (const file of walk(DEN_SRC, [".css"])) {
      // CSS declarations define tokens.
      for (const m of readSourceText(file, "utf8").matchAll(
        /^\s*(--den-[a-z0-9-]+)\s*:/gm,
      )) {
        defined.add(m[1]!);
      }
    }
    // TypeScript also publishes runtime tokens.
    for (const file of walk(DEN_SRC, [".ts", ".tsx"])) {
      // Inline styles define runtime tokens.
      for (const m of readSourceText(file, "utf8").matchAll(
        /(?:setProperty\(\s*|style\[\s*)?[`"'](--den-[a-z0-9-]+)[`"']\s*(?:[,\]:])/g,
      )) {
        defined.add(m[1]!);
      }
    }
    for (const cssVar of Object.values(SYNTAX_SCOPE_VARS)) {
      for (const flag of ["style", "weight", "decoration"]) {
        defined.add(`${cssVar}-${flag}`);
      }
    }

    const dangling: string[] = [];
    for (const file of walk(DEN_SRC, [".css", ".ts", ".tsx"])) {
      if (file.includes(".test.")) continue;
      const body = readSourceText(file, "utf8");
      for (const m of body.matchAll(/var\(\s*(--den-[a-z0-9-]+)\s*[,)]/g)) {
        if (!defined.has(m[1]!)) dangling.push(`${rel(file)}: ${m[1]}`);
      }
    }
    expect(
      [...new Set(dangling)],
      `these custom properties are read but never defined:\n${dangling.join("\n")}`,
    ).toEqual([]);
  });

  it("defines every vocabulary token in the generated sheet", () => {
    // Generated tokens have stylesheet declarations.
    const sheet = readSourceText(join(DEN_SRC, GENERATED_TOKEN_SHEET), "utf8");
    const highlight = readSourceText(
      join(DEN_SRC, "components/source/editor/codemirror-highlight.generated.ts"),
      "utf8",
    );
    const missing = [
      ...Object.values(THEME_TOKEN_VARS),
      ...Object.values(SYNTAX_SCOPE_VARS),
    ].filter((cssVar) => {
      if (sheet.includes(`${cssVar}:`)) return false;
      // Style scopes publish flags through the highlight sheet.
      return !highlight.includes(`${cssVar}-style`);
    });
    expect(missing, `absent from ${GENERATED_TOKEN_SHEET}`).toEqual([]);
  });

  it("carries both schemes' base tokens on the appearance attribute", () => {
    // Appearance attributes carry the active scheme's tokens.
    const sheet = readSourceText(join(DEN_SRC, GENERATED_TOKEN_SHEET), "utf8");
    const lightAt = sheet.indexOf(':root[data-den-appearance="light"]');
    const darkAt = sheet.indexOf(':root[data-den-appearance="dark"]');
    expect(lightAt, "no light appearance block").toBeGreaterThan(-1);
    expect(darkAt, "no dark appearance block").toBeGreaterThan(-1);

    const lightBlock = sheet.slice(lightAt, darkAt);
    const darkBlock = sheet.slice(darkAt);
    for (const cssVar of Object.values(THEME_TOKEN_VARS)) {
      expect(lightBlock, `${cssVar} absent from the light block`).toContain(
        `${cssVar}:`,
      );
      expect(darkBlock, `${cssVar} absent from the dark block`).toContain(
        `${cssVar}:`,
      );
    }
    // Schemes paint distinct backgrounds.
    expect(
      /--den-background:\s*(\S+);/.exec(lightBlock)?.[1],
    ).not.toBe(/--den-background:\s*(\S+);/.exec(darkBlock)?.[1]);
  });

  it("keeps the dark scheme on the appearance attribute, not a media query", () => {
    // Only the cold-start default may read the system scheme.
    const offenders: string[] = [];
    for (const file of walk(DEN_SRC, [".css"])) {
      if (file.endsWith(GENERATED_TOKEN_SHEET)) continue;
      const body = readSourceText(file, "utf8");
      if (!body.includes("prefers-color-scheme")) continue;
      // Cold-start rules defer once an appearance attribute exists.
      const coldStartOnly = body
        .split("@media (prefers-color-scheme: dark)")
        .slice(1)
        .every((block) =>
          /^\s*\{?\s*:root:not\(\[data-den-appearance\]\)/.test(block),
        );
      if (coldStartOnly) continue;
      offenders.push(rel(file));
    }
    expect(
      offenders,
      "these sheets branch on the OS scheme instead of the applied theme:\n" +
        offenders.join("\n"),
    ).toEqual([]);
  });

  it("seats tracks on their planes and marks the chosen item on each", () => {
    const browse = readSourceText(join(DEN_SRC, "browse-stage-domain.css"), "utf8");
    const files = readSourceText(join(DEN_SRC, "files-domain.css"), "utf8");
    const chrome = readSourceText(join(DEN_SRC, "global-components.css"), "utf8");

    expect(extractCssRuleBlock(browse, ".den-browse-segmented")).toMatch(
      /background:\s*var\(--den-surface-offset\)/,
    );
    // The selected segment uses the raised surface.
    expect(
      extractCssRuleBlock(browse, ".den-browse-segment-thumb__fill"),
    ).toMatch(/background:\s*var\(--den-surface-raised\)/);
    expect(extractCssRuleBlock(files, ".den-files-tab-strip")).toMatch(
      /background:\s*linear-gradient\([\s\S]*var\(--den-background\)[\s\S]*var\(--den-line\)[\s\S]*var\(--den-surface-inset\)/,
    );
    expect(extractCssRuleBlock(files, ".den-files-tab--active .den-files-tab__body")).toMatch(
      /background:\s*var\(--den-current-window-selection,\s*var\(--den-selection\)\)/,
    );
    expect(
      extractCssRuleBlock(
        chrome,
        ".tabs__rail:not(.tabs__rail--compact) .tabs__chip--shown",
      ),
    ).toMatch(/background:\s*transparent/);
    expect(extractCssRuleBlock(chrome, ".tabs__panel-clip")).toMatch(
      /background:\s*var\(--den-background\)/,
    );

    expect(browse).not.toMatch(
      /data-den-appearance="dark"[\s\S]{0,120}\.den-browse-segmented/,
    );
    expect(chrome).not.toMatch(
      /data-den-appearance="dark"[\s\S]{0,200}\.tabs__chip/,
    );
  });
});

describe("finding severity ramp", () => {
  const derived = readSourceText(join(DEN_SRC, "tokens-derived.css"), "utf8");
  const LEVELS = ["critical", "high", "medium", "low", "info"] as const;

  it("defines a colour and a wash for every severity the gutter can render", () => {
    for (const level of LEVELS) {
      expect(derived).toContain(`--den-finding-${level}:`);
      expect(derived).toContain(`--den-finding-${level}-wash:`);
    }
  });

  it("derives every step by reference, so appearance changes carry", () => {
    // Derived levels reference theme tokens.
    for (const level of LEVELS) {
      for (const suffix of ["", "-wash"]) {
        const decl = new RegExp(
          `--den-finding-${level}${suffix}:\\s*([^;]+);`,
        ).exec(derived);
        expect(decl, `--den-finding-${level}${suffix} is not declared`).toBeTruthy();
        const value = decl![1]!;
        expect(value, `--den-finding-${level}${suffix} = ${value}`).toMatch(
          /var\(--den-/,
        );
        expect(value).not.toMatch(/#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/);
      }
    }
  });
});
