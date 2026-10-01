import { dirname, join, relative, resolve } from "node:path";
import * as ts from "typescript";
import type { SourceCorpus, TypeScriptSourceFile } from "./source-corpus.ts";

/**
 * Classifies how long a registration made at a call site lives, relative to the
 * Solid component that makes it:
 * - "mount": for the whole mount (component body, `ref`, `onMount`, or an
 *   effect that registers unconditionally);
 * - "transient": while a reactive condition holds, or from inside an event
 *   handler or gesture.
 * Calls that never reach a component (exported helpers, plugins, module
 * scope) belong to shared infrastructure and have no holder.
 */
export type RegistrationLifetime = "mount" | "transient";

export type RegistrationHolder = {
  readonly component: string;
  readonly file: string;
  readonly lifetime: RegistrationLifetime;
  /** Mounted exactly once per window, directly under the root render. */
  readonly singleton: boolean;
  /** The lifecycle and wrapper hops between the component and the call. */
  readonly via: readonly string[];
};

export type CallSite = {
  readonly file: TypeScriptSourceFile;
  readonly call: ts.Node;
};

type FunctionNode =
  | ts.ArrowFunction
  | ts.FunctionExpression
  | ts.FunctionDeclaration
  | ts.MethodDeclaration
  | ts.ConstructorDeclaration
  | ts.GetAccessorDeclaration
  | ts.SetAccessorDeclaration;

const REACTIVE_SCOPES = new Set(["createEffect", "createRenderEffect", "createComputed", "on"]);
const MOUNT_SCOPES = new Set(["onMount", "untrack", "batch", "runWithOwner"]);
/** Callbacks that run during the caller's own turn or shortly after it. */
const SAME_TURN_CALLBACKS = new Set([
  "forEach", "map", "flatMap", "filter", "some", "every", "reduce", "find",
  "then", "catch", "finally",
  "setTimeout", "requestAnimationFrame", "queueMicrotask",
]);

export function line(file: TypeScriptSourceFile, node: ts.Node): number {
  return file.ast.getLineAndCharacterOfPosition(node.getStart(file.ast)).line + 1;
}

function isFunctionNode(node: ts.Node): node is FunctionNode {
  return ts.isArrowFunction(node) || ts.isFunctionExpression(node) ||
    ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) ||
    ts.isConstructorDeclaration(node) || ts.isGetAccessorDeclaration(node) ||
    ts.isSetAccessorDeclaration(node);
}

function enclosingFunction(node: ts.Node): FunctionNode | undefined {
  for (let current = node.parent; current; current = current.parent) {
    if (isFunctionNode(current)) return current;
  }
  return undefined;
}

function boundName(fn: FunctionNode): ts.Identifier | undefined {
  if (ts.isFunctionDeclaration(fn)) return fn.name;
  if ((ts.isArrowFunction(fn) || ts.isFunctionExpression(fn)) &&
    ts.isVariableDeclaration(fn.parent) && ts.isIdentifier(fn.parent.name)) {
    return fn.parent.name;
  }
  return undefined;
}

function containsJsx(node: ts.Node): boolean {
  let found = false;
  const visit = (child: ts.Node) => {
    if (found) return;
    if (ts.isJsxElement(child) || ts.isJsxSelfClosingElement(child) || ts.isJsxFragment(child)) {
      found = true;
      return;
    }
    ts.forEachChild(child, visit);
  };
  visit(node);
  return found;
}

/** Imported local name → exported name, for one module specifier predicate. */
function namedImports(
  ast: ts.SourceFile,
  from: (specifier: string) => boolean,
): Map<string, string> {
  const names = new Map<string, string>();
  for (const statement of ast.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
    if (!from(statement.moduleSpecifier.text)) continue;
    const bindings = statement.importClause?.namedBindings;
    if (!bindings || !ts.isNamedImports(bindings)) continue;
    for (const element of bindings.elements) {
      names.set(element.name.text, (element.propertyName ?? element.name).text);
    }
  }
  return names;
}

function isModuleExported(fn: FunctionNode): boolean {
  const statement = ts.isFunctionDeclaration(fn) ? fn : fn.parent?.parent?.parent;
  if (!statement || !ts.isSourceFile(statement.parent)) return false;
  const modifiers = ts.canHaveModifiers(statement) ? ts.getModifiers(statement) : undefined;
  return modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword) ?? false;
}

