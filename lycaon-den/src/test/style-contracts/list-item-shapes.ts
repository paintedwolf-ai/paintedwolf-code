import { dirname, relative, resolve } from "node:path";
import { parse, type Container, type Rule } from "postcss";
import * as ts from "typescript";
import { loadSourceCorpus, loadTypeScriptCorpus, type TypeScriptSourceFile } from "../source-corpus.ts";
import { splitTopLevel, subjectCompound } from "./stylesheet-parser.ts";

const WORKAROUND_ATTRIBUTE = "data-den-webkit-atspi-workaround";

/** Which class names a stylesheet can make `display: none`, or lay out as a grid. */
type DisplayIndex = { hidden: Map<string, string>; grid: Map<string, string> };

export type ListItemShapeFinding = { site: string; shape: "grid" | "hidden"; detail: string };

function fullSelectors(rule: Rule): string[] {
  const own = rule.selectors;
  const parent = rule.parent;
  if (!parent || parent.type !== "rule") return own;
  const outer = fullSelectors(parent as Rule);
  return outer.flatMap((prefix) =>
    own.map((selector) => (selector.includes("&") ? selector.replaceAll("&", prefix) : `${prefix} ${selector}`)),
  );
}

function subjectClasses(selector: string): string[] {
  const compound = subjectCompound(selector);
  // A pseudo-element is not part of the element's subtree.
  if (compound.includes("::")) return [];
  const subject = compound.replace(/:[a-z-]+(\([^)]*\))?/g, "");
  return [...subject.matchAll(/\.([a-zA-Z][a-zA-Z0-9_-]*)/g)].map((match) => match[1] ?? "");
}

function record(index: Map<string, string>, name: string, where: string): void {
  if (!index.has(name)) index.set(name, where);
}

/** Indexes every rule in the Den stylesheets by the classes its subject compound names. */
export function buildDisplayIndex(denSrc: string): DisplayIndex {
  const index: DisplayIndex = { hidden: new Map(), grid: new Map() };
  const sheets = loadSourceCorpus(denSrc, { extensions: [".css"] }).files;
  for (const sheet of sheets) {
    const root = parse(sheet.text, { from: sheet.path });
    root.walkDecls("display", (decl) => {
      const value = decl.value.toLowerCase();
      const shape = /\bnone\b/.test(value) ? "hidden" : /\b(?:inline-)?grid\b/.test(value) ? "grid" : null;
      if (!shape) return;
      const where = `${sheet.rel}:${decl.source?.start?.line ?? 0}`;
      const owner = decl.parent as Container | undefined;
      if (owner?.type === "atrule" && (owner as unknown as { name: string }).name === "utility") {
        record(index[shape], (owner as unknown as { params: string }).params.trim(), where);
        return;
      }
      if (owner?.type !== "rule") return;
      for (const selector of fullSelectors(owner as Rule)) {
        for (const part of splitTopLevel(selector, /,/)) {
          // Rules that never apply under the WebKitGTK AT-SPI workaround keep their own rendering.
          if (part.includes(`:not([${WORKAROUND_ATTRIBUTE}])`)) continue;
          for (const name of subjectClasses(part)) record(index[shape], name, where);
        }
      }
    });
  }
  return index;
}

/** Tailwind display utilities, after any variant prefixes. */
function utilityShape(token: string): "hidden" | "grid" | null {
  const base = token.slice(token.lastIndexOf(":") + 1);
  if (base === "hidden") return "hidden";
  if (base === "grid" || base === "inline-grid") return "grid";
  return null;
}

function tagName(node: ts.JsxElement | ts.JsxSelfClosingElement): string {
  const opening = ts.isJsxElement(node) ? node.openingElement : node;
  return opening.tagName.getText();
}

function attributes(node: ts.JsxElement | ts.JsxSelfClosingElement): ts.JsxAttributes {
  return (ts.isJsxElement(node) ? node.openingElement : node).attributes;
}

function attribute(node: ts.JsxElement | ts.JsxSelfClosingElement, name: string): ts.JsxAttribute | undefined {
  return attributes(node).properties.find(
    (property): property is ts.JsxAttribute => ts.isJsxAttribute(property) && property.name.getText() === name,
  );
}

