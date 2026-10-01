


export function extractTailwindUtilities(css: string): Set<string> {
  const utilities = new Set<string>();
  for (const match of css.matchAll(/@utility\s+([a-zA-Z0-9_-]+)/g)) {
    utilities.add(match[1] ?? "");
  }
  return utilities;
}

/** Class selectors from plain CSS (includes compound selector roots). */
export function extractCssClassSelectors(css: string): Set<string> {
  const selectors = new Set<string>();
  for (const match of css.matchAll(/\.([a-zA-Z][a-zA-Z0-9_-]*)/g)) {
    selectors.add(match[1] ?? "");
  }
  return selectors;
}

export function splitTopLevel(text: string, separator: RegExp): string[] {
  const parts: string[] = [];
  let depth = 0;
  let current = "";
  for (const char of text) {
    if (char === "(" || char === "[") depth++;
    else if (char === ")" || char === "]") depth--;
    if (depth === 0 && separator.test(char)) {
      if (current.trim()) parts.push(current.trim());
      current = "";
      continue;
    }
    current += char;
  }
  if (current.trim()) parts.push(current.trim());
  return parts;
}

/** The subject compound of a complex selector, e.g. `::after` in `.x > *::after`. */
export function subjectCompound(selector: string): string {
  const compounds = splitTopLevel(selector.replace(/\s*([>+~])\s*/g, " $1 "), /[\s>+~]/);
  return compounds[compounds.length - 1] ?? "";
}

/** Count unique BEM (`.foo__bar` / `.foo--bar`) selectors. */
export function countBemSelectors(css: string): number {
  const seen = new Set<string>();
  for (const match of css.matchAll(/\.([a-zA-Z][a-zA-Z0-9_-]*(?:__|--)[a-zA-Z0-9_-]+)/g)) {
    seen.add(match[1] ?? "");
  }
  return seen.size;
}

/** Splits a stylesheet into top-level rules; an at-rule keeps its block as body. */
export function splitStylesheetRules(css: string): Array<{ selector: string; body: string }> {
  const rules: Array<{ selector: string; body: string }> = [];
  let i = 0;
  const n = css.length;
  while (i < n) {
    while (i < n && /\s/.test(css[i] ?? "")) i++;
    if (i >= n) break;
    if (css.startsWith("/*", i)) {
      const end = css.indexOf("*/", i);
      i = end >= 0 ? end + 2 : n;
      continue;
    }
    const start = i;
    const brace = css.indexOf("{", i);
    if (brace < 0) break;
    const selector = css.slice(start, brace).trim();
    i = brace + 1;
    let depth = 1;
    while (i < n && depth) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}") depth--;
      i++;
    }
    rules.push({ selector, body: css.slice(brace + 1, i - 1) });
  }
  return rules;
}

export function parseDeclarations(body: string): Array<{ prop: string; value: string }> {
  const decls: Array<{ prop: string; value: string }> = [];
  for (const chunk of body.split(";")) {
    const trimmed = chunk.trim();
    if (!trimmed || trimmed.startsWith("/*")) continue;
    const colon = trimmed.indexOf(":");
    if (colon <= 0) continue;
    decls.push({
      prop: trimmed.slice(0, colon).trim().toLowerCase(),
      value: trimmed.slice(colon + 1).trim(),
    });
  }
  return decls;
}

/** Declarations a rule sets directly, ignoring nested blocks. */
export function topLevelDeclaredProps(body: string): Set<string> {
  const props = new Set<string>();
  let depth = 0;
  let chunk = "";
  const take = () => {
    const colon = chunk.indexOf(":");
    const prop = colon > 0 ? chunk.slice(0, colon).trim().toLowerCase() : "";
    if (/^-?[a-z][a-z0-9-]*$/.test(prop)) props.add(prop);
    chunk = "";
  };
  for (const ch of body) {
    if (ch === "{") depth++;
    else if (ch === "}") depth--;
    else if (depth === 0) {
      if (ch === ";") take();
      else chunk += ch;
      continue;
    }
    chunk = "";
  }
  take();
  return props;
}

export function keyframeDeclaredProps(body: string): Set<string> {
  const props = new Set<string>();
  for (const match of body.matchAll(/(?:^|[{;\s])([a-zA-Z-]+)\s*:/g)) {
    props.add(match[1] ?? "");
  }
  return props;
}
