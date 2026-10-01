import { relative } from "node:path";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import { denSourceRoot, loadTypeScriptCorpus } from "../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const COMPARISONS = new Set([
  ts.SyntaxKind.EqualsEqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsEqualsToken,
  ts.SyntaxKind.EqualsEqualsToken,
  ts.SyntaxKind.ExclamationEqualsToken,
  ts.SyntaxKind.LessThanToken,
  ts.SyntaxKind.LessThanEqualsToken,
  ts.SyntaxKind.GreaterThanToken,
  ts.SyntaxKind.GreaterThanEqualsToken,
]);

const MEMBERSHIP = new Set(["includes", "has", "indexOf"]);

function unwrap(node: ts.Node): ts.Node {
  let current = node;
  while (
    ts.isParenthesizedExpression(current) || ts.isNonNullExpression(current)
    || ts.isAsExpression(current) || ts.isSatisfiesExpression(current)
  ) {
    current = current.expression;
  }
  return current;
}

function isStatusRead(node: ts.Node, transportResponse?: (receiver: ts.Expression) => boolean): boolean {
  const target = unwrap(node);
  return ts.isPropertyAccessExpression(target) && target.name.text === "status"
    && !transportResponse?.(target.expression);
}

// An HTTP error status: a numeric literal from 400 through 599.
function isErrorStatus(node: ts.Node): boolean {
  const target = unwrap(node);
  if (!ts.isNumericLiteral(target)) return false;
  const value = Number(target.text);
  return value >= 400 && value <= 599;
}

function holdsErrorStatus(node: ts.Node): boolean {
  let target = unwrap(node);
  if (ts.isNewExpression(target)) {
    const [list] = target.arguments ?? [];
    if (list === undefined) return false;
    target = unwrap(list);
  }
  return ts.isArrayLiteralExpression(target) && target.elements.some(isErrorStatus);
}

/**
 * Lines of `ast` that branch on an HTTP error status read from a `.status`
 * property: a comparison with a 4xx/5xx literal, a `case` of one in a switch
 * over the status, or membership of the status in a literal list of them.
 * Success statuses (204, 202) select a declared response shape and are not
 * error branching. Each line is reported once.
 */
function errorStatusBranches(ast: ts.SourceFile, transportResponse?: (receiver: ts.Expression) => boolean): number[] {
  const statusRead = (node: ts.Node) => isStatusRead(node, transportResponse);
  const lines: number[] = [];
  const report = (node: ts.Node) => {
    lines.push(ast.getLineAndCharacterOfPosition(node.getStart(ast)).line + 1);
  };
  const visit = (node: ts.Node) => {
    if (ts.isBinaryExpression(node) && COMPARISONS.has(node.operatorToken.kind)) {
      if ((statusRead(node.left) && isErrorStatus(node.right)) || (statusRead(node.right) && isErrorStatus(node.left))) {
        report(node);
      }
    } else if (ts.isSwitchStatement(node) && statusRead(node.expression)) {
      for (const clause of node.caseBlock.clauses) {
        if (ts.isCaseClause(clause) && isErrorStatus(clause.expression)) report(clause);
      }
    } else if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)) {
      const [subject] = node.arguments;
      if (
        MEMBERSHIP.has(node.expression.name.text) && subject !== undefined
        && statusRead(subject) && holdsErrorStatus(node.expression.expression)
      ) {
        report(node);
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(ast);
  return [...new Set(lines)];
}

describe("Den error handling", () => {
  it("branches on the error code, never on an HTTP error status", () => {
    const candidates = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files
      .filter(({ ast }) => errorStatusBranches(ast).length > 0);
    const program = ts.createProgram(candidates.map(({ path }) => path), {
      target: ts.ScriptTarget.ESNext, module: ts.ModuleKind.ESNext,
      moduleResolution: ts.ModuleResolutionKind.Bundler, skipLibCheck: true,
    });
    const checker = program.getTypeChecker();
    // Raw HTTP responses select transport behavior before a domain error exists.
    const transportResponse = (receiver: ts.Expression) => {
      const type = checker.getTypeAtLocation(receiver);
      const types = type.isUnion() ? type.types : [type];
      return types.every((part) => part.getSymbol()?.name === "Response"
        && part.getSymbol()?.declarations?.some((decl) => program.isSourceFileDefaultLibrary(decl.getSourceFile())));
    };
    const branches: string[] = [];
    for (const candidate of candidates) {
      const ast = program.getSourceFile(candidate.path)!;
      const file = relative(denSourceRoot, ast.fileName).split("\\").join("/");
      for (const line of errorStatusBranches(ast, transportResponse)) branches.push(`${file}:${line}`);
    }
    expect(branches).toEqual([]);
  }, VITEST_REPOSITORY_SCAN_TIMEOUT_MS);

  it("recognizes each form of status branching and ignores other statuses", () => {
    const source = [
      "if (err.status === 404) {}", // 1: flagged
      "if (409 !== error.status) {}", // 2: flagged
      "if ((cause as LycaonApiError).status >= 500) {}", // 3: flagged
      "switch (response.status) {", // 4
      "  case 409: break;", // 5: flagged
      "  case 204: break;", // 6: success shape
      "}",
      "if ([401, 403].includes(err.status)) {}", // 8: flagged
      "if (new Set([429]).has(err.status)) {}", // 9: flagged
      "if (response.status === 204) {}", // 10: success shape
      "if (task.status === \"failed\") {}", // 11: not an HTTP status
      "if (exit.status === 0) {}", // 12: not an HTTP status
      "if (err.code === \"session_not_found\") {}", // 13: the rule
    ].join("\n");
    const ast = ts.createSourceFile("fixture.ts", source, ts.ScriptTarget.Latest, true);
    expect(errorStatusBranches(ast)).toEqual([1, 2, 3, 5, 8, 9]);
  });
});