function literalStrings(node: ts.Node | undefined): string[] {
  if (!node) return [];
  const out: string[] = [];
  const visit = (child: ts.Node): void => {
    if (ts.isStringLiteral(child) || ts.isNoSubstitutionTemplateLiteral(child)) out.push(child.text);
    else if (ts.isTemplateExpression(child)) {
      out.push(child.head.text, ...child.templateSpans.map((span) => span.literal.text));
    }
    // classList keys are class names too.
    if (ts.isPropertyAssignment(child) && (ts.isStringLiteral(child.name) || ts.isIdentifier(child.name))) {
      out.push(child.name.text);
    }
    ts.forEachChild(child, visit);
  };
  visit(node);
  return out;
}

function classTokens(node: ts.JsxElement | ts.JsxSelfClosingElement): string[] {
  return ["class", "className", "classList"]
    .flatMap((name) => literalStrings(attribute(node, name)?.initializer))
    .flatMap((text) => text.split(/\s+/))
    .filter((token) => /^[a-zA-Z][\w:-]*$/.test(token));
}

function inlineDisplay(node: ts.JsxElement | ts.JsxSelfClosingElement): string[] {
  const style = attribute(node, "style")?.initializer;
  if (!style) return [];
  const values: string[] = [];
  const visit = (child: ts.Node): void => {
    if (ts.isPropertyAssignment(child) && /^["']?display["']?$/.test(child.name.getText())) {
      values.push(...literalStrings(child.initializer));
    }
    ts.forEachChild(child, visit);
  };
  visit(style);
  return values;
}

function isListItem(node: ts.JsxElement | ts.JsxSelfClosingElement): boolean {
  if (tagName(node) === "li") return true;
  return literalStrings(attribute(node, "role")?.initializer).includes("listitem");
}

/** Portaled content mounts outside the list item. */
function isPortal(node: ts.JsxElement | ts.JsxSelfClosingElement): boolean {
  return tagName(node) === "Portal";
}

function jsxElements(
  root: ts.Node,
  prune: (node: ts.JsxElement | ts.JsxSelfClosingElement) => boolean = () => false,
): Array<ts.JsxElement | ts.JsxSelfClosingElement> {
  const out: Array<ts.JsxElement | ts.JsxSelfClosingElement> = [];
  const visit = (child: ts.Node): void => {
    if (ts.isJsxElement(child) || ts.isJsxSelfClosingElement(child)) {
      if (prune(child)) return;
      out.push(child);
    }
    ts.forEachChild(child, visit);
  };
  visit(root);
  return out;
}

function siteOf(file: TypeScriptSourceFile, node: ts.Node, denSrc: string): string {
  const { line } = file.ast.getLineAndCharacterOfPosition(node.getStart(file.ast));
  return `${relative(denSrc, file.path)}:${line + 1}`;
}

/**
 * Finds list items laid out as a grid or holding a CSS-hidden subtree. Components rendered inside a
 * list item are followed through their local definitions, so a hidden element they render counts.
 */
export function findListItemShapes(denSrc: string): ListItemShapeFinding[] {
  const display = buildDisplayIndex(denSrc);
  const corpus = loadTypeScriptCorpus(denSrc, { excludeTests: true });
  const byPath = new Map(corpus.files.map((file) => [file.path, file]));
  const findings: ListItemShapeFinding[] = [];
  const hiddenCache = new Map<string, string | null>();

  const hiddenReason = (node: ts.JsxElement | ts.JsxSelfClosingElement): string | null => {
    // Under the workaround a disclosure panel is mounted only while open.
    if (tagName(node) === "TranscriptDisclosurePanel") return null;
    const hidden = attribute(node, "hidden");
    if (hidden && hidden.initializer?.getText() !== "{false}") return `hidden attribute on <${tagName(node)}>`;
    if (inlineDisplay(node).includes("none")) return `inline display: none on <${tagName(node)}>`;
    for (const token of classTokens(node)) {
      if (utilityShape(token) === "hidden") return `utility ${token}`;
      const rule = display.hidden.get(token);
      if (rule) return `.${token} (display: none at ${rule})`;
    }
    return null;
  };

  const componentDefinition = (file: TypeScriptSourceFile, name: string): { file: TypeScriptSourceFile; body: ts.Node } | null => {
    const local = (source: TypeScriptSourceFile, wanted: string): ts.Node | null => {
      for (const statement of source.ast.statements) {
        if (ts.isFunctionDeclaration(statement) && statement.name?.text === wanted) return statement;
        if (ts.isVariableStatement(statement)) {
          for (const declaration of statement.declarationList.declarations) {
            if (declaration.name.getText() === wanted && declaration.initializer) return declaration.initializer;
          }
        }
      }
      return null;
    };
    const own = local(file, name);
    if (own) return { file, body: own };
    for (const statement of file.ast.statements) {
      if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
      const bindings = statement.importClause?.namedBindings;
      const imported =
        statement.importClause?.name?.text === name ||
        (bindings && ts.isNamedImports(bindings) && bindings.elements.some((element) => element.name.text === name));
      if (!imported || !statement.moduleSpecifier.text.startsWith(".")) continue;
      const base = resolve(dirname(file.path), statement.moduleSpecifier.text);
      const target = [base, `${base}.tsx`, `${base}.ts`].map((candidate) => byPath.get(candidate)).find(Boolean);
      if (!target) continue;
      const original = bindings && ts.isNamedImports(bindings)
        ? bindings.elements.find((element) => element.name.text === name)?.propertyName?.text ?? name
        : name;
      const body = local(target, original);
      if (body) return { file: target, body };
    }
    return null;
  };

  const subtreeHidden = (file: TypeScriptSourceFile, root: ts.Node, depth: number, seen: Set<string>): string | null => {
    for (const element of jsxElements(root, isPortal)) {
      if (element === root) continue;
      const reason = hiddenReason(element);
      if (reason) return `${reason} at ${siteOf(file, element, denSrc)}`;
      const tag = tagName(element);
      if (depth >= 4 || !/^[A-Z]/.test(tag) || tag.includes(".") || tag === "TranscriptDisclosurePanel") continue;
      const definition = componentDefinition(file, tag);
      if (!definition) continue;
      const key = `${definition.file.path}#${tag}`;
      if (seen.has(key)) continue;
      seen.add(key);
      if (!hiddenCache.has(key)) {
        hiddenCache.set(key, subtreeHidden(definition.file, definition.body, depth + 1, seen));
      }
      const nested = hiddenCache.get(key);
      if (nested) return `${tag}: ${nested}`;
    }
    return null;
  };

  for (const file of corpus.files) {
    if (!file.path.endsWith(".tsx")) continue;
    for (const element of jsxElements(file.ast)) {
      if (!isListItem(element)) continue;
      const site = siteOf(file, element, denSrc);
      const grid =
        inlineDisplay(element).find((value) => /\b(?:inline-)?grid\b/.test(value)) ??
        classTokens(element).find((token) => utilityShape(token) === "grid" || display.grid.has(token));
      if (grid) {
        const rule = display.grid.get(grid);
        findings.push({ site, shape: "grid", detail: rule ? `.${grid} (display: grid at ${rule})` : grid });
      }
      const hidden = subtreeHidden(file, element, 0, new Set());
      if (hidden) findings.push({ site, shape: "hidden", detail: hidden });
    }
  }
  return findings;
}

/** Stylesheet rules that lay out list items themselves as a grid. */
export function findListItemGridRules(denSrc: string): string[] {
  const out: string[] = [];
  for (const sheet of loadSourceCorpus(denSrc, { extensions: [".css"] }).files) {
    parse(sheet.text).walkDecls("display", (decl) => {
      if (!/\b(?:inline-)?grid\b/.test(decl.value) || decl.parent?.type !== "rule") return;
      for (const selector of fullSelectors(decl.parent as Rule)) {
        const subject = subjectCompound(selector);
        if (/(^|[^\w-])li\b|\[role=["']?listitem/.test(subject)) {
          out.push(`${sheet.rel}:${decl.source?.start?.line ?? 0} ${selector}`);
        }
      }
    });
  }
  return out;
}
