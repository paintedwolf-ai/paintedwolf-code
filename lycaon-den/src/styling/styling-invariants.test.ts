import { readSourceText, stylesheetDependencies } from "../test/stylesheet-source.ts";
import { execSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";
import { BTN_UTILITY_PREFIX, FORBIDDEN_COMPONENT_BEM_PREFIXES } from "../test/style-contracts/catalog.ts";
import { REQUIRED_THEME_COLOR_KEYS, REQUIRED_THEME_SPACING_KEYS, REQUIRED_THEME_TEXT_KEYS, findHardcodedHexLines, findUnexpectedComponentCssImports, findUndefinedThemeRadiusReferences, themeKeyValues } from "../test/style-contracts/theme.ts";
import { buildStyleHookRegistry, findForbiddenDomainSelectorRoots, findHookCoverageGaps, findOrphanDomainSelectors, findPrimitiveRecipeDrift, findUnprefixedDenHookGaps, isBtnUtilityHook, isDenDomainHook, isHookCovered } from "../test/style-contracts/hook-coverage.ts";
import { collectComponentClassHooks, findAlwaysCoOccurringHooks } from "../test/style-contracts/source-hooks.ts";
import { countBemSelectors, extractTailwindUtilities } from "../test/style-contracts/stylesheet-parser.ts";
import { extractAnimationNames, extractKeyframeNames, findIncompleteInfiniteSpinKeyframes, findKeyframesInUtilitiesCss, findRepaintingInfiniteAnimations } from "../test/style-contracts/animation.ts";
import { extractClassHooksFromSource } from "../test/style-contracts/jsx-classes.ts";
import { findBareLayoutDomainRules, findStageRootBackdropFills, findUniversalPseudoElementRules } from "../test/style-contracts/layout.ts";
import { findButtonRecipeBoxContests, findLayerShadowedUtilities, findDuplicateUtilityDefinitions, findDynamicModifierUtilityViolations, findMissingDynamicModifierDomainRules } from "../test/style-contracts/utilities.ts";
import { findNativeTooltipAttributes } from "../test/style-contracts/browser.ts";
import { readDenStylesheetInventory, walkFiles } from "../test/style-contracts/inventory.ts";

const denSrc = join(dirname(fileURLToPath(import.meta.url)), "..");
const denRoot = join(denSrc, "..");

function componentTsxFiles(): string[] {
  const app = join(denSrc, "App.tsx");
  const files = ["components", "files"].flatMap((dir) =>
    walkFiles(join(denSrc, dir), (p) => p.endsWith(".tsx") && !p.endsWith(".test.tsx")));
  if (existsSync(app)) files.push(app);
  return files.sort();
}

function readText(path: string): string {
  return readSourceText(path, "utf8");
}

describe("styling invariant helpers", () => {
  it("extractClassHooksFromSource ignores data-testid values", () => {
    const hooks = extractClassHooksFromSource(`
      <section
        class="den-chat-stream-body"
        classList={{ "den-chat-stream-body--wide": true }}
        data-testid="den-chat-stream-body-testid"
      />
    `);
    expect(hooks).toContain("den-chat-stream-body");
    expect(hooks).toContain("den-chat-stream-body--wide");
    expect(hooks).not.toContain("den-chat-stream-body-testid");
  });

  it("isHookCovered rejects TSX/CSS root rename drift (shell-nav class)", () => {
    const wrongRegistry = new Set(["shell-nav-link", "shell-nav"]);
    expect(isHookCovered("den-shell-nav-link", wrongRegistry)).toBe(false);
  });

  it("isHookCovered accepts variant suffix hooks when an ancestor token is styled", () => {
    const registry = new Set(["den-shell-nav-sidebar-btn", "den-approval-card"]);
    expect(isHookCovered("den-shell-nav-sidebar-btn-collapse", registry)).toBe(true);
    expect(isHookCovered("den-approval-card-command", registry)).toBe(true);
  });

  it("findAlwaysCoOccurringHooks joins a forwarded class to the component's base", () => {
    const root = mkdtempSync(join(tmpdir(), "den-styling-"));
    writeFileSync(
      join(root, "Segmented.tsx"),
      `export function Segmented(props) {
         return <div class={cn("den-seg", props.class)} />;
       }`,
    );
    writeFileSync(
      join(root, "Panel.tsx"),
      `export function Panel() {
         return <Segmented class="den-panel-seg" />;
       }`,
    );
    const together = findAlwaysCoOccurringHooks(root);
    expect([...(together.get("den-panel-seg") ?? [])]).toEqual(["den-seg"]);
  });

  it("findAlwaysCoOccurringHooks keeps conditional and one-sided classes out", () => {
    const root = mkdtempSync(join(tmpdir(), "den-styling-"));
    writeFileSync(
      join(root, "Rows.tsx"),
      `export function Rows() {
         return (
           <>
             <div class="den-row den-first" classList={{ "den-lit": on() }} />
             <div class="den-row" />
           </>
         );
       }`,
    );
    const together = findAlwaysCoOccurringHooks(root);
    // A classList entry is a condition, and den-row also appears without den-first.
    expect(together.get("den-row")).toBeUndefined();
    expect([...(together.get("den-first") ?? [])]).toEqual(["den-row"]);
    expect([...(together.get("den-lit") ?? [])].sort()).toEqual(["den-first", "den-row"]);
  });

  it("findLayerShadowedUtilities flags a utility buried through a neighbour class", () => {
    const root = mkdtempSync(join(tmpdir(), "den-styling-"));
    writeFileSync(join(root, "tailwind.css"), "@utility den-panel-seg {\n  flex-wrap: nowrap;\n}\n");
    writeFileSync(join(root, "seg-domain.css"), ".den-seg {\n  flex-wrap: wrap;\n}\n");
    writeFileSync(
      join(root, "Segmented.tsx"),
      `export function Segmented(props) {
         return <div class={cn("den-seg", props.class)} />;
       }`,
    );
    writeFileSync(
      join(root, "Panel.tsx"),
      `export function Panel() {
         return <Segmented class="den-panel-seg" />;
       }`,
    );
    expect(findLayerShadowedUtilities(root)).toEqual([
      "tailwind.css: @utility den-panel-seg cannot set flex-wrap — unlayered .den-seg (seg-domain.css) wins",
    ]);
  });

  it("isHookCovered accepts modifier, suffix, and descendant-namespace hooks", () => {
    const registry = new Set([
      "den-checkpoint-card",
      "den-shell-nav-link",
      "den-citation-grounding-headline",
      "den-tool-part",
      "den-draft-rail",
    ]);
    expect(isHookCovered("den-checkpoint-card--content-apply", registry)).toBe(true);
    expect(isHookCovered("den-shell-nav-link-row", registry)).toBe(true);
    expect(isHookCovered("den-citation-grounding", registry)).toBe(true);
    expect(isHookCovered("den-tool-part-name", registry)).toBe(true);
    expect(isHookCovered("den-missing-hook", registry)).toBe(false);
  });
});

describe("Tailwind adoption invariants", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("keeps Tailwind and PostCSS configuration out of JS config files", () => {
    expect(existsSync(join(denRoot, "tailwind.config.js"))).toBe(false);
    expect(existsSync(join(denRoot, "tailwind.config.ts"))).toBe(false);
    expect(existsSync(join(denRoot, "postcss.config.js"))).toBe(false);
    expect(existsSync(join(denRoot, "postcss.config.cjs"))).toBe(false);
  });

  it("leaves vendor prefixes to the webview CSS targets", () => {
    const prefixed = /-webkit-(?:user-select|box-decoration-break|text-decoration|mask-[a-z-]+|backdrop-filter)\s*:/;
    const offenders = walkFiles(denSrc, (p) => p.endsWith(".css"))
      .filter((path) => prefixed.test(readText(path)));
    expect(offenders).toEqual([]);
  });

  it("wires Tailwind v4 through vite before vite-plugin-solid", () => {
    const vite = readText(join(denRoot, "vite.config.ts"));
    const tailAt = vite.indexOf("tailwindcss()");
    const solidAt = vite.indexOf("solid()");
    expect(tailAt).toBeGreaterThan(-1);
    expect(solidAt).toBeGreaterThan(-1);
    expect(tailAt).toBeLessThan(solidAt);
  });

  it("keeps the documented global stylesheet import order in index.tsx", () => {
    const index = readText(join(denSrc, "index.tsx"));
    const tokensAt = index.indexOf('import "./tokens.generated.css"');
    const derivedAt = index.indexOf('import "./tokens-derived.css"');
    expect(derivedAt).toBeGreaterThan(tokensAt);
    const tailwindAt = index.indexOf('import "./tailwind.css"');
    expect(tailwindAt).toBeGreaterThan(derivedAt);
    const globalAt = index.indexOf('import "./global.css"');
    const markdownAt = index.indexOf('import "./markdown.css"');
    const scrollbarsAt = index.indexOf('import "./platform/themed-scrollbars.css"');
    expect(tokensAt).toBeGreaterThan(-1);
    expect(tailwindAt).toBeGreaterThan(tokensAt);
    expect(globalAt).toBeGreaterThan(tailwindAt);
    expect(markdownAt).toBeGreaterThan(globalAt);
    expect(scrollbarsAt).toBeGreaterThan(markdownAt);
    expect(readText(join(denSrc, "App.tsx"))).not.toMatch(/App\.css/);
  });

  it("imports every domain stylesheet from tailwind.css", () => {
    const { domainCssFiles } = readDenStylesheetInventory(denSrc);
    const reachable = stylesheetDependencies(join(denSrc, "tailwind.css"));
    const missing = domainCssFiles.filter((path) => !reachable.has(path));
    expect(missing, `tailwind.css missing @import for: ${missing.join(", ")}`).toEqual([]);
  });

  it("imports Tailwind theme and utilities around the Den element reset", () => {
    const tailwind = readFileSync(join(denSrc, "tailwind.css"), "utf8");
    // The bare entry would re-add preflight and its universal pseudo-element rule.
    expect(tailwind).not.toMatch(/@import\s+"tailwindcss"\s*;/);
    expect(tailwind).toMatch(/@import\s+"tailwindcss\/theme\.css"\s+layer\(theme\)/);
    expect(tailwind).toMatch(/@import\s+"\.\/element-reset\.css"\s+layer\(base\)/);
    expect(tailwind).toMatch(/@import\s+"tailwindcss\/utilities\.css"\s+layer\(utilities\)/);
  });

  it("never keys a pseudo-element rule on every element", () => {
    const { cssFiles } = readDenStylesheetInventory(denSrc);
    const violations = cssFiles.flatMap((file) =>
      findUniversalPseudoElementRules(readText(file), file.slice(denSrc.length + 1)),
    );
    expect(violations).toEqual([]);
    expect(findUniversalPseudoElementRules("*, *::before, *::after { box-sizing: border-box; }", "x.css")).toEqual([
      "x.css:1: *::before",
      "x.css:1: *::after",
    ]);
    expect(findUniversalPseudoElementRules("@media (a) {\n  ::after { color: red; }\n}", "x.css")).toEqual([
      "x.css:2: ::after",
    ]);
    expect(
      findUniversalPseudoElementRules(".x::before, :where(.y)::after, input::placeholder, .z > ::-webkit-scrollbar { }", "x.css"),
    ).toEqual([]);
  });

  it("bridges brand tokens in @theme by reference, not hardcoded palette", () => {
    const tailwind = readText(join(denSrc, "tailwind.css"));
    expect(tailwind).toMatch(/@theme\s*\{/);
    for (const key of REQUIRED_THEME_COLOR_KEYS) {
      expect(tailwind).toContain(key);
    }
  });

  it("@theme color entries reference --den-* variables", () => {
    const themeBlock = readText(join(denSrc, "tailwind.css")).match(/@theme\s*\{([\s\S]*?)\n\}/)?.[1] ?? "";
    const colorLines = themeBlock
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l.startsWith("--color-den-"));
    expect(colorLines.length).toBeGreaterThan(0);
    for (const line of colorLines) {
      expect(line).toMatch(/var\(--den-/);
      expect(line).not.toMatch(/#[0-9a-fA-F]{3,8}/);
    }
  });

  it("bridges Den spacing and type scale keys in @theme", () => {
    const tailwind = readText(join(denSrc, "tailwind.css"));
    for (const key of REQUIRED_THEME_SPACING_KEYS) {
      expect(tailwind).toContain(key);
    }
    for (const key of REQUIRED_THEME_TEXT_KEYS) {
      expect(tailwind).toContain(key);
    }
  });

  it("keeps every Tailwind radius reference on the canonical concentric ladder", () => {
    const tailwind = readText(join(denSrc, "tailwind.css"));
    expect(tailwind).toContain("--radius-den-sm: var(--den-radius-sm)");
    expect(tailwind).toContain("--radius-den: var(--den-radius)");
    expect(tailwind).toContain("--radius-den-lg: var(--den-radius-lg)");
    expect(findUndefinedThemeRadiusReferences(denSrc)).toEqual([]);
  });

  it("keeps ordinary component styling in Tailwind rather than local CSS imports", () => {
    expect(findUnexpectedComponentCssImports(denSrc)).toEqual([]);
  });

  it("@theme spacing and type scale use literal rem", () => {
    const tailwind = readText(join(denSrc, "tailwind.css"));
    const spacing = themeKeyValues(tailwind, "--spacing-den-");
    const text = themeKeyValues(tailwind, "--text-den-");
    expect([...spacing.keys()].sort()).toEqual([...REQUIRED_THEME_SPACING_KEYS].sort());
    expect([...text.keys()].sort()).toEqual([...REQUIRED_THEME_TEXT_KEYS].sort());
    for (const value of spacing.values()) {
      expect(value).toMatch(/^\d+(\.\d+)?rem$/);
      expect(value).not.toMatch(/#[0-9a-fA-F]{3,8}/);
      expect(value).not.toMatch(/^var\(/);
    }
    for (const value of text.values()) {
      expect(value).toMatch(/^\d+(\.\d+)?rem$/);
      expect(value).not.toMatch(/#[0-9a-fA-F]{3,8}/);
      expect(value).not.toMatch(/^var\(/);
      expect(value).not.toMatch(/px$/);
    }
  });

  it("enables eslint-plugin-tailwindcss against tailwind.css with cn()", () => {
    const eslint = readText(join(denRoot, "eslint.config.js"));
    expect(eslint).toMatch(/eslint-plugin-tailwindcss/);
    expect(eslint).toMatch(/cssConfigPath:\s*"\.\/src\/tailwind\.css"/);
    expect(eslint).toMatch(/functions:\s*\[\s*"cn"\s*\]/);
    expect(eslint).toMatch(/tailwindcss\/no-contradicting-classname":\s*"error"/);
  });

  it("keeps reserved BEM prefixes out of component hooks", () => {
    const pattern = FORBIDDEN_COMPONENT_BEM_PREFIXES.map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(
      "|",
    );
    const matches = execSync(
      `rg '${pattern}' ${join(denSrc, "components")} ${join(denSrc, "files")} --glob '*.tsx' 2>/dev/null || true`,
      { encoding: "utf8", cwd: denRoot },
    ).trim();
    expect(matches).toBe("");
  });

  it("domain CSS files do not use forbidden bare selector roots", () => {
    const { domainCssFiles } = readDenStylesheetInventory(denSrc);
    const violations: string[] = [];
    for (const file of domainCssFiles) {
      const rel = file.slice(denSrc.length + 1);
      violations.push(...findForbiddenDomainSelectorRoots(readText(file), rel).map((v) => v.line));
    }
    expect(violations).toEqual([]);
  });

  it("does not force UI copy out of sentence case", () => {
    const { cssFiles } = readDenStylesheetInventory(denSrc);
    const violations = cssFiles.flatMap((file) => {
      const rel = file.slice(denSrc.length + 1);
      return readText(file)
        .split("\n")
        .flatMap((line, index) =>
          /text-transform:\s*(?:uppercase|lowercase|capitalize)/u.test(line)
            ? [`${rel}:${index + 1}`]
            : [],
        );
    });
    expect(violations).toEqual([]);
  });

  it("animation names in project CSS resolve to @keyframes", () => {
    const { cssFiles } = readDenStylesheetInventory(denSrc);
    let css = "";
    for (const file of cssFiles) {
      if (file.includes("overlayscrollbars")) continue;
      css += readText(file);
      css += "\n";
    }
    const keyframes = extractKeyframeNames(css);
    const animations = extractAnimationNames(css);
    const missing = [...new Set(animations)].filter((name) => !keyframes.has(name)).sort();
    expect(missing, `missing @keyframes for: ${missing.join(", ")}`).toEqual([]);
  });

  it("every den-* and btn-* class hook in components resolves to CSS or @utility", () => {
    const registry = buildStyleHookRegistry(denSrc);
    const byFile = collectComponentClassHooks(componentTsxFiles());
    const gaps = findHookCoverageGaps(
      byFile,
      registry,
      (hook) => isDenDomainHook(hook) || isBtnUtilityHook(hook),
    );
    if (gaps.length > 0) {
      const sample = gaps
        .slice(0, 12)
        .map((g) => `${g.hook} (${g.file.slice(denSrc.length + 1)})`)
        .join("\n");
      expect.fail(
        `${gaps.length} class hook(s) have no matching selector or @utility — likely TSX/CSS rename drift:\n${sample}`,
      );
    }
  });

  it("domain hooks use den-* names when matching prefixed selectors exist", () => {
    const registry = buildStyleHookRegistry(denSrc);
    const drift = findUnprefixedDenHookGaps(
      collectComponentClassHooks(componentTsxFiles()),
      registry,
    );
    if (drift.length > 0) {
      const sample = drift
        .slice(0, 12)
        .map((g) => `${g.hook} → use den-${g.hook} (${g.file.slice(denSrc.length + 1)})`)
        .join("\n");
      expect.fail(
        `${drift.length} hook(s) omit the den- prefix while den-* CSS exists:\n${sample}`,
      );
    }
  });

  it("shared btn @utility helpers stay defined in tailwind.css", () => {
    const utilities = extractTailwindUtilities(readText(join(denSrc, "tailwind.css")));
    const usedBtn = new Set<string>();
    for (const hooks of collectComponentClassHooks(componentTsxFiles()).values()) {
      for (const hook of hooks) {
        if (hook.startsWith(BTN_UTILITY_PREFIX)) usedBtn.add(hook);
      }
    }
    const missing = [...usedBtn].filter((u) => !utilities.has(u)).sort();
    expect(missing, `undefined btn utilities: ${missing.join(", ")}`).toEqual([]);
  });

  it("settings stage column stays width:100% so container-type editors do not collapse", () => {
    const css = readText(join(denSrc, "tailwind.css"));
    for (const name of [
      "den-settings-view",
      "den-settings-view-body",
      "den-settings-panel",
      "den-settings-editor",
    ] as const) {
      const block = css.match(
        new RegExp(`@utility\\s+${name}\\s*\\{([\\s\\S]*?)\\n\\}`),
      )?.[1];
      expect(block, `${name} @utility missing`).toBeTruthy();
      expect(block, `${name} must set width: 100%`).toMatch(/width:\s*100%/);
    }
    expect(css).toMatch(
      /@utility\s+den-settings-editor\s*\{[\s\S]*?container-type:\s*inline-size/,
    );
  });

  it("no stage root repaints the backdrop over the wash", () => {
    expect(findStageRootBackdropFills(denSrc)).toEqual([]);
  });

  it("shell-domain keeps no per-stage wash transparency list", () => {
    const shell = readText(join(denSrc, "shell-domain.css"));
    expect(shell).not.toMatch(/den-shell-stage-host--washed\s+\.project-/);
  });

  it("locks SaaS full-bleed stage tokens and editor inset", () => {
    const globalCss = readText(join(denSrc, "global.css"));
    expect(globalCss).toMatch(/--den-stage-inset-x:\s*36px/);
    expect(globalCss).toMatch(/--den-stage-inset-y:\s*4px/);
    expect(globalCss).toMatch(/--den-prose-measure:\s*40rem/);
    expect(globalCss).not.toMatch(/--den-stage-measure:/);
    expect(globalCss).toMatch(/--den-radius-shell:\s*0/);
    expect(globalCss).toMatch(/--den-radius-pill:\s*999px/);
    expect(globalCss).toMatch(/--den-nav-hover-bg:/);
    expect(globalCss).not.toMatch(/--den-radius-md\b/);

    const tw = readText(join(denSrc, "tailwind.css"));
    expect(tw).not.toMatch(/--den-radius-md\b/);
    expect(tw).toMatch(
      /@utility\s+den-settings-editor\s*\{[\s\S]*?padding:\s*var\(--den-stage-inset-y\)\s+var\(--den-stage-inset-x\)\s+var\(--spacing-den-8\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-section\s*\{[\s\S]*?& > \.den-settings-pref-group\s*\{[\s\S]*?margin-bottom:\s*0/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-list-panel\s*\{[\s\S]*?border-radius:\s*var\(--den-radius-shell\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-prefs-band\s*\{[\s\S]*?border-radius:\s*var\(--den-radius-shell\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-approvals-posture-card\s*\{[\s\S]*?border-radius:\s*var\(--den-radius-sm\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-keyboard-list\s*\{[\s\S]*?border-radius:\s*var\(--den-radius-shell\)/,
    );

    const globalComponents = readText(join(denSrc, "global-components.css"));
    expect(globalComponents).toMatch(
      /\.den-list-panel\s*\{[\s\S]*?border-radius:\s*var\(--den-radius-shell\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-list-panel\s*\{[\s\S]*?background:\s*var\(--den-background\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-prefs-band\s*\{[\s\S]*?background:\s*var\(--den-background\)/,
    );
    expect(tw).toMatch(
      /@utility\s+den-settings-panel\s*\{[\s\S]*?max-width:\s*none/,
    );

    const browse = readText(join(denSrc, "browse-stage-domain.css"));
    expect(browse).toMatch(
      /\.den-browse-chrome\s*\{[\s\S]*?padding:\s*var\(--den-stage-inset-y\)\s+var\(--den-stage-inset-x\)/,
    );
  });

  it("composes every bevel from the theme's derived edge colors", () => {
    // Theme edge colors determine bevel strength.
    const globalCss = readText(join(denSrc, "global.css"));
    const darkAt = globalCss.indexOf(':root[data-den-appearance="dark"]');
    expect(darkAt).toBeGreaterThan(-1);
    const lightTheme = globalCss.slice(0, darkAt);
    const darkTheme = globalCss.slice(darkAt);

    for (const [token, litEdge] of [
      ["--den-bevel", "var(--den-bevel-highlight)"],
      ["--den-bevel-inset", "var(--den-bevel-highlight)"],
      ["--den-bevel-plate", "var(--den-bevel-plate-highlight)"],
    ] as const) {
      const value = new RegExp(`${token}:\\s*([^;]+);`).exec(lightTheme)?.[1];
      expect(value, `${token} must be defined`).toBeTruthy();
      expect(value, `${token} must draw its lit edge from the theme`).toContain(litEdge);
      expect(
        value,
        `${token} must draw its shaded edge from the theme`,
      ).toContain("var(--den-bevel-shadow)");
    }
    expect(
      darkTheme,
      "the bevel's strength belongs to the theme, not an appearance block",
    ).not.toMatch(/--den-bevel(?:-inset|-plate)?\s*:/);

    const cssFiles = walkFiles(denSrc, (path) => path.endsWith(".css"));
    const handRolled = cssFiles
      .filter((path) => path !== join(denSrc, "global.css"))
      .filter((path) =>
        /var\(--den-bevel-(?:highlight|shadow|plate-highlight)\)/.test(readText(path)),
      );
    expect(
      handRolled,
      "these sheets draw a bevel edge directly instead of using " +
        "var(--den-bevel), var(--den-bevel-inset), or var(--den-bevel-plate)",
    ).toEqual([]);
  });

  it("keeps every elevation shadow in the dark-aware theme registry", () => {
    const globalCss = readText(join(denSrc, "global.css"));
    const darkAt = globalCss.indexOf(':root[data-den-appearance="dark"]');
    expect(darkAt).toBeGreaterThan(-1);
    const lightTheme = globalCss.slice(0, darkAt);
    const darkTheme = globalCss.slice(darkAt);
    const shadowTokens = new Set(
      [...lightTheme.matchAll(/(--den-shadow-[a-z0-9-]+)\s*:/g)].map((match) => match[1]),
    );
    expect(shadowTokens.size).toBeGreaterThan(0);
    for (const token of shadowTokens) {
      const darkValue = new RegExp(`${token}\\s*:\\s*([^;]+);`).exec(darkTheme)?.[1];
      expect(darkValue, `${token} needs a dark-mode value`).toBeTruthy();
      expect(
        darkValue,
        `${token} must cast the theme's shadow ink, not a literal`,
      ).toMatch(/var\(--den-shadow\)/);
    }

    const cssFiles = walkFiles(denSrc, (path) => path.endsWith(".css"));
    const shadowReferences = new Set(
      cssFiles.flatMap((path) =>
        [...readText(path).matchAll(/var\((--den-shadow-[a-z0-9-]+)/g)].map((match) => match[1]),
      ),
    );
    expect([...shadowReferences].filter((token) => !shadowTokens.has(token))).toEqual([]);

    const externalDefinitions = cssFiles
      .filter((path) => path !== join(denSrc, "global.css"))
      .filter((path) => /--den-shadow-[a-z0-9-]+\s*:/.test(readText(path)));
    expect(externalDefinitions).toEqual([]);

    const componentShadow = /box-shadow\s*:\s*[^;{}]*(?:color-mix\([^;{}]*var\(--den-text\)|rgba\(|rgb\(0)/;
    const violations = cssFiles
      .filter((path) => path !== join(denSrc, "global.css"))
      .filter((path) => componentShadow.test(readText(path)));
    expect(violations).toEqual([]);

    const elevationFallback = /box-shadow\s*:\s*var\(--den-shadow-[^)]*,/;
    const fallbacks = cssFiles
      .filter((path) => path !== join(denSrc, "global.css"))
      .filter((path) => elevationFallback.test(readText(path)));
    expect(fallbacks).toEqual([]);
  });

  it("non-chat stage header shares the sidebar titlebar top line", () => {
    // Shared height expressions align both header hairlines.
    const shellUtils = readText(join(denSrc, "shell-utilities.css"));
    const shellDomain = readText(join(denSrc, "shell-domain.css"));
    const headerBlock = shellUtils.match(
      /@utility\s+den-shell-header\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(headerBlock, "@utility den-shell-header missing").toBeTruthy();
    expect(headerBlock, "den-shell-header must set the shared top-line min-height").toMatch(
      /min-height:\s*calc\(var\(--den-tabs-top-gap\)\s*\+\s*var\(--den-unified-chrome-row\)\)/,
    );
    const asideTitlebarBlock = shellDomain.match(
      /\.den-shell-aside-titlebar\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(asideTitlebarBlock, ".den-shell-aside-titlebar missing").toBeTruthy();
    expect(asideTitlebarBlock).toMatch(
      /height:\s*calc\(var\(--den-tabs-top-gap\)\s*\+\s*var\(--den-unified-chrome-row\)\)/,
    );
  });

  it("text-entry controls use paper fill, gray only when disabled", () => {
    const tokens = readText(join(denSrc, "tokens-derived.css"));
    expect(tokens).toMatch(/--den-control-bg:\s*var\(--den-background\)/);
    expect(tokens).toMatch(
      /--den-control-bg-disabled:\s*color-mix\(in srgb, var\(--den-text\) 5\.5%, var\(--den-background\)\)/,
    );

    // Select triggers use button styling.
    const tw = readText(join(denSrc, "tailwind.css"));
    for (const name of ["den-input", "den-rules-input"] as const) {
      expect(tw).toMatch(
        new RegExp(
          `@utility\\s+${name}\\s*\\{[\\s\\S]*?background-color:\\s*var\\(--den-control-bg\\)`,
        ),
      );
      expect(tw).toMatch(
        new RegExp(
          `@utility\\s+${name}\\s*\\{[\\s\\S]*?:disabled\\s*\\{[\\s\\S]*?background-color:\\s*var\\(--den-control-bg-disabled\\)`,
        ),
      );
    }
  });

  it("keeps button and field recipe hooks inside primitive files only", () => {
    const drift = findPrimitiveRecipeDrift(
      collectComponentClassHooks(componentTsxFiles()),
      denSrc,
    );
    if (drift.length > 0) {
      const sample = drift
        .slice(0, 12)
        .map((g) => `${g.hook} (${g.file.slice(denSrc.length + 1)})`)
        .join("\n");
      expect.fail(
        `${drift.length} recipe hook(s) outside Den* primitives — use a shared control primitive:\n${sample}`,
      );
    }
  });

  it("uses direct, non-elastic scrolling across panes", () => {
    const css = readText(join(denSrc, "global.css"));
    expect(css).toMatch(/\*\s*\{[^}]*scroll-behavior:\s*auto/);
    expect(css).not.toMatch(/\*\s*\{[^}]*overscroll-behavior:/);
    expect(css).toMatch(/html,\s*body\s*\{[^}]*overscroll-behavior:\s*none/);
    // Containment stops chaining while preserving elastic scrolling.
    const contain = execSync(
      `rg -n 'overscroll-behavior(-[xy])?:\\s*contain|overscrollBehavior[XY]?:\\s*"contain"|overscroll-([xy]-)?contain\\b' ${denSrc} --glob '!*.test.ts' --glob '!*.test.tsx' 2>/dev/null || true`,
      { encoding: "utf8", cwd: denRoot },
    ).trim();
    expect(contain).toBe("");
  });

});

describe("utility-first adoption ratchets", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("domain/art CSS references brand tokens, not hardcoded hex", () => {
    const { domainCssFiles } = readDenStylesheetInventory(denSrc);
    const hits: string[] = [];
    for (const file of domainCssFiles) {
      hits.push(...findHardcodedHexLines(readText(file), file.slice(denSrc.length + 1)));
    }
    expect(
      hits,
      `hardcoded hex in domain CSS — use var(--den-*) tokens:\n${hits.join("\n")}`,
    ).toEqual([]);
  });

  // Color fallbacks define a second palette.
  it("counts a colour in a var() fallback as a hardcoded hex", () => {
    expect(
      findHardcodedHexLines("  color: var(--den-danger, #c0392b);", "probe.css"),
    ).toHaveLength(1);
    expect(
      findHardcodedHexLines(
        "  background: color-mix(in srgb, var(--den-warning, #b45309) 80%, transparent);",
        "probe.css",
      ),
    ).toHaveLength(1);
    // Non-color fallbacks can default optional tokens.
    expect(findHardcodedHexLines("  left: var(--den-nav-slot-width, 0);", "probe.css")).toEqual([]);
    expect(
      findHardcodedHexLines("  animation: reveal var(--den-enter-fade-duration, 160ms) ease-out;", "probe.css"),
    ).toEqual([]);
    expect(
      findHardcodedHexLines("  color: var(--den-attention-tone, var(--den-text-muted));", "probe.css"),
    ).toEqual([]);
  });

  it("has no orphaned den-/btn- domain selectors (dead CSS)", () => {
    const orphans = findOrphanDomainSelectors(denSrc);
    expect(
      orphans,
      `${orphans.length} domain selector(s) referenced nowhere — delete the dead rules:\n${orphans.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps infinite animations on composited properties only", () => {
    const repainting = findRepaintingInfiniteAnimations(denSrc);
    expect(
      repainting,
      `Infinite animations must crossfade/translate a composited overlay instead of animating repaint properties:\n${repainting.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps infinite spin keyframes bounded with explicit start and end frames", () => {
    const unbounded = findIncompleteInfiniteSpinKeyframes(denSrc);
    expect(
      unbounded,
      `Infinite spin animations must specify explicit start (from/0%) and end (to/100%) frames for smooth continuous looping:\n${unbounded.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps @keyframes out of *-utilities.css (retention boundary, both ways)", () => {
    const displaced = findKeyframesInUtilitiesCss(denSrc);
    expect(
      displaced,
      `@keyframes in *-utilities.css — move to the matching *-domain.css / *-art.css:\n${displaced.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps domain CSS at the retention boundary only (no bare layout blocks)", () => {
    const bare = findBareLayoutDomainRules(denSrc);
    expect(
      bare,
      `${bare.length} bare layout rule(s) in *-domain.css — migrate to @utility:\n${bare.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps component selectors out of global.css", () => {
    expect(countBemSelectors(readText(join(denSrc, "global.css")))).toBe(0);
  });

  it("layers global.css button reset so @utility backgrounds win", () => {
    const globalCss = readText(join(denSrc, "global.css"));
    expect(globalCss).toMatch(/@layer base\s*\{[\s\S]*button\s*\{/);
    expect(globalCss).not.toMatch(/^button\s*\{/m);
  });

  it("layers global.css focus-visible so field utilities can opt out", () => {
    const globalCss = readText(join(denSrc, "global.css"));
    expect(globalCss).toMatch(/@layer base\s*\{[\s\S]*:focus-visible\s*\{/);
    expect(globalCss).not.toMatch(/^:focus-visible\s*\{/m);
  });

  it("defines every @utility hook once", () => {
    const duplicates = findDuplicateUtilityDefinitions(denSrc);
    expect(
      duplicates,
      `${duplicates.length} @utility hook(s) split across blocks — merge into one recipe:\n${duplicates.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps @utility declarations out of the shadow of an unlayered rule", () => {
    const shadowed = findLayerShadowedUtilities(denSrc);
    expect(
      shadowed,
      `${shadowed.length} @utility declaration(s) are unreachable — keep one definition per hook, or drive the state from an attribute the unlayered rule reads:\n${shadowed.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps a DenButton's own class out of a box contest with its recipe", () => {
    const contests = findButtonRecipeBoxContests(denSrc);
    expect(
      contests,
      `${contests.length} @utility class(es) size a DenButton against its btn- recipe — same layer, same specificity, Tailwind picks the winner. Move the box onto an unlayered selector the button already carries:\n${contests.join("\n")}`,
    ).toEqual([]);
  });

  it("keeps template BEM modifiers in domain CSS, not @utility shadow rules", () => {
    const utilityViolations = findDynamicModifierUtilityViolations(denSrc);
    expect(
      utilityViolations,
      `@utility modifiers for template classes are dead code — move to *-domain.css:\n${utilityViolations.join("\n")}`,
    ).toEqual([]);
    const missingDomain = findMissingDynamicModifierDomainRules(denSrc);
    expect(
      missingDomain,
      `template class modifiers missing from domain CSS:\n${missingDomain.join("\n")}`,
    ).toEqual([]);
  });

  it("routes every hover tip through TooltipHost, never a native title popup", () => {
    const hits = findNativeTooltipAttributes(denSrc);
    expect(
      hits,
      `${hits.length} native title tooltip(s) — the OS popup ignores Den's theme, delay, and placement. Use data-tip (plus aria-label when the control has no text):\n${hits.join("\n")}`,
    ).toEqual([]);
  });

  it("has no @apply graveyards in Den CSS", () => {
    const hits = execSync(`rg '@apply' ${denSrc} --glob '*.css' 2>/dev/null || true`, {
      encoding: "utf8",
      cwd: denRoot,
    }).trim();
    expect(hits).toBe("");
  });
});
