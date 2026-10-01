import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import {
  denSourceRoot,
  loadTypeScriptCorpus,
  requireNonEmpty,
  type TypeScriptSourceFile,
} from "../test/source-corpus.ts";
import {
  describeHolder,
  line,
  RegistrationLifetimes,
  type RegistrationHolder,
} from "../test/registration-lifetime.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

/** Global handler registrations are instance-keyed or scoped to active resident presence. */

const REGISTRIES = ["shortcuts/dispatcher.ts", "notices/notice-actions.ts"];

type Registration = {
  readonly name: string;
  /** Parameter positions that identify the registering instance. */
  readonly instanceKeys: readonly number[];
};

function exported(node: ts.FunctionDeclaration): boolean {
  return ts.getModifiers(node)?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword) ?? false;
}

/** Parameters the body validates against a generated catalog are catalog ids, not instance keys. */
function catalogValidated(fn: ts.FunctionDeclaration, parameter: string, generated: ReadonlySet<string>): boolean {
  let validated = false;
  const visit = (node: ts.Node) => {
    if (validated) return;
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && generated.has(node.expression.text) &&
      node.arguments.some((argument) => ts.isIdentifier(argument) && argument.text === parameter)) {
      validated = true;
      return;
    }
    ts.forEachChild(node, visit);
  };
  if (fn.body) visit(fn.body);
  return validated;
}

/** Exported functions that take handlers and hand back their unregister function. */
function registrations(file: TypeScriptSourceFile): Registration[] {
  const generated = new Set<string>();
  for (const statement of file.ast.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
    if (!/\.generated(?:\.ts)?$/.test(statement.moduleSpecifier.text)) continue;
    const bindings = statement.importClause?.namedBindings;
    if (bindings && ts.isNamedImports(bindings)) for (const element of bindings.elements) generated.add(element.name.text);
  }
  const out: Registration[] = [];
  for (const statement of file.ast.statements) {
    if (!ts.isFunctionDeclaration(statement) || !statement.name || !exported(statement)) continue;
    if (!statement.type || !ts.isFunctionTypeNode(statement.type) || statement.parameters.length === 0) continue;
    const instanceKeys = statement.parameters.flatMap((parameter, index) =>
      parameter.type?.kind === ts.SyntaxKind.StringKeyword && ts.isIdentifier(parameter.name) &&
      !catalogValidated(statement, parameter.name.text, generated) ? [index] : []);
    out.push({ name: statement.name.text, instanceKeys });
  }
  return out;
}

type Finding = { site: string; api: string; holder: RegistrationHolder };

function findings(): Finding[] {
  const corpus = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true });
  const lifetimes = new RegistrationLifetimes(corpus);
  const registries = REGISTRIES.map((rel) => {
    const file = corpus.file(rel);
    if (!file) expect.fail(`registry module missing: ${rel}`);
    return { file, apis: new Map(registrations(file).map((api) => [api.name, api])) };
  });
  requireNonEmpty("registration APIs", registries.flatMap(({ apis }) => [...apis.values()]));
  const out: Finding[] = [];
  for (const file of corpus.files) {
    const imported = new Map<string, Registration>();
    for (const statement of file.ast.statements) {
      if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) continue;
      const target = lifetimes.importTarget(file, statement.moduleSpecifier.text);
      const registry = registries.find((entry) => entry.file === target);
      const bindings = statement.importClause?.namedBindings;
      if (!registry || !bindings || !ts.isNamedImports(bindings)) continue;
      for (const element of bindings.elements) {
        const api = registry.apis.get((element.propertyName ?? element.name).text);
        if (api) imported.set(element.name.text, api);
      }
    }
    if (imported.size === 0) continue;
    const visit = (node: ts.Node) => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
        const api = imported.get(node.expression.text);
        const keyed = api?.instanceKeys.some((index) => {
          const argument = node.arguments[index];
          return argument !== undefined && !ts.isStringLiteralLike(argument);
        });
        if (api && !keyed) {
          for (const holder of lifetimes.holders({ file, call: node })) {
            out.push({ site: `${file.rel}:${line(file, node)}`, api: api.name, holder });
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(file.ast);
  }
  return out;
}

describe("global handler scoping", () => {
  it("every component registration into a global handler registry is instance-keyed or scoped to an active state", () => {
    const all = requireNonEmpty("component registrations", findings());
    const violations = all.filter(({ holder }) => holder.lifetime === "mount" && !holder.singleton);
    expect(
      [...new Set(violations.map(({ site, api, holder }) => `${site} ${api} — ${describeHolder(holder)}`))],
      "A component registers a global command handler or notice action sink for its whole mount. " +
        "Retained (resident) instances keep answering while inactive, and the last instance to mount wins. " +
        "Register inside an effect gated on useResidentPresence() === \"active\", or key the registration " +
        "by its instance (for example the session id) so the registry routes to the registering instance.",
    ).toEqual([]);
  }, VITEST_REPOSITORY_SCAN_TIMEOUT_MS);
});
