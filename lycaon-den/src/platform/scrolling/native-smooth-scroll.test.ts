import { relative } from "node:path";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import {
  denRoot,
  denSourceRoot,
  loadSourceCorpus,
  loadTypeScriptCorpus,
  requireNonEmpty,
} from "../../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../../test/vitest-timeouts.ts";

// A native smooth scroll animates on WebKit's scrolling thread. A scroll write during it leaves
// the page painted at one offset and hit tested at another, so clicks land a row away.
const JUMP_BEHAVIORS = new Set(["instant", "auto"]);
const NATIVE_SCROLL_METHODS = new Set(["scroll", "scrollTo", "scrollBy", "scrollIntoView", "scrollIntoViewIfNeeded"]);

/** Native scroll calls that can request an animated scroll. */
function nativeScrollRequestsWithBehavior(source: ts.SourceFile): string[] {
  const found: string[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      ts.isPropertyAccessExpression(node.expression) &&
      NATIVE_SCROLL_METHODS.has(node.expression.name.text) &&
      mayRequestBehavior(node.expression.name.text, node.arguments)
    ) {
      const { line } = source.getLineAndCharacterOfPosition(node.getStart());
      found.push(`${relative(denRoot, source.fileName)}:${line + 1}  ${node.getText().replace(/\s+/g, " ").slice(0, 120)}`);
    }
    if (
      (ts.isPropertyAssignment(node) ||
        (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken)) &&
      /\bscrollBehavior\b/.test((ts.isPropertyAssignment(node) ? node.name : node.left).getText()) &&
      /["'`]smooth["'`]/.test((ts.isPropertyAssignment(node) ? node.initializer : node.right).getText())
    ) {
      const { line } = source.getLineAndCharacterOfPosition(node.getStart());
      found.push(`${relative(denRoot, source.fileName)}:${line + 1}  ${node.getText().slice(0, 120)}`);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}

/**
 * Coordinate pairs take no options; a lone options bag names an animated behavior or hides its contents.
 * `scroll` is also a common non-DOM method name, so only its literal options count.
 */
function mayRequestBehavior(method: string, args: ts.NodeArray<ts.Expression>): boolean {
  const [options] = args;
  if (args.length !== 1 || !options) return false;
  if (options.kind === ts.SyntaxKind.TrueKeyword || options.kind === ts.SyntaxKind.FalseKeyword) return false;
  if (!ts.isObjectLiteralExpression(options)) return method !== "scroll";
  return options.properties.some((property) => {
    if (ts.isSpreadAssignment(property)) return true;
    if (ts.isShorthandPropertyAssignment(property)) return property.name.getText() === "behavior";
    if (!ts.isPropertyAssignment(property) || property.name.getText() !== "behavior") return false;
    // A jump is safe to request by name; anything else may animate.
    return !(ts.isStringLiteral(property.initializer) && JUMP_BEHAVIORS.has(property.initializer.text));
  });
}

function parse(text: string): ts.SourceFile {
  return ts.createSourceFile(`${denSourceRoot}/fixture.ts`, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
}

describe("native smooth scrolling", () => {
  it("recognizes every form that has requested one", () => {
    const shipped = [
      `this.viewport.scrollTo({ top: nextY, behavior: "smooth" });`,
      `list.scrollTo({ top: list.scrollTop + delta, behavior: prefersReducedMotion() ? "auto" : "smooth" });`,
      `scroller?.scrollTo({ top: offset + (adjustments ?? 0), behavior: options.behavior });`,
      `props.scrollport().scrollTo({ top: next, behavior });`,
      `tab.scrollIntoView({ inline: "nearest", block: "nearest", behavior: "smooth" });`,
      `element.scrollTo({ left: target, behavior: "smooth" });`,
      `element.scrollTo(options);`,
      `element.scrollBy({ ...options, top: 4 });`,
      `element.scroll({ top: 4, behavior: "smooth" });`,
      `viewport.style.scrollBehavior = "smooth";`,
    ];
    for (const text of shipped) expect(nativeScrollRequestsWithBehavior(parse(text)), text).toHaveLength(1);
    const jumps = [
      `viewport.scrollTo({ top: 40 });`,
      `viewport.scrollBy(0, delta);`,
      `viewport.scrollTo(0, top);`,
      `element.scrollIntoView({ block: "nearest", inline: "nearest" });`,
      `element.scrollIntoView(false);`,
      `el.scrollIntoView({ block: "center", behavior: "instant" });`,
      `element.scrollIntoView();`,
      `motion.commit(40, "reveal");`,
      `splitEditors?.scroll(editor);`,
      `virtualizer.scrollToOffset(offset, { behavior: "smooth" });`,
    ];
    for (const text of jumps) expect(nativeScrollRequestsWithBehavior(parse(text)), text).toEqual([]);
  });

  it("is never requested by Den source", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
    const files = requireNonEmpty("Den TypeScript", loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files);
    const found = files.flatMap((file) => nativeScrollRequestsWithBehavior(file.ast));
    expect(found, `Glide with ScrollportMotion.revealOffset or glideScrollLeft:\n${found.join("\n")}`).toEqual([]);
  });

  it("is never a stylesheet default", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
    const sheets = requireNonEmpty("Den stylesheets", loadSourceCorpus(denSourceRoot, { extensions: [".css"] }).files);
    const found = sheets.filter((sheet) => /scroll-behavior\s*:\s*smooth/i.test(sheet.text)).map((sheet) => sheet.rel);
    expect(found).toEqual([]);
  });
});
