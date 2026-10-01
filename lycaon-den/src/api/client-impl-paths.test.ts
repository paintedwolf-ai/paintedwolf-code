import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import * as ts from "typescript";
import { API_OPERATIONS } from "./operations.generated.ts";

const clientSources = [
  "client-impl.ts",
  ...readdirSync(join(import.meta.dirname, "http-capabilities"))
    .filter((name) => name.endsWith(".ts") && !name.endsWith(".test.ts"))
    .sort().map((name) => `http-capabilities/${name}`),
].map((file) => {
  const source = readFileSync(join(import.meta.dirname, file), "utf8");
  return { file, source, ast: ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true) };
});

type PathLiteral = { file: string; line: number; raw: string; pathname: string };

function interpolations(raw: string): { expr: string; start: number; end: number }[] {
  const out: { expr: string; start: number; end: number }[] = [];
  let i = 0;
  while (i < raw.length) {
    const start = raw.indexOf("${", i);
    if (start < 0) break;
    let depth = 0;
    let j = start + 1;
    for (; j < raw.length; j++) {
      const ch = raw[j];
      if (ch === "{") depth += 1;
      else if (ch === "}") {
        depth -= 1;
        if (depth === 0) break;
      }
    }
    out.push({ expr: raw.slice(start + 2, j), start, end: j + 1 });
    i = j + 1;
  }
  return out;
}

// Query-named interpolations add parameters; other interpolations fill path segments.
function resolveTemplate(raw: string): string {
  let resolved = "";
  let cursor = 0;
  for (const part of interpolations(raw)) {
    resolved += raw.slice(cursor, part.start);
    resolved += /query/i.test(part.expr) ? "" : "x";
    cursor = part.end;
  }
  resolved += raw.slice(cursor);
  return resolved.split("?", 1)[0] ?? resolved;
}

function clientPathLiterals(): PathLiteral[] {
  const literals: PathLiteral[] = [];
  for (const { file, source } of clientSources) {
    const lines = source.split("\n");
    lines.forEach((text, index) => {
      for (const match of text.matchAll(/`(\/v1\/[^`]*)`|"(\/v1\/[^"]*)"/g)) {
        const raw = match[1] ?? match[2] ?? "";
        literals.push({ file, line: index + 1, raw, pathname: resolveTemplate(raw) });
      }
    });
  }
  return literals;
}

function operationPattern(template: string): RegExp {
  const escaped = template.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^${escaped.replace(/\\\{[^{}]+\\\}/g, "[^/]+")}$`);
}

const operationPatterns = Object.values(API_OPERATIONS).map((operation) =>
  operationPattern(operation.path),
);

const requestHelperAst = ts.createSourceFile(
  "http.ts", readFileSync(join(import.meta.dirname, "http.ts"), "utf8"), ts.ScriptTarget.Latest, true,
);
const jsonRequestDeclaration = requestHelperAst.statements.find((node) =>
  ts.isFunctionDeclaration(node) && node.name?.text === "jsonRequest",
);
const jsonRequestReturn = jsonRequestDeclaration && ts.isFunctionDeclaration(jsonRequestDeclaration)
  ? jsonRequestDeclaration.body?.statements.find(ts.isReturnStatement)?.expression
  : undefined;

// Later object properties override earlier spreads.
function requestMethod(
  node: ts.Expression | undefined,
  current = "GET",
  declaredMethod?: string,
): string | undefined {
  if (!node) return current;
  if (ts.isIdentifier(node) && node.text === "undefined") return current;
  if (ts.isParenthesizedExpression(node)) return requestMethod(node.expression, current);
  if (ts.isConditionalExpression(node)) {
    const yes = requestMethod(node.whenTrue, current);
    return yes === requestMethod(node.whenFalse, current) ? yes : undefined;
  }
  if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "jsonRequest") {
    const method = node.arguments[0];
    return jsonRequestReturn && method && ts.isStringLiteral(method)
      ? requestMethod(jsonRequestReturn, current, method.text)
      : undefined;
  }
  if (!ts.isObjectLiteralExpression(node)) return undefined;
  let method: string | undefined = current;
  for (const property of node.properties) {
    if (ts.isSpreadAssignment(property)) {
      method = requestMethod(property.expression, method);
    } else if (ts.isShorthandPropertyAssignment(property) && property.name.text === "method") {
      method = declaredMethod;
    } else if (ts.isPropertyAssignment(property)
      && (ts.isIdentifier(property.name) || ts.isStringLiteral(property.name))
      && property.name.text === "method") {
      method = ts.isStringLiteral(property.initializer) ? property.initializer.text : undefined;
    }
  }
  return method;
}

function clientRequests(): { file: string; line: number; path: string; method: string | undefined }[] {
  const requests: { file: string; line: number; path: string; method: string | undefined }[] = [];
  for (const { file, ast: clientAst } of clientSources) {
    const visit = (node: ts.Node): void => {
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
        const name = node.expression.text;
        const pathIndex = name === "j" ? 0 : ["lycaonFetch", "lycaonBlob", "lycaonDownload"].includes(name) ? 1 : -1;
        const path = node.arguments[pathIndex];
        if (path && (ts.isStringLiteral(path) || ts.isTemplateExpression(path) || ts.isNoSubstitutionTemplateLiteral(path))) {
          const raw = path.getText().slice(1, -1);
          if (raw.startsWith("/v1/")) {
            requests.push({
              file,
              line: clientAst.getLineAndCharacterOfPosition(node.getStart()).line + 1,
              path: resolveTemplate(raw),
              method: requestMethod(node.arguments[pathIndex + (name === "lycaonDownload" ? 2 : 1)]),
            });
          }
        }
      }
      ts.forEachChild(node, visit);
  };
  visit(clientAst);
  }
  return requests;
}

