import { importMap, isSolidPrimitive, jsxAttribute } from "./ast.js";

export default {
  meta: {
    type: "problem",
    docs: { description: "A settings draft is never a keyed subtree identity." },
    schema: [],
    messages: {
      keyed: "A settings edit replaces its draft object, so keying on it remounts the form. Use ShowLatest or a stable subject identity.",
    },
  },
  create(context) {
    let imports = new Map();
    const drafts = new Set();
    const accessors = new Set();
    const keyedOpenings = [];
    const isDraftValue = (node) =>
      node.type === "MemberExpression" && !node.computed && node.object.type === "Identifier"
      && drafts.has(node.object.name) && node.property.name === "value";
    return {
      Program(node) {
        imports = importMap(node);
      },
      VariableDeclarator(node) {
        if (node.id.type !== "Identifier" || !node.init) return;
        const value = node.init;
        if (value.type === "CallExpression" && value.callee.type === "Identifier"
          && imports.get(value.callee.name)?.endsWith("/settings-draft.ts")) {
          drafts.add(node.id.name);
        } else if (isDraftValue(value)) {
          accessors.add(node.id.name);
        }
      },
      JSXOpeningElement(node) {
        if (node.name.type !== "JSXIdentifier" || !isSolidPrimitive(node.name.name, imports)) return;
        const keyed = jsxAttribute(node, "keyed");
        if (!keyed) return;
        const explicit = keyed.value?.type === "JSXExpressionContainer" ? keyed.value.expression : undefined;
        if (explicit?.type === "Literal" && explicit.value === false) return;
        keyedOpenings.push(node);
      },
      "Program:exit"() {
        for (const opening of keyedOpenings) {
          const when = jsxAttribute(opening, "when")?.value;
          const subject = when?.type === "JSXExpressionContainer" ? when.expression : undefined;
          if (subject?.type !== "CallExpression") continue;
          const callee = subject.callee;
          if ((callee.type === "Identifier" && accessors.has(callee.name)) || isDraftValue(callee)) {
            context.report({ node: opening, messageId: "keyed" });
          }
        }
      },
    };
  },
};

const FIXTURE = `
  import { Show } from "solid-js";
  import { createSettingsDraft } from "../../settings/settings-draft.ts";
  const state = createSettingsDraft<{ id: string } | null>(null);
  const draft = state.value;
`;

export const cases = [
  { code: "<Show when={draft()} keyed>{() => <input />}</Show>;", errors: 1 },
  { code: "<Show when={state.value()} keyed>{() => <input />}</Show>;", errors: 1 },
  { code: "<Show when={draft()?.id} keyed>{() => <input />}</Show>;", errors: 0 },
  { code: "<Show when={draft()}>{() => <input />}</Show>;", errors: 0 },
  { code: "<Show when={draft()} keyed={false}>{() => <input />}</Show>;", errors: 0 },
].map((entry) => ({ ...entry, file: "src/components/primitives/DenCheckbox.tsx", code: FIXTURE + entry.code }));
