function extractBraceBlock(css: string, openBrace: number): string {
  let depth = 0;
  for (let i = openBrace; i < css.length; i++) {
    if (css[i] === "{") depth++;
    else if (css[i] === "}") {
      depth--;
      if (depth === 0) return css.slice(openBrace, i + 1);
    }
  }
  throw new Error("unclosed CSS rule block");
}

/** Extract a CSS rule block for an exact selector (e.g. `.den-foo`) or matching `@utility den-foo`. */
export function extractCssRuleBlock(css: string, selector: string): string {
  const className = selector.startsWith(".") ? selector.slice(1) : selector;
  const escapedClass = className.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const utilityRe = new RegExp(`@utility\\s+${escapedClass}\\s*\\{`, "m");
  const utilityMatch = utilityRe.exec(css);
  if (utilityMatch) {
    const openBrace = css.indexOf("{", utilityMatch.index);
    try {
      return extractBraceBlock(css, openBrace);
    } catch {
      throw new Error(`unclosed CSS rule: ${selector}`);
    }
  }

  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const re = new RegExp(`(?:^|[\\n}])\\s*${escaped}\\s*\\{`, "m");
  const match = re.exec(css);
  if (!match) {
    throw new Error(`missing CSS rule: ${selector}`);
  }
  const openBrace = css.indexOf("{", match.index);
  try {
    return extractBraceBlock(css, openBrace);
  } catch {
    throw new Error(`unclosed CSS rule: ${selector}`);
  }
}