describe("client-impl request paths", () => {
  const literals = clientPathLiterals();

  it("finds the hand-written paths", () => {
    expect(literals.length).toBeGreaterThan(100);
  });

  it("names only operations the generated table serves", () => {
    const unknown = literals
      .filter(
        (literal) =>
          !operationPatterns.some((pattern) => pattern.test(literal.pathname)),
      )
      .map((literal) => `${literal.file}:${literal.line} ${literal.raw}`);
    expect(unknown).toEqual([]);
  });

  it("authors each JSON request method once", () => {
    const duplicates: string[] = [];
    for (const { file, ast: clientAst } of clientSources) {
      const visit = (node: ts.Node): void => {
        if (ts.isObjectLiteralExpression(node)) {
          const writers = node.properties.filter((property) => {
            if (ts.isSpreadAssignment(property)) {
              const call = property.expression;
              return ts.isCallExpression(call) && ts.isIdentifier(call.expression)
                && call.expression.text === "jsonRequest";
            }
            return (ts.isPropertyAssignment(property) || ts.isShorthandPropertyAssignment(property))
              && (ts.isIdentifier(property.name) || ts.isStringLiteral(property.name))
              && property.name.text === "method";
          });
          if (writers.length > 1) duplicates.push(`${file}:${clientAst.getLineAndCharacterOfPosition(node.getStart()).line + 1}`);
        }
        ts.forEachChild(node, visit);
    };
    visit(clientAst);
    }
    expect(duplicates).toEqual([]);
  });

  it("uses a supported HTTP method after applying request option spreads", () => {
    const requests = clientRequests();
    expect(requests.length).toBeGreaterThan(100);
    const unsupported = requests.filter((request) =>
      !Object.values(API_OPERATIONS).some((operation) =>
        operation.method === request.method && operationPattern(operation.path).test(request.path),
      ),
    );
    expect(unsupported).toEqual([]);
  });

  it("resolves explicit helper methods and option precedence", () => {
    const expression = (source: string) => {
      const ast = ts.createSourceFile("request.ts", `const request = ${source}`, ts.ScriptTarget.Latest, true);
      const statement = ast.statements[0];
      if (!statement || !ts.isVariableStatement(statement)) throw new Error("Missing request fixture");
      return statement.declarationList.declarations[0]?.initializer;
    };
    expect(requestMethod(expression('jsonRequest("PUT", {})'))).toBe("PUT");
    expect(requestMethod(expression('{ method: "PATCH", ...jsonRequest("POST", {}) }'))).toBe("POST");
    expect(requestMethod(expression('{ ...jsonRequest("POST", {}), method: "PATCH" }'))).toBe("PATCH");
  });
});