/** The statement list a local function's name is visible in. */
function declarationScope(fn: FunctionNode): ts.Node {
  for (let current: ts.Node | undefined = fn.parent; current; current = current.parent) {
    if (ts.isBlock(current) || ts.isSourceFile(current)) return current;
  }
  return fn.getSourceFile();
}

function calleeName(call: ts.CallExpression): string | undefined {
  const callee = call.expression;
  if (ts.isIdentifier(callee)) return callee.text;
  if (ts.isPropertyAccessExpression(callee)) return callee.name.text;
  return undefined;
}

/** True when `target` runs only while a condition inside `fn` holds. */
function conditionallyReached(fn: FunctionNode, target: ts.Node): boolean {
  const body = fn.body;
  if (!body) return false;
  let child: ts.Node = target;
  for (let current = target.parent; current && current !== fn; current = current.parent) {
    if (ts.isIfStatement(current) && child !== current.expression) return true;
    if (ts.isConditionalExpression(current) && child !== current.condition) return true;
    if (ts.isCaseClause(current) || ts.isDefaultClause(current)) return true;
    if (ts.isBinaryExpression(current) && child === current.right && (
      current.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken ||
      current.operatorToken.kind === ts.SyntaxKind.BarBarToken ||
      current.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken
    )) return true;
    if (current === body && ts.isBlock(body)) {
      const index = body.statements.indexOf(child as ts.Statement);
      return body.statements.slice(0, Math.max(index, 0)).some(exitsEarly);
    }
    child = current;
  }
  return false;
}

function exitsEarly(statement: ts.Statement): boolean {
  if (!ts.isIfStatement(statement)) return false;
  let exits = false;
  const visit = (node: ts.Node) => {
    if (exits || isFunctionNode(node)) return;
    if (ts.isReturnStatement(node)) {
      exits = true;
      return;
    }
    ts.forEachChild(node, visit);
  };
  visit(statement.thenStatement);
  return exits;
}

const SINGLE_CHILD_CONTROL_FLOW = new Set(["Show", "Match", "ErrorBoundary", "Suspense"]);

/** The function a JSX site renders in, looking through conditional one-child callbacks. */
function hostFunction(node: ts.Node, control: ReadonlyMap<string, string>): FunctionNode | undefined {
  let fn = enclosingFunction(node);
  while (fn && ts.isJsxExpression(fn.parent)) {
    const element = fn.parent.parent;
    const tag = ts.isJsxElement(element) ? element.openingElement.tagName
      : ts.isJsxAttribute(element) && ts.isJsxAttributes(element.parent) ? element.parent.parent.tagName
        : undefined;
    if (!tag || !ts.isIdentifier(tag) || !SINGLE_CHILD_CONTROL_FLOW.has(control.get(tag.text) ?? "")) break;
    fn = enclosingFunction(fn);
  }
  return fn;
}

export class RegistrationLifetimes {
  readonly #corpus: SourceCorpus<TypeScriptSourceFile>;
  readonly #solidImports = new Map<TypeScriptSourceFile, Map<string, string>>();
  readonly #components = new WeakMap<FunctionNode, boolean>();
  #singletons: Set<string> | undefined;

  constructor(corpus: SourceCorpus<TypeScriptSourceFile>) {
    this.#corpus = corpus;
  }

  /** Every component that makes the call, with how long the call's effect lives. */
  holders(site: CallSite): RegistrationHolder[] {
    return this.#holdersFrom(site.file, site.call, [], new Set());
  }

  isComponent(fn: FunctionNode): boolean {
    let component = this.#components.get(fn);
    if (component === undefined) {
      const name = boundName(fn);
      component = Boolean(
        name && /^[A-Z]/.test(name.text) && fn.getSourceFile().fileName.endsWith(".tsx") &&
        fn.body && containsJsx(fn.body),
      );
      this.#components.set(fn, component);
    }
    return component;
  }

