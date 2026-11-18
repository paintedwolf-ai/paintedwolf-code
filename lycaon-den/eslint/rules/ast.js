/** Local binding name to the module it is imported from. */
export function importMap(program) {
  const map = new Map();
  for (const statement of program.body) {
    if (statement.type !== "ImportDeclaration") continue;
    for (const specifier of statement.specifiers) map.set(specifier.local.name, statement.source.value);
  }
  return map;
}

export function isSolidPrimitive(name, imports) {
  const module = imports.get(name);
  return module === "solid-js" || module === "solid-js/web";
}

/** Visits `node` and its descendants until `visit` returns true. */
export function walk(node, visitorKeys, visit) {
  if (visit(node) === true) return true;
  for (const key of visitorKeys[node.type] ?? []) {
    const child = node[key];
    for (const item of Array.isArray(child) ? child : [child]) {
      if (item && typeof item.type === "string" && walk(item, visitorKeys, visit)) return true;
    }
  }
  return false;
}

export function isFunctionExpression(node) {
  return node.type === "ArrowFunctionExpression" || node.type === "FunctionExpression";
}

export function jsxAttribute(opening, name) {
  return opening.attributes.find((attribute) => attribute.type === "JSXAttribute" && attribute.name.name === name);
}
