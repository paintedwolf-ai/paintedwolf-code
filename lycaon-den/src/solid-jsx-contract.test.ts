// @vitest-environment jsdom
import { relative } from "node:path";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "./test/vitest-timeouts.ts";
import { denRoot, denSourceRoot, loadTypeScriptCorpus } from "./test/source-corpus.ts";

const root = denRoot;

type Finding = { file: string; line: number; col: number; snippet: string };

function sourceFiles(): readonly ts.SourceFile[] {
  return loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files.map(
    (file) => file.ast,
  );
}

function record(
  findings: Finding[],
  sourceFile: ts.SourceFile,
  node: ts.Node,
  snippet?: string,
): void {
  const { line, character } = sourceFile.getLineAndCharacterOfPosition(
    node.getStart(),
  );
  findings.push({
    file: relative(root, sourceFile.fileName),
    line: line + 1,
    col: character + 1,
    snippet: (snippet ?? node.getText()).slice(0, 120),
  });
}

function format(findings: Finding[]): string {
  if (findings.length === 0) return "";
  return findings.map((f) => `  ${f.file}:${f.line}:${f.col}  ${f.snippet}`).join("\n");
}

function buildImportMap(sourceFile: ts.SourceFile): Map<string, string> {
  const map = new Map<string, string>();
  for (const stmt of sourceFile.statements) {
    if (!ts.isImportDeclaration(stmt)) continue;
    if (!ts.isStringLiteral(stmt.moduleSpecifier)) continue;
    const module = stmt.moduleSpecifier.text;
    const clause = stmt.importClause;
    if (!clause) continue;
    if (clause.name) map.set(clause.name.text, module);
    const bindings = clause.namedBindings;
    if (!bindings) continue;
    if (ts.isNamedImports(bindings)) {
      for (const elem of bindings.elements) {
        map.set(elem.name.text, module);
      }
    } else if (ts.isNamespaceImport(bindings)) {
      map.set(bindings.name.text, module);
    }
  }
  return map;
}

function isSolidPrimitive(
  tagName: string,
  importMap: Map<string, string>,
): boolean {
  const module = importMap.get(tagName);
  return module === "solid-js" || module === "solid-js/web";
}

function findReactiveLeak(
  showElement: ts.JsxElement,
  sourceFile: ts.SourceFile,
  importMap: Map<string, string>,
  out: Finding[],
): void {
  for (const child of showElement.children) {
    if (!ts.isJsxExpression(child) || !child.expression) continue;
    const expr = child.expression;
    if (!ts.isArrowFunction(expr) && !ts.isFunctionExpression(expr)) continue;
    const param = expr.parameters[0];
    if (!param || !ts.isIdentifier(param.name)) continue;
    const accessorName = param.name.text;

    const checkProp = (attr: ts.JsxAttributeLike): void => {
      if (!ts.isJsxAttribute(attr) || !attr.initializer) return;
      if (!ts.isJsxExpression(attr.initializer)) return;
      const value = attr.initializer.expression;
      if (!value) return;
      let leaks = false;
      const findCall = (n: ts.Node): void => {
        if (
          ts.isCallExpression(n) &&
          ts.isIdentifier(n.expression) &&
          n.expression.text === accessorName
        ) {
          leaks = true;
        }
        if (!leaks) ts.forEachChild(n, findCall);
      };
      findCall(value);
      if (leaks) record(out, sourceFile, attr.initializer);
    };

    const visitJsx = (n: ts.Node): void => {
      if (ts.isJsxElement(n) || ts.isJsxSelfClosingElement(n)) {
        const opening = ts.isJsxElement(n) ? n.openingElement : n;
        if (
          ts.isIdentifier(opening.tagName) &&
          /^[A-Z]/.test(opening.tagName.text) &&
          !isSolidPrimitive(opening.tagName.text, importMap)
        ) {
          for (const attr of opening.attributes.properties) {
            checkProp(attr);
          }
        }
      }
      ts.forEachChild(n, visitJsx);
    };
    visitJsx(expr.body);
  }
}