  #solid(file: TypeScriptSourceFile): Map<string, string> {
    let names = this.#solidImports.get(file);
    if (!names) {
      names = namedImports(file.ast, (specifier) => specifier === "solid-js");
      this.#solidImports.set(file, names);
    }
    return names;
  }

  #holdersFrom(
    file: TypeScriptSourceFile,
    node: ts.Node,
    via: readonly string[],
    visited: Set<ts.Node>,
  ): RegistrationHolder[] {
    const fn = enclosingFunction(node);
    if (!fn || visited.has(fn)) return [];
    visited.add(fn);
    try {
      return this.#holdersOfFunction(file, fn, node, via, visited);
    } finally {
      visited.delete(fn);
    }
  }

  #holdersOfFunction(
    file: TypeScriptSourceFile,
    fn: FunctionNode,
    target: ts.Node,
    via: readonly string[],
    visited: Set<ts.Node>,
  ): RegistrationHolder[] {
    const componentName = this.isComponent(fn) ? boundName(fn) : undefined;
    if (componentName) {
      const component = componentName.text;
      return [{
        component,
        file: file.rel,
        lifetime: "mount",
        singleton: this.singletons().has(component),
        via,
      }];
    }
    const parent = fn.parent;
    // Callback argument: resolve by what receives it.
    if (ts.isCallExpression(parent) && parent.arguments.includes(fn as ts.Expression)) {
      const name = calleeName(parent);
      const solid = ts.isIdentifier(parent.expression)
        ? this.#solid(file).get(parent.expression.text)
        : undefined;
      if (solid && REACTIVE_SCOPES.has(solid)) {
        if (conditionallyReached(fn, target)) return this.#transient(file, parent, [...via, solid], visited);
        // `on(deps, fn)` lives inside the effect that receives it.
        return this.#holdersFrom(file, parent, [solid, ...via], visited);
      }
      if (solid && MOUNT_SCOPES.has(solid)) {
        return this.#holdersFrom(file, parent, [solid, ...via], visited);
      }
      if (name && SAME_TURN_CALLBACKS.has(name)) {
        return this.#holdersFrom(file, parent, [name, ...via], visited);
      }
      return this.#transient(file, parent, [...via, name ?? "callback"], visited);
    }
    // JSX: event props run on demand; `ref`, render props, and child callbacks render with the host.
    if (ts.isJsxExpression(parent)) {
      const attribute = ts.isJsxAttribute(parent.parent) ? parent.parent : undefined;
      const attributeName = attribute?.name.getText(file.ast);
      if (attributeName && /^on[A-Z:]/.test(attributeName)) {
        return this.#transient(file, parent, [...via, attributeName], visited);
      }
      return this.#holdersFrom(file, parent, [attributeName ?? "render callback", ...via], visited);
    }
    // Object members run when whatever receives the object calls them.
    if (ts.isPropertyAssignment(parent) || ts.isObjectLiteralExpression(parent)) {
      const member = ts.isPropertyAssignment(parent) ? parent.name : "name" in fn ? fn.name : undefined;
      return this.#transient(file, fn, [...via, member?.getText(file.ast) ?? "member"], visited);
    }
    const name = boundName(fn);
    if (!name) return [];
    const holders: RegistrationHolder[] = [];
    if (!isModuleExported(fn)) {
      // Local helper: its lifetime is its callers'.
      holders.push(...this.#referenceHolders(file, declarationScope(fn), name.text, name, via, visited));
      return holders;
    }
    // Library module (.ts) exports are shared infrastructure; a component module's exports are part of its components.
    if (!file.rel.endsWith(".tsx")) return [];
    holders.push(...this.#referenceHolders(file, file.ast, name.text, name, via, visited));
    for (const importer of this.#corpus.files) {
      for (const statement of importer.ast.statements) {
        if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
        if (this.importTarget(importer, statement.moduleSpecifier.text) !== file) continue;
        const bindings = statement.importClause?.namedBindings;
        if (!bindings || !ts.isNamedImports(bindings)) continue;
        for (const element of bindings.elements) {
          if ((element.propertyName ?? element.name).text !== name.text) continue;
          holders.push(...this.#referenceHolders(importer, importer.ast, element.name.text, element.name, via, visited));
        }
      }
    }
    return holders;
  }

  #referenceHolders(
    file: TypeScriptSourceFile,
    scope: ts.Node,
    text: string,
    declaration: ts.Identifier,
    via: readonly string[],
    visited: Set<ts.Node>,
  ): RegistrationHolder[] {
    const holders: RegistrationHolder[] = [];
    const visit = (node: ts.Node) => {
      if (ts.isImportDeclaration(node)) return;
      if (ts.isIdentifier(node) && node.text === text && node !== declaration) {
        const reference = node.parent;
        if (ts.isCallExpression(reference) && reference.expression === node) {
          holders.push(...this.#holdersFrom(file, reference, [text, ...via], visited));
        } else if (ts.isCallExpression(reference) && reference.arguments.includes(node)) {
          const solid = ts.isIdentifier(reference.expression)
            ? this.#solid(file).get(reference.expression.text)
            : undefined;
          if (solid && (MOUNT_SCOPES.has(solid) || REACTIVE_SCOPES.has(solid))) {
            holders.push(...this.#holdersFrom(file, reference, [solid, text, ...via], visited));
          } else {
            holders.push(...this.#transient(file, reference, [...via, text], visited));
          }
        } else if (!ts.isPropertyAccessExpression(reference) || reference.expression === node) {
          holders.push(...this.#transient(file, reference, [...via, text], visited));
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(scope);
    return holders;
  }

  /** The holding components, with the call's lifetime cut to the transient scope. */
  #transient(
    file: TypeScriptSourceFile,
    node: ts.Node,
    via: readonly string[],
    visited: Set<ts.Node>,
  ): RegistrationHolder[] {
    return this.#holdersFrom(file, node, via, visited).map((holder) => ({
      ...holder,
      lifetime: "transient" as const,
      via,
    }));
  }

  /**
   * Components rendered exactly once per window: one JSX site, placed directly
   * (not inside a render callback) in the root `render()` or another singleton.
   */
  singletons(): Set<string> {
    if (this.#singletons) return this.#singletons;
    type Host = { kind: "root" } | { kind: "component"; name: string } | { kind: "callback" };
    const sites = new Map<string, Host[]>();
    for (const file of this.#corpus.files) {
      if (!file.rel.endsWith(".tsx")) continue;
      const renders = namedImports(file.ast, (specifier) => specifier === "solid-js/web");
      const control = this.#solid(file);
      const visit = (node: ts.Node) => {
        const tag = ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node) ? node.tagName : undefined;
        if (tag && ts.isIdentifier(tag) && /^[A-Z]/.test(tag.text)) {
          const fn = hostFunction(node, control);
          let host: Host = { kind: "callback" };
          const componentName = fn && this.isComponent(fn) ? boundName(fn) : undefined;
          if (componentName) host = { kind: "component", name: componentName.text };
          else if (fn && ts.isCallExpression(fn.parent) && ts.isIdentifier(fn.parent.expression) &&
            renders.get(fn.parent.expression.text) === "render") host = { kind: "root" };
          sites.set(tag.text, [...(sites.get(tag.text) ?? []), host]);
        }
        ts.forEachChild(node, visit);
      };
      visit(file.ast);
    }
    const singletons = new Set<string>();
    for (let changed = true; changed;) {
      changed = false;
      for (const [name, hosts] of sites) {
        if (singletons.has(name) || hosts.length !== 1) continue;
        const [host] = hosts;
        if (host!.kind === "root" || (host!.kind === "component" && singletons.has(host!.name))) {
          singletons.add(name);
          changed = true;
        }
      }
    }
    this.#singletons = singletons;
    return singletons;
  }

  /** Resolves a relative import specifier to a corpus file. */
  importTarget(from: TypeScriptSourceFile, specifier: string): TypeScriptSourceFile | undefined {
    if (!specifier.startsWith(".")) return undefined;
    const base = resolve(dirname(from.path), specifier);
    const rel = relative(this.#corpus.root, base).split("\\").join("/");
    return this.#corpus.file(rel) ?? this.#corpus.file(`${rel}.ts`) ?? this.#corpus.file(`${rel}.tsx`) ??
      this.#corpus.file(join(rel, "index.ts"));
  }
}

export function describeHolder(holder: RegistrationHolder): string {
  const hops = holder.via.length > 0 ? ` via ${holder.via.join(" → ")}` : "";
  return `${holder.component}${hops}`;
}
