const COMPARISONS = new Set(["===", "!==", "==", "!=", "<", "<=", ">", ">="]);
const MEMBERSHIP = new Set(["includes", "has", "indexOf"]);
const WRAPPERS = new Set(["TSNonNullExpression", "TSAsExpression", "TSSatisfiesExpression", "ChainExpression"]);

function unwrap(node) {
  let current = node;
  while (WRAPPERS.has(current.type)) current = current.expression;
  return current;
}

function isErrorStatus(node) {
  const target = unwrap(node);
  return target.type === "Literal" && typeof target.value === "number" && target.value >= 400 && target.value <= 599;
}

function holdsErrorStatus(node) {
  let target = unwrap(node);
  if (target.type === "NewExpression") {
    const [list] = target.arguments;
    if (list === undefined) return false;
    target = unwrap(list);
  }
  return target.type === "ArrayExpression" && target.elements.some((element) => element && isErrorStatus(element));
}

export default {
  meta: {
    type: "problem",
    docs: { description: "Branch on the error code, never on an HTTP error status." },
    schema: [],
    messages: {
      status: "Branch on the error code. An HTTP error status is transport detail; success statuses may select a response shape.",
    },
  },
  create(context) {
    const services = context.sourceCode.parserServices;
    // A raw HTTP response selects transport behavior before a domain error exists.
    const isTransportResponse = (receiver) => {
      const type = services.getTypeAtLocation(receiver);
      const parts = type.isUnion() ? type.types : [type];
      return parts.every((part) => {
        const symbol = part.getSymbol();
        return symbol?.name === "Response"
          && symbol.declarations?.some((declaration) =>
            services.program.isSourceFileDefaultLibrary(declaration.getSourceFile()));
      });
    };
    const isStatusRead = (node) => {
      const target = unwrap(node);
      return target.type === "MemberExpression" && !target.computed && target.property.name === "status"
        && !isTransportResponse(target.object);
    };
    const report = (node) => context.report({ node, messageId: "status" });
    return {
      BinaryExpression(node) {
        if (!COMPARISONS.has(node.operator)) return;
        if ((isErrorStatus(node.right) && isStatusRead(node.left)) || (isErrorStatus(node.left) && isStatusRead(node.right))) {
          report(node);
        }
      },
      SwitchStatement(node) {
        const cases = node.cases.filter((clause) => clause.test && isErrorStatus(clause.test));
        if (cases.length > 0 && isStatusRead(node.discriminant)) cases.forEach(report);
      },
      CallExpression(node) {
        const callee = node.callee;
        const [subject] = node.arguments;
        if (callee.type !== "MemberExpression" || callee.computed || subject === undefined) return;
        if (MEMBERSHIP.has(callee.property.name) && holdsErrorStatus(callee.object) && isStatusRead(subject)) report(node);
      },
    };
  },
};

const DECLARATIONS = `
  declare const err: { status: number; code: string };
  declare const response: Response;
  declare const task: { status: string };
`;

export const cases = [
  { code: "if (err.status === 404) {}", errors: 1 },
  { code: "if (409 !== err.status) {}", errors: 1 },
  { code: "if ((err as { status: number }).status >= 500) {}", errors: 1 },
  { code: "switch (err.status) { case 409: break; case 204: break; }", errors: 1 },
  { code: "if ([401, 403].includes(err.status)) {}", errors: 1 },
  { code: "if (new Set([429]).has(err.status)) {}", errors: 1 },
  { code: "if (err.status === 204) {}", errors: 0 },
  { code: "if (response.status === 404) {}", errors: 0 },
  { code: "switch (response.status) { case 409: break; }", errors: 0 },
  { code: 'if (task.status === "failed") {}', errors: 0 },
  { code: 'if (err.code === "session_not_found") {}', errors: 0 },
].map((entry) => ({ ...entry, file: "src/api/http.ts", code: DECLARATIONS + entry.code }));
