

/** Strip data-testid attributes so test ids are not mistaken for class hooks. */
export function stripDataTestIds(source: string): string {
  return source.replace(/\sdata-testid=(?:\{[^}]+\}|"[^"]*"|'[^']*')/g, "");
}

/** Extract static class tokens from JSX class, classList, and cn() usage. */
export function extractClassHooksFromSource(source: string): string[] {
  const scrubbed = stripDataTestIds(source);
  const hooks = new Set<string>();

  const addTokens = (raw: string) => {
    for (const token of raw.split(/\s+/)) {
      const t = token.trim();
      if (t) hooks.add(t);
    }
  };

  /** Drop ${...} interpolations so only static class tokens survive tokenization. */
  const addTemplateTokens = (raw: string) => addTokens(raw.replace(/\$\{[^}]*\}/g, " "));

  for (const match of scrubbed.matchAll(/\bclass\s*=\s*"([^"]+)"/g)) {
    addTokens(match[1] ?? "");
  }
  for (const match of scrubbed.matchAll(/\bclass\s*=\s*'([^']+)'/g)) {
    addTokens(match[1] ?? "");
  }
  for (const match of scrubbed.matchAll(/\bclass\s*=\s*\{\s*`([^`]*)`\s*\}/g)) {
    addTemplateTokens(match[1] ?? "");
  }

  for (const block of scrubbed.matchAll(/classList\s*=\s*\{\{([\s\S]*?)\}\}/g)) {
    const body = block[1] ?? "";
    for (const key of body.matchAll(/["']([^"']+)["']\s*:/g)) {
      addTokens(key[1] ?? "");
    }
  }

  for (const call of scrubbed.matchAll(/\bcn\s*\(([\s\S]*?)\)/g)) {
    const args = call[1] ?? "";
    for (const lit of args.matchAll(/"([^"]+)"/g)) {
      addTokens(lit[1] ?? "");
    }
    for (const lit of args.matchAll(/'([^']+)'/g)) {
      addTokens(lit[1] ?? "");
    }
    for (const lit of args.matchAll(/`([^`]*)`/g)) {
      addTemplateTokens(lit[1] ?? "");
    }
  }

  return [...hooks];
}

/** An attribute value's source text, and whether it was written as a string. */
type JsxAttributeValue = { text: string; quoted: boolean };

/** Reads a JSX attribute's raw source value out of an opening tag's attributes. */
function jsxAttributeValue(attrs: string, name: string): JsxAttributeValue | null {
  let depth = 0;
  let quote = "";
  for (let i = 0; i < attrs.length; i++) {
    const c = attrs[i] as string;
    if (quote) {
      if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      continue;
    }
    if (c === "{") {
      depth++;
      continue;
    }
    if (c === "}") {
      depth--;
      continue;
    }
    // Only attributes of this tag, never one inside a nested element.
    if (depth !== 0 || !attrs.startsWith(name, i)) continue;
    if (i > 0 && !/\s/.test(attrs[i - 1] as string)) continue;
    let j = i + name.length;
    while (j < attrs.length && /\s/.test(attrs[j] as string)) j++;
    if (attrs[j] !== "=") continue;
    j++;
    while (j < attrs.length && /\s/.test(attrs[j] as string)) j++;
    return jsxAttributeText(attrs, j);
  }
  return null;
}

/** The quoted text or balanced brace expression an attribute value opens with. */
function jsxAttributeText(src: string, start: number): JsxAttributeValue | null {
  const opener = src[start];
  if (opener === '"' || opener === "'") {
    const end = src.indexOf(opener, start + 1);
    return end < 0 ? null : { text: src.slice(start + 1, end), quoted: true };
  }
  if (opener !== "{") return null;
  let depth = 0;
  let quote = "";
  for (let i = start; i < src.length; i++) {
    const c = src[i] as string;
    if (quote) {
      if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      continue;
    }
    if (c === "{") depth++;
    else if (c === "}" && --depth === 0) {
      return { text: src.slice(start + 1, i), quoted: false };
    }
  }
  return null;
}

function addClassTokens(raw: string, into: Set<string>): void {
  for (const token of raw.replace(/\$\{[^}]*\}/g, " ").split(/\s+/)) {
    if (/^[a-zA-Z][a-zA-Z0-9_-]*$/.test(token)) into.add(token);
  }
}

