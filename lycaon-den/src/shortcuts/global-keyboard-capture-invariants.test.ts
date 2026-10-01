import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import { denSourceRoot, loadTypeScriptCorpus, requireNonEmpty } from "../test/source-corpus.ts";
import {
  describeHolder,
  line,
  RegistrationLifetimes,
  type RegistrationHolder,
} from "../test/registration-lifetime.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

/** Global key listeners are transient or singleton; shortcuts route through command dispatch or escape ladder. */

const KEY_EVENTS = new Set(["keydown", "keyup"]);
const GLOBAL_TARGETS = new Set(["window", "document", "globalThis", "self"]);
const GLOBAL_TARGET_PROPERTIES = new Set(["ownerDocument", "defaultView", "document"]);
const LISTENER_HELPERS = new Set(["makeEventListener", "createEventListener", "useEventListener"]);

function isGlobalTarget(expression: ts.Expression): boolean {
  if (ts.isIdentifier(expression)) return GLOBAL_TARGETS.has(expression.text);
  return ts.isPropertyAccessExpression(expression) && GLOBAL_TARGET_PROPERTIES.has(expression.name.text);
}

function keyEvent(node: ts.Expression | undefined): boolean {
  return Boolean(node && ts.isStringLiteralLike(node) && KEY_EVENTS.has(node.text));
}

function isGlobalKeyListener(node: ts.Node): boolean {
  if (ts.isCallExpression(node)) {
    const callee = node.expression;
    if (ts.isPropertyAccessExpression(callee) && callee.name.text === "addEventListener") {
      return isGlobalTarget(callee.expression) && keyEvent(node.arguments[0]);
    }
    if (ts.isIdentifier(callee) && LISTENER_HELPERS.has(callee.text)) {
      const [target, event] = node.arguments;
      return Boolean(target && isGlobalTarget(target)) && keyEvent(event);
    }
  }
  // window.onkeydown = …
  if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.EqualsToken &&
    ts.isPropertyAccessExpression(node.left) && /^onkey(?:down|up)$/.test(node.left.name.text)) {
    return isGlobalTarget(node.left.expression);
  }
  return false;
}

type Finding = { site: string; holder: RegistrationHolder };

function findings(): Finding[] {
  const corpus = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true });
  const lifetimes = new RegistrationLifetimes(corpus);
  const out: Finding[] = [];
  for (const file of corpus.files) {
    const visit = (node: ts.Node) => {
      if (isGlobalKeyListener(node)) {
        for (const holder of lifetimes.holders({ file, call: node })) {
          out.push({ site: `${file.rel}:${line(file, node)}`, holder });
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(file.ast);
  }
  return out;
}

describe("global keyboard capture", () => {
  it("no retained or repeated component holds a window/document key listener for its whole mount", () => {
    const all = requireNonEmpty("component-held global key listeners", findings());
    const violations = all.filter(({ holder }) => holder.lifetime === "mount" && !holder.singleton);
    expect(
      [...new Set(violations.map(({ site, holder }) => `${site} — ${describeHolder(holder)}`))],
      "A component registers a window/document keydown/keyup listener for its whole mount. " +
        "Retained (resident) surfaces keep it after they go inactive, and it bypasses the dispatcher's " +
        "scope gates. Route shortcuts through the command catalog (registerCommandHandler, gated on " +
        "useResidentPresence) and Escape through registerEscapeLadderLayer, or install the listener only " +
        "while the component's own overlay or gesture is open.",
    ).toEqual([]);
  }, VITEST_REPOSITORY_SCAN_TIMEOUT_MS);
});
