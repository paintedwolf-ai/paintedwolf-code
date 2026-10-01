import { readFileSync } from "node:fs";
import { extractDynamicModifierBases, findAlwaysCoOccurringHooks } from "./source-hooks.ts";
import { readDenStylesheetInventory, walkFiles } from "./inventory.ts";
import { hookBaseBeforeModifier, isBtnUtilityHook } from "./hook-coverage.ts";
import { extractCssClassSelectors, splitStylesheetRules, topLevelDeclaredProps } from "./stylesheet-parser.ts";

/** Finds utilities that cannot match dynamic modifier classes. */
export function findDynamicModifierUtilityViolations(denSrc: string): string[] {
  const dynamicBases = extractDynamicModifierBases(denSrc);
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const hits: string[] = [];
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    const css = readFileSync(file, "utf8");
    for (const match of css.matchAll(/@utility\s+(den-[a-z0-9-]+--[a-z0-9-]+)/g)) {
      const util = match[1] ?? "";
      const base = hookBaseBeforeModifier(util);
      if (base && dynamicBases.has(base)) {
        hits.push(`${rel}: @utility ${util}`);
      }
    }
  }
  return hits.sort();
}

/** Finds utility declarations shadowed by unlayered rules. */
export function findLayerShadowedUtilities(denSrc: string): string[] {
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const unlayered = new Map<string, Array<{ rel: string; props: Set<string> }>>();
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    for (const rule of splitStylesheetRules(readFileSync(file, "utf8"))) {
      const hook = /^\.([a-zA-Z][a-zA-Z0-9_-]*)$/.exec(rule.selector)?.[1];
      if (!hook) continue;
      const rules = unlayered.get(hook) ?? [];
      rules.push({ rel, props: topLevelDeclaredProps(rule.body) });
      unlayered.set(hook, rules);
    }
  }
  const coOccurring = findAlwaysCoOccurringHooks(denSrc);
  const hits: string[] = [];
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    for (const rule of splitStylesheetRules(readFileSync(file, "utf8"))) {
      const util = /^@utility\s+([a-zA-Z][a-zA-Z0-9_-]*)$/.exec(rule.selector)?.[1];
      if (!util) continue;
      const utilityProps = topLevelDeclaredProps(rule.body);
      const utilityHooks = [util, hookBaseBeforeModifier(util)].filter(
        (hook): hook is string => hook !== null,
      );
      // Unlayered beats the utilities layer at any specificity, so a class
      // riding the same element everywhere buries this hook as surely as its own.
      const hooks = new Set([...utilityHooks, ...(coOccurring.get(util) ?? [])]);
      for (const hook of hooks) {
        for (const shadow of unlayered.get(hook) ?? []) {
          const shadowed = [...utilityProps].filter((p) => shadow.props.has(p)).sort();
          if (shadowed.length === 0) continue;
          hits.push(
            `${rel}: @utility ${util} cannot set ${shadowed.join(", ")} — unlayered .${hook} (${shadow.rel}) wins`,
          );
        }
      }
    }
  }
  return [...new Set(hits)].sort();
}

/** Box properties a `btn-` recipe may own. */
const RECIPE_BOX_PROPS = new Set([
  "padding",
  "padding-top",
  "padding-right",
  "padding-bottom",
  "padding-left",
  "width",
  "height",
  "min-width",
  "min-height",
]);

