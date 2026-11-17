import { importMap, isFunctionExpression, isSolidPrimitive, jsxAttribute, walk } from "./ast.js";

export default {
  meta: {
    type: "problem",
    docs: { description: "A non-keyed Show accessor is not called inside a component prop." },
    schema: [],
    messages: {
      leak: "This prop outlives the Show branch and reads its accessor after the value is gone. Add `keyed`, or use ShowLatest when a remount would discard what the user is editing.",
    },
  },
  create(context) {
    const keys = context.sourceCode.visitorKeys;
    let imports = new Map();
    const callsAccessor = (node, accessor) =>
      walk(node, keys, (inner) =>
        inner.type === "CallExpression" && inner.callee.type === "Identifier" && inner.callee.name === accessor);
    return {
      Program(node) {
        imports = importMap(node);
      },
      JSXElement(node) {
        const opening = node.openingElement;
        if (opening.name.type !== "JSXIdentifier" || opening.name.name !== "Show" || jsxAttribute(opening, "keyed")) return;
        for (const child of node.children) {
          if (child.type !== "JSXExpressionContainer" || !isFunctionExpression(child.expression)) continue;
          const [accessor] = child.expression.params;
          if (accessor?.type !== "Identifier") continue;
          walk(child.expression.body, keys, (inner) => {
            if (inner.type !== "JSXOpeningElement" || inner.name.type !== "JSXIdentifier") return;
            if (!/^[A-Z]/.test(inner.name.name) || isSolidPrimitive(inner.name.name, imports)) return;
            for (const attribute of inner.attributes) {
              if (attribute.type !== "JSXAttribute" || attribute.value?.type !== "JSXExpressionContainer") continue;
              if (callsAccessor(attribute.value.expression, accessor.name)) {
                context.report({ node: attribute.value, messageId: "leak" });
              }
            }
          });
        }
      },
    };
  },
};

const FIXTURE = `
  import { Show } from "solid-js";
  declare const subject: () => { id: string } | undefined;
  declare function Card(props: { id: string }): null;
`;

export const cases = [
  { code: "<Show when={subject()}>{(value) => <Card id={value().id} />}</Show>;", errors: 1 },
  { code: "<Show when={subject()} keyed>{(value) => <Card id={value.id} />}</Show>;", errors: 0 },
  { code: "<Show when={subject()}>{(value) => <span title={value().id} />}</Show>;", errors: 0 },
].map((entry) => ({ ...entry, file: "src/components/primitives/DenCheckbox.tsx", code: FIXTURE + entry.code }));
