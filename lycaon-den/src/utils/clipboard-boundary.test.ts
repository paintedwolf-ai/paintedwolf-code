import { relative } from "node:path";
import * as ts from "typescript";
import { expect, it } from "vitest";
import { denSourceRoot, loadTypeScriptCorpus } from "../test/source-corpus.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

it("routes UI clipboard access through the native-aware bridge", () => {
  const bypasses: string[] = [];
  for (const { ast } of loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files) {
    const file = relative(denSourceRoot, ast.fileName);
    if (file === "utils/clipboard.ts") continue;
    const visit = (node: ts.Node) => {
      const property = ts.isPropertyAccessExpression(node) ? node.name.text
        : ts.isElementAccessExpression(node) && node.argumentExpression && ts.isStringLiteral(node.argumentExpression)
          ? node.argumentExpression.text : undefined;
      if (property === "clipboard") {
        bypasses.push(`${file}:${ast.getLineAndCharacterOfPosition(node.getStart()).line + 1}`);
      }
      ts.forEachChild(node, visit);
    };
    visit(ast);
  }
  expect(bypasses).toEqual([]);
}, VITEST_REPOSITORY_SCAN_TIMEOUT_MS);
