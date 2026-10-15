/** Finds every live-region declaration in Den's source, for the live-region bridge inventory. */
import * as ts from "typescript";
import { denSourceRoot, loadTypeScriptCorpus } from "./source-corpus.ts";

export type LiveDeclaration = {
  file: string;
  line: number;
  /** `role`, `aria-live`, or `output`. */
  kind: "role" | "aria-live" | "output";
  /** Literal value, or the string literals inside a computed one. */
  values: readonly string[];
  computed: boolean;
};

const LIVE_ROLES = new Set(["status", "log", "alert"]);

function literals(node: ts.Node): string[] {
  const found: string[] = [];
  const visit = (child: ts.Node) => {
    if (ts.isStringLiteralLike(child)) found.push(child.text);
    ts.forEachChild(child, visit);
  };
  visit(node);
  return found;
}

function attributeValue(initializer: ts.JsxAttributeValue | undefined): { values: string[]; computed: boolean } {
  if (!initializer) return { values: [""], computed: false };
  if (ts.isStringLiteral(initializer)) return { values: [initializer.text], computed: false };
  return { values: literals(initializer), computed: true };
}

function propertyName(name: ts.PropertyName): string | undefined {
  return ts.isIdentifier(name) || ts.isStringLiteralLike(name) ? name.text : undefined;
}

function isLive(kind: LiveDeclaration["kind"], values: readonly string[]): boolean {
  if (kind === "output") return true;
  if (kind === "aria-live") return true;
  return values.some((value) => value.split(/\s+/).some((token) => LIVE_ROLES.has(token)));
}

/** Every `role`/`aria-live` attribute, property, `setAttribute` call and `<output>` declaring a live region. */
export function scanLiveDeclarations(): LiveDeclaration[] {
  const corpus = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true });
  const out: LiveDeclaration[] = [];
  for (const file of corpus.files) {
    if (file.rel.startsWith("test/") || file.rel === "api/types.ts") continue;
    const add = (node: ts.Node, kind: LiveDeclaration["kind"], values: string[], computed: boolean) => {
      if (!isLive(kind, values)) return;
      const line = file.ast.getLineAndCharacterOfPosition(node.getStart(file.ast)).line + 1;
      out.push({ file: file.rel, line, kind, values, computed });
    };
    const visit = (node: ts.Node) => {
      if (ts.isJsxAttribute(node) && ts.isIdentifier(node.name)) {
        const name = node.name.text;
        if (name === "role" || name === "aria-live") {
          const { values, computed } = attributeValue(node.initializer);
          add(node, name, values, computed);
        }
      } else if ((ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node))
        && ts.isIdentifier(node.tagName) && node.tagName.text === "output") {
        add(node, "output", ["output"], false);
      } else if (ts.isPropertyAssignment(node)) {
        const name = propertyName(node.name);
        if (name === "role" || name === "aria-live") {
          add(node, name, literals(node.initializer), !ts.isStringLiteralLike(node.initializer));
        }
      } else if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)
        && node.expression.name.text === "setAttribute" && node.arguments.length === 2) {
        const [name, value] = node.arguments;
        if (name && value && ts.isStringLiteralLike(name) && (name.text === "role" || name.text === "aria-live")) {
          add(node, name.text, literals(value), !ts.isStringLiteralLike(value));
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(file.ast);
  }
  return out;
}

/** Declaration counts per file, the shape of the checked-in inventory. */
export function liveDeclarationCounts(declarations: readonly LiveDeclaration[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const declaration of [...declarations].sort((a, b) => a.file.localeCompare(b.file))) {
    counts[declaration.file] = (counts[declaration.file] ?? 0) + 1;
  }
  return counts;
}
