import { importMap, isFunctionExpression, isSolidPrimitive } from "./ast.js";

const COMPUTATION_FACTORIES = new Set(["createComputed", "createEffect", "createMemo", "createRenderEffect"]);

export default {
  meta: {
    type: "problem",
    docs: { description: "JSX callbacks create no owner-bound computations." },
    schema: [],
    messages: {
      computation: "A JSX callback runs without a reactive owner. Create this in the component body, or use reactive JSX props.",
    },
  },
  create(context) {
    let imports = new Map();
    // List callbacks run under an item scope; KeyedIndex renders its children inside For.
    const ownsAnItemScope = (container) => {
      const element = container.parent;
      if (element.type !== "JSXElement" || element.openingElement.name.type !== "JSXIdentifier") return false;
      const tag = element.openingElement.name.name;
      if (tag === "KeyedIndex") return imports.get(tag)?.endsWith("/keyed-index.tsx") === true;
      return (tag === "For" || tag === "Index") && isSolidPrimitive(tag, imports);
    };
    return {
      Program(node) {
        imports = importMap(node);
      },
      CallExpression(node) {
        if (node.callee.type !== "Identifier" || !COMPUTATION_FACTORIES.has(node.callee.name)) return;
        for (let ancestor = node.parent; ancestor; ancestor = ancestor.parent) {
          if (!isFunctionExpression(ancestor) || ancestor.parent.type !== "JSXExpressionContainer") continue;
          if (!ownsAnItemScope(ancestor.parent)) context.report({ node, messageId: "computation" });
          return;
        }
      },
    };
  },
};

const FIXTURE = `
  import { createMemo, For, Show } from "solid-js";
  declare const rows: () => string[];
`;

export const cases = [
  { code: "<Show when={rows()}>{() => { const count = createMemo(() => rows().length); return <span>{count()}</span>; }}</Show>;", errors: 1 },
  { code: "<button onClick={() => { createMemo(() => rows().length); }} />;", errors: 1 },
  { code: "<For each={rows()}>{(row) => { const upper = createMemo(() => row.toUpperCase()); return <span>{upper()}</span>; }}</For>;", errors: 0 },
  { code: "const count = createMemo(() => rows().length); <span>{count()}</span>;", errors: 0 },
].map((entry) => ({ ...entry, file: "src/components/primitives/DenCheckbox.tsx", code: FIXTURE + entry.code }));