/** Top-level comma-separated argument texts of a call's argument list. */
export function splitCallArguments(args: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let quote = "";
  let start = 0;
  for (let i = 0; i < args.length; i++) {
    const c = args[i] as string;
    if (quote) {
      if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'" || c === "`") quote = c;
    else if (c === "(" || c === "[" || c === "{") depth++;
    else if (c === ")" || c === "]" || c === "}") depth--;
    else if (c === "," && depth === 0) {
      parts.push(args.slice(start, i));
      start = i + 1;
    }
  }
  parts.push(args.slice(start));
  return parts;
}

/** Class tokens an element carries always, versus only under a condition. */
type ClassTokens = { always: Set<string>; maybe: Set<string> };

type ElementClassSite = ClassTokens & { element: string };

function emptyClassTokens(): ClassTokens {
  return { always: new Set(), maybe: new Set() };
}

/** Splits class-name expressions into unconditional and conditional tokens. */
export function classTokensFromExpressions(expressions: string[]): ClassTokens {
  const tokens = emptyClassTokens();
  for (const expression of expressions) {
    // A bare literal always lands; anything guarded may not.
    const literal = /^\s*(?:"([^"]*)"|'([^']*)'|`([^`$]*)`)\s*$/.exec(expression);
    if (literal) {
      addClassTokens(literal[1] ?? literal[2] ?? literal[3] ?? "", tokens.always);
      continue;
    }
    for (const inner of expression.matchAll(/"([^"]*)"|'([^']*)'|`([^`]*)`/g)) {
      addClassTokens(inner[1] ?? inner[2] ?? inner[3] ?? "", tokens.maybe);
    }
  }
  return tokens;
}

/** Splits a `class` attribute value into unconditional and conditional tokens. */
function classTokensFromValue(value: string, quoted: boolean): ClassTokens {
  const written = quoted ? value : /^\s*`([^`]*)`\s*$/.exec(value)?.[1];
  if (written !== undefined) {
    const tokens = emptyClassTokens();
    addClassTokens(written, tokens.always);
    return tokens;
  }
  const call = /^\s*cn\s*\(([\s\S]*)\)\s*$/.exec(value);
  return classTokensFromExpressions(
    call ? splitCallArguments(call[1] ?? "") : [value],
  );
}

/** Class tokens a `classList` object literal can put on its element. */
function classTokensFromClassList(value: string): Set<string> {
  const tokens = new Set<string>();
  for (const key of value.matchAll(/["'`]([^"'`]+)["'`]\s*:/g)) {
    addClassTokens(key[1] ?? "", tokens);
  }
  return tokens;
}

/** Class tokens per opening tag, split by whether the element always carries them. */
export function elementClassSites(source: string): ElementClassSite[] {
  const src = stripDataTestIds(source);
  const sites: ElementClassSite[] = [];
  for (const open of src.matchAll(/<([A-Za-z][A-Za-z0-9_.]*)(?=[\s/>])/g)) {
    const attrs = openingTagAttributes(src, (open.index ?? 0) + open[0].length);
    const klass = jsxAttributeValue(attrs, "class");
    const site: ElementClassSite = {
      element: open[1] ?? "",
      ...(klass ? classTokensFromValue(klass.text, klass.quoted) : emptyClassTokens()),
    };
    const classList = jsxAttributeValue(attrs, "classList");
    if (classList) {
      // A classList entry is a condition, never a guarantee.
      for (const token of classTokensFromClassList(classList.text)) site.maybe.add(token);
    }
    if (site.always.size + site.maybe.size > 0) sites.push(site);
  }
  return sites;
}



/** Reads the attribute text of the opening tag starting at `open`. */
export function openingTagAttributes(src: string, open: number): string {
  let depth = 0;
  let quote = "";
  for (let i = open; i < src.length; i++) {
    const c = src[i] as string;
    if (quote) {
      if (c === quote) quote = "";
      continue;
    }
    if (c === '"' || c === "'" || c === "`") {
      quote = c;
      continue;
    }
    if (c === "{") depth++;
    else if (c === "}") depth--;
    else if (c === ">" && depth === 0) return src.slice(open, i);
  }
  return src.slice(open);
}