/** List callbacks run under an item-scoped reactive owner. KeyedIndex renders its children inside `For`. */
function ownsAnItemScope(
  expression: ts.JsxExpression,
  importMap: Map<string, string>,
): boolean {
  const element = expression.parent;
  if (!ts.isJsxElement(element)) return false;
  const tagName = element.openingElement.tagName;
  if (!ts.isIdentifier(tagName)) return false;
  if (tagName.text === "KeyedIndex") return importMap.get(tagName.text)?.endsWith("/keyed-index.tsx") === true;
  if (tagName.text !== "For" && tagName.text !== "Index") return false;
  return isSolidPrimitive(tagName.text, importMap);
}

function scan(visitor: (sourceFile: ts.SourceFile, findings: Finding[]) => void): Finding[] {
  const findings: Finding[] = [];
  for (const sourceFile of sourceFiles()) {
    visitor(sourceFile, findings);
  }
  return findings;
}

function findKeyedSettingsDrafts(sourceFile: ts.SourceFile, findings: Finding[]): void {
  const imports = buildImportMap(sourceFile);
  const drafts = new Set<string>();
  const accessors = new Set<string>();
  const isDraftValue = (node: ts.Expression): boolean =>
    ts.isPropertyAccessExpression(node) && ts.isIdentifier(node.expression)
      && drafts.has(node.expression.text) && node.name.text === "value";
  const collect = (node: ts.Node): void => {
    if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.initializer) {
      const value = node.initializer;
      if (ts.isCallExpression(value) && ts.isIdentifier(value.expression)
        && imports.get(value.expression.text)?.endsWith("/settings-draft.ts")) {
        drafts.add(node.name.text);
      } else if (isDraftValue(value)) {
        accessors.add(node.name.text);
      }
    }
    ts.forEachChild(node, collect);
  };
  collect(sourceFile);
  if (drafts.size === 0) return;
  const visit = (node: ts.Node): void => {
    if (ts.isJsxOpeningElement(node) && ts.isIdentifier(node.tagName)
      && isSolidPrimitive(node.tagName.text, imports)) {
      const attributes = node.attributes.properties.filter(ts.isJsxAttribute);
      const keyed = attributes.some((attr) => attr.name.getText() === "keyed"
        && !(attr.initializer && ts.isJsxExpression(attr.initializer)
          && attr.initializer.expression?.kind === ts.SyntaxKind.FalseKeyword));
      const when = attributes.find((attr) => attr.name.getText() === "when")?.initializer;
      const expression = when && ts.isJsxExpression(when) ? when.expression : undefined;
      if (keyed && expression && ts.isCallExpression(expression)) {
        const callee = expression.expression;
        if ((ts.isIdentifier(callee) && accessors.has(callee.text)) || isDraftValue(callee)) {
          record(findings, sourceFile, node);
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sourceFile);
}

describe("solid jsx contract", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("settings drafts do not become keyed reactive subtree identities", () => {
    const findings = scan(findKeyedSettingsDrafts);
    expect(findings, `A settings edit replaces its draft object. Keep the form mounted with ShowLatest or a stable subject identity:\n${format(findings)}`).toEqual([]);
  });

  it("distinguishes replaceable drafts from stable subject keys", () => {
    const source = `
      import { Show } from "solid-js";
      import { createSettingsDraft } from "./settings/settings-draft.ts";
      const state = createSettingsDraft(null);
      const draft = state.value;
      <Show when={draft()} keyed>{value => <input />}</Show>;
      <Show when={state.value()} keyed>{value => <input />}</Show>;
      <Show when={draft()?.id} keyed>{value => <input />}</Show>;
      <Show when={draft()}>{value => <input />}</Show>;
      <Show when={draft()} keyed={ false }>{value => <input />}</Show>;
    `;
    const ast = ts.createSourceFile("fixture.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
    const findings: Finding[] = [];
    findKeyedSettingsDrafts(ast, findings);
    expect(findings).toHaveLength(2);
  });

  it("no non-null bang on call expressions (NonNullExpression on CallExpression)", () => {
    const findings = scan((sourceFile, out) => {
      const visit = (node: ts.Node): void => {
        if (
          ts.isNonNullExpression(node) &&
          ts.isCallExpression(node.expression)
        ) {
          record(out, sourceFile, node);
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    });
    expect(
      findings.length,
      `\nReactive call!.field anti-pattern. Use keyed <Show when={x()}>{(v) => …}</Show> / snapshot the call once:\n${format(findings)}\n`,
    ).toBe(0);
  });

  it("no `as Extract<…>` cast on call expressions (unsafe reactive narrowing)", () => {
    const findings = scan((sourceFile, out) => {
      const visit = (node: ts.Node): void => {
        if (
          ts.isAsExpression(node) &&
          ts.isCallExpression(node.expression) &&
          ts.isTypeReferenceNode(node.type) &&
          ts.isIdentifier(node.type.typeName) &&
          node.type.typeName.text === "Extract"
        ) {
          record(out, sourceFile, node);
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    });
    expect(
      findings.length,
      `\nUnsafe Extract<…> cast on a reactive call. Use a kind-narrowing helper and the keyed <Match when={helper(item())}>{(v) => …}</Match> form:\n${format(findings)}\n`,
    ).toBe(0);
  });

  it("non-keyed <Show> callback must not flow accessor() calls into function-component props", () => {
    const findings = scan((sourceFile, out) => {
      const importMap = buildImportMap(sourceFile);
      const visit = (node: ts.Node): void => {
        if (
          ts.isJsxElement(node) &&
          ts.isIdentifier(node.openingElement.tagName) &&
          node.openingElement.tagName.text === "Show"
        ) {
          const hasKeyed = node.openingElement.attributes.properties.some(
            (attr) =>
              ts.isJsxAttribute(attr) &&
              ts.isIdentifier(attr.name) &&
              attr.name.text === "keyed",
          );
          if (!hasKeyed) {
            findReactiveLeak(node, sourceFile, importMap, out);
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    });
    expect(
      findings.length,
      `\nNon-keyed <Show> leaks its accessor into a function-component prop. Add \`keyed\` so the callback receives the value directly, or use <ShowLatest> when the same subject arrives as a new object and a remount would tear down what the user is editing:\n${format(findings)}\n`,
    ).toBe(0);
  });

  it("JSX callbacks do not create owner-bound computations", () => {
    const computationFactories = new Set([
      "createComputed",
      "createEffect",
      "createMemo",
      "createRenderEffect",
    ]);
    const findings = scan((sourceFile, out) => {
      const importMap = buildImportMap(sourceFile);
      const visit = (node: ts.Node): void => {
        if (
          ts.isCallExpression(node) &&
          ts.isIdentifier(node.expression) &&
          computationFactories.has(node.expression.text)
        ) {
          let ancestor: ts.Node | undefined = node.parent;
          while (ancestor) {
            if (
              (ts.isArrowFunction(ancestor) || ts.isFunctionExpression(ancestor)) &&
              ts.isJsxExpression(ancestor.parent)
            ) {
              if (!ownsAnItemScope(ancestor.parent, importMap)) {
                record(out, sourceFile, node);
              }
              break;
            }
            ancestor = ancestor.parent;
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    });
    expect(
      findings.length,
      `\nJSX callbacks run without a reactive owner. Move createMemo/createEffect to the component body or use reactive JSX props:\n${format(findings)}\n`,
    ).toBe(0);
  });

  it("no `!` non-null assertions inside JSX attribute or child expressions", () => {
    const findings = scan((sourceFile, out) => {
      let jsxDepth = 0;
      const visit = (node: ts.Node): void => {
        if (
          ts.isJsxAttribute(node) ||
          ts.isJsxExpression(node) ||
          ts.isJsxSpreadAttribute(node)
        ) {
          jsxDepth += 1;
          if (jsxDepth === 1) {
            const findBangs = (n: ts.Node): void => {
              if (ts.isNonNullExpression(n)) {
                record(out, sourceFile, n);
              }
              // Nested JSX attributes are visited separately.
              if (!ts.isJsxAttribute(n) && !ts.isJsxExpression(n)) {
                ts.forEachChild(n, findBangs);
              }
            };
            ts.forEachChild(node, findBangs);
          }
          ts.forEachChild(node, visit);
          jsxDepth -= 1;
          return;
        }
        ts.forEachChild(node, visit);
      };
      visit(sourceFile);
    });
    expect(
      findings.length,
      `\nNon-null bang (\`!\`) inside JSX. Pull the value into a local with an explicit check, or use the keyed <Show>/<Match> callback form:\n${format(findings)}\n`,
    ).toBe(0);
  });
});
