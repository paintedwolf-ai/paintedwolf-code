import { join } from "node:path";
import { readFileSync } from "node:fs";
import { BTN_UTILITY_PREFIX, FORBIDDEN_DOMAIN_SELECTOR_ROOTS, PRIMITIVE_RECIPE_FILES, PRIMITIVE_RECIPE_HOOKS, UNSTYLED_HOOK_SUFFIXES, type HookCoverageGap } from "./catalog.ts";
import { collectReferencedClassTokens } from "./source-hooks.ts";
import { readDenStylesheetInventory } from "./inventory.ts";
import { extractCssClassSelectors, extractTailwindUtilities } from "./stylesheet-parser.ts";


export function isDenDomainHook(hook: string): boolean {
  return hook.startsWith("den-");
}

export function isBtnUtilityHook(hook: string): boolean {
  return hook.startsWith(BTN_UTILITY_PREFIX);
}

export function hookBaseBeforeModifier(hook: string): string | null {
  const idx = hook.indexOf("--");
  if (idx <= 0) return null;
  return hook.slice(0, idx);
}

export function hookBaseBeforeSuffix(hook: string): string | null {
  for (const suffix of UNSTYLED_HOOK_SUFFIXES) {
    if (hook.endsWith(suffix)) {
      return hook.slice(0, -suffix.length);
    }
  }
  return null;
}

export function hasStyledDescendantNamespace(hook: string, registry: Set<string>): boolean {
  const prefix = `${hook}-`;
  for (const key of registry) {
    if (key.startsWith(prefix)) return true;
  }
  return false;
}

export function findRegistryPrefixBase(hook: string, registry: Set<string>): boolean {
  let cur = hook;
  while (cur.includes("-")) {
    cur = cur.slice(0, cur.lastIndexOf("-"));
    if (registry.has(cur)) return true;
  }
  return false;
}

export function isHookCovered(hook: string, registry: Set<string>): boolean {
  if (registry.has(hook)) return true;

  const modifierBase = hookBaseBeforeModifier(hook);
  if (modifierBase && registry.has(modifierBase)) return true;

  const suffixBase = hookBaseBeforeSuffix(hook);
  if (suffixBase && registry.has(suffixBase)) return true;

  if (hasStyledDescendantNamespace(hook, registry)) return true;

  if (hook.startsWith("den-") && findRegistryPrefixBase(hook, registry)) return true;

  return false;
}

export function isPlainCssHook(hook: string): boolean {
  if (hook.includes("__")) return true;
  if (hook.startsWith("markdown")) return true;
  return false;
}

/** Finds unprefixed hooks with matching den-* selectors. */
export function findUnprefixedDenHookGaps(
  byFile: Map<string, Set<string>>,
  registry: Set<string>,
): HookCoverageGap[] {
  const gaps: HookCoverageGap[] = [];
  for (const [file, hooks] of byFile) {
    for (const hook of hooks) {
      if (hook.startsWith("den-") || hook.startsWith(BTN_UTILITY_PREFIX)) continue;
      if (isPlainCssHook(hook)) continue;
      if (!/^[a-z][a-z0-9-]*$/.test(hook)) continue;
      const denVersion = `den-${hook}`;
      if (registry.has(denVersion)) {
        gaps.push({ hook, file });
      }
    }
  }
  gaps.sort((a, b) => a.hook.localeCompare(b.hook) || a.file.localeCompare(b.file));
  return gaps;
}

export function findHookCoverageGaps(
  byFile: Map<string, Set<string>>,
  registry: Set<string>,
  filter: (hook: string) => boolean,
): HookCoverageGap[] {
  const gaps: HookCoverageGap[] = [];
  for (const [file, hooks] of byFile) {
    for (const hook of hooks) {
      if (!filter(hook)) continue;
      if (!isHookCovered(hook, registry)) {
        gaps.push({ hook, file });
      }
    }
  }
  gaps.sort((a, b) => a.hook.localeCompare(b.hook) || a.file.localeCompare(b.file));
  return gaps;
}

/** Recipe hooks used outside the control primitive files. */
export function findPrimitiveRecipeDrift(
  byFile: Map<string, Set<string>>,
  denSrc: string,
): HookCoverageGap[] {
  const allowed = new Set(
    PRIMITIVE_RECIPE_FILES.map((rel) => join(denSrc, rel)),
  );
  const recipeSet = new Set<string>(PRIMITIVE_RECIPE_HOOKS);
  const gaps: HookCoverageGap[] = [];
  for (const [file, hooks] of byFile) {
    if (allowed.has(file)) continue;
    for (const hook of hooks) {
      if (recipeSet.has(hook)) {
        gaps.push({ hook, file });
      }
    }
  }
  gaps.sort((a, b) => a.hook.localeCompare(b.hook) || a.file.localeCompare(b.file));
  return gaps;
}

/** Top-level selector roots in a domain CSS file (first class token per rule line). */
export function findForbiddenDomainSelectorRoots(
  css: string,
  fileLabel: string,
): { root: string; line: string }[] {
  const hits: { root: string; line: string }[] = [];
  for (const line of css.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed.startsWith(".")) continue;
    const rootMatch = /^\.([a-zA-Z][a-zA-Z0-9_-]*)/.exec(trimmed);
    if (!rootMatch) continue;
    const root = rootMatch[1] ?? "";
    if (root.startsWith("den-")) continue;
    if (FORBIDDEN_DOMAIN_SELECTOR_ROOTS.includes(root as (typeof FORBIDDEN_DOMAIN_SELECTOR_ROOTS)[number])) {
      hits.push({ root, line: `${fileLabel}: ${trimmed}` });
    }
  }
  return hits;
}

function selectorBase(selector: string): string {
  const idx = selector.indexOf("--");
  return idx > 0 ? selector.slice(0, idx) : selector;
}

/** Tests whether any source token shares the selector's base lineage. */
export function isDomainSelectorReferenced(selector: string, tokens: Set<string>): boolean {
  const base = selectorBase(selector);
  for (const token of tokens) {
    const tokenBase = selectorBase(token);
    if (tokenBase === base) return true;
    if (tokenBase.startsWith(`${base}-`)) return true;
    if (base.startsWith(`${tokenBase}-`)) return true;
  }
  return false;
}

/** Finds unreferenced den-/btn- selectors across all stylesheets. */
export function findOrphanDomainSelectors(denSrc: string): string[] {
  const tokens = collectReferencedClassTokens(denSrc);
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const scanned = cssFiles.filter(
    (p) => !p.endsWith(".generated.css") && !p.includes("overlayscrollbars"),
  );
  const selectors = new Set<string>();
  for (const file of scanned) {
    for (const selector of extractCssClassSelectors(readFileSync(file, "utf8"))) {
      if (selector.startsWith("den-") || selector.startsWith(BTN_UTILITY_PREFIX)) {
        selectors.add(selector);
      }
    }
  }
  return [...selectors].filter((s) => !isDomainSelectorReferenced(s, tokens)).sort();
}

export function buildStyleHookRegistry(denSrc: string): Set<string> {
  const { cssFiles, tailwindCss } = readDenStylesheetInventory(denSrc);
  const registry = new Set<string>();
  for (const util of extractTailwindUtilities(tailwindCss)) {
    registry.add(util);
  }
  for (const file of cssFiles) {
    const css = readFileSync(file, "utf8");
    for (const util of extractTailwindUtilities(css)) {
      registry.add(util);
    }
    for (const sel of extractCssClassSelectors(css)) {
      registry.add(sel);
    }
  }
  return registry;
}