/** `class=` string literals on every `<DenButton>` tag, by source file. */
function denButtonClassHooks(denSrc: string): Map<string, Set<string>> {
  const files = walkFiles(
    denSrc,
    (p) => p.endsWith(".tsx") && !p.endsWith(".test.tsx"),
  );
  const byHook = new Map<string, Set<string>>();
  for (const file of files) {
    const rel = file.slice(denSrc.length + 1);
    const src = readFileSync(file, "utf8");
    for (const open of src.matchAll(/<DenButton\b/g)) {
      let i = (open.index ?? 0) + "<DenButton".length;
      let depth = 0;
      while (i < src.length) {
        const ch = src[i];
        if (ch === "{") depth++;
        else if (ch === "}") depth--;
        else if (ch === ">" && depth === 0) break;
        i++;
      }
      const tag = src.slice(open.index ?? 0, i);
      const classProp = /\bclass=(?:"([^"]*)"|\{([\s\S]*)\})/.exec(tag);
      if (!classProp) continue;
      const literals = classProp[1]
        ? [classProp[1]]
        : [...(classProp[2] ?? "").matchAll(/"([^"]*)"/g)].map((m) => m[1] ?? "");
      for (const hook of literals.join(" ").split(/\s+/)) {
        if (!hook) continue;
        byHook.set(hook, (byHook.get(hook) ?? new Set()).add(rel));
      }
    }
  }
  return byHook;
}

/** Equal-specificity utilities cannot reliably override recipe sizing. */
export function findButtonRecipeBoxContests(denSrc: string): string[] {
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const utilities = new Map<string, Set<string>>();
  for (const file of cssFiles) {
    for (const rule of splitStylesheetRules(readFileSync(file, "utf8"))) {
      const name = /^@utility\s+([a-zA-Z][a-zA-Z0-9_-]*)$/.exec(rule.selector)?.[1];
      if (!name) continue;
      const props = utilities.get(name) ?? new Set<string>();
      for (const prop of topLevelDeclaredProps(rule.body)) props.add(prop);
      utilities.set(name, props);
    }
  }
  const recipeBox = new Set<string>();
  for (const [name, props] of utilities) {
    if (!isBtnUtilityHook(name)) continue;
    for (const prop of props) if (RECIPE_BOX_PROPS.has(prop)) recipeBox.add(prop);
  }
  const hits: string[] = [];
  for (const [hook, files] of denButtonClassHooks(denSrc)) {
    const contested = [...(utilities.get(hook) ?? [])]
      .filter((prop) => recipeBox.has(prop))
      .sort();
    if (contested.length === 0) continue;
    hits.push(
      `@utility ${hook} cannot set ${contested.join(", ")} on a DenButton (${[...files].sort().join(", ")})`,
    );
  }
  return hits.sort();
}

/** One `@utility` block per hook — split recipes hide which values are live. */
export function findDuplicateUtilityDefinitions(denSrc: string): string[] {
  const { cssFiles } = readDenStylesheetInventory(denSrc);
  const seen = new Map<string, string[]>();
  for (const file of cssFiles) {
    const rel = file.slice(denSrc.length + 1);
    for (const match of readFileSync(file, "utf8").matchAll(
      /^@utility\s+([a-zA-Z][a-zA-Z0-9_-]*)/gm,
    )) {
      const name = match[1] ?? "";
      seen.set(name, [...(seen.get(name) ?? []), rel]);
    }
  }
  return [...seen]
    .filter(([, files]) => files.length > 1)
    .map(([name, files]) => `@utility ${name} defined ${files.length}× (${files.join(", ")})`)
    .sort();
}

/** Template `base--${…}` hooks must define modifiers in *-domain.css or *-art.css. */
export function findMissingDynamicModifierDomainRules(denSrc: string): string[] {
  const dynamicBases = extractDynamicModifierBases(denSrc);
  const { domainCssFiles } = readDenStylesheetInventory(denSrc);
  const domainSelectors = new Set<string>();
  for (const file of domainCssFiles) {
    for (const sel of extractCssClassSelectors(readFileSync(file, "utf8"))) {
      domainSelectors.add(sel);
    }
  }
  const hits: string[] = [];
  for (const base of [...dynamicBases].sort()) {
    const hasModifier = [...domainSelectors].some(
      (s) => s.startsWith(`${base}--`) && s !== base,
    );
    if (!hasModifier) {
      hits.push(`${base}--\${…} has no modifier rules in *-domain.css / *-art.css`);
    }
  }
  return hits;
}
