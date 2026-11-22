import path from "node:path";

const TEST_FILE = /\.test\.tsx?$/;

function posix(file) {
  return file.split(path.sep).join("/");
}

function matchesAny(file, globs) {
  return globs.some((glob) => path.posix.matchesGlob(file, glob));
}

/** An invariant governs production files under `files`; `tests: true` extends it to test files. */
export function governs(invariant, file) {
  if (!matchesAny(file, invariant.files)) return false;
  if (invariant.ignores && matchesAny(file, invariant.ignores)) return false;
  return invariant.tests === true || !TEST_FILE.test(file);
}

/** The module a specifier names: a package name, or a path relative to the lint root. */
function importedModule(specifier, file) {
  if (!specifier.startsWith(".")) return specifier;
  return path.posix.normalize(path.posix.join(path.posix.dirname(file), specifier));
}

function bansModule(ban, module) {
  return ban.from.startsWith("src/") || ban.from.startsWith("shared/")
    ? path.posix.matchesGlob(module, ban.from)
    : module === ban.from;
}

/**
 * Names an import or re-export statement takes from its module. `null` means
 * the whole module: a namespace, a side-effect import, `export *`, or `import()`.
 */
function takenNames(node) {
  if (node.type !== "ImportDeclaration" && node.type !== "ExportNamedDeclaration") return null;
  if (node.specifiers.length === 0) return null;
  const names = [];
  for (const specifier of node.specifiers) {
    if (specifier.type === "ImportNamespaceSpecifier") return null;
    if (specifier.type === "ImportDefaultSpecifier") names.push("default");
    else if (specifier.type === "ImportSpecifier") names.push(specifier.imported.name ?? specifier.imported.value);
    else names.push(specifier.local.name ?? specifier.local.value);
  }
  return names;
}

export default {
  meta: {
    type: "problem",
    docs: { description: "Source invariants scoped to the files they govern." },
    schema: [{ type: "array", items: { type: "object" } }],
    messages: { invariant: "{{message}} [{{name}}]" },
  },
  create(context) {
    const file = posix(path.relative(context.cwd, context.filename));
    const invariants = context.options[0].filter((invariant) => governs(invariant, file));
    const listeners = new Map();
    const listen = (selector, handler) => {
      const handlers = listeners.get(selector) ?? [];
      handlers.push(handler);
      listeners.set(selector, handlers);
    };
    const report = (invariant, node) =>
      context.report({ node, messageId: "invariant", data: { message: invariant.message, name: invariant.name } });

    for (const invariant of invariants) {
      for (const selector of invariant.syntax ?? []) listen(selector, (node) => report(invariant, node));
    }

    const importInvariants = invariants.filter((invariant) => invariant.imports);
    if (importInvariants.length > 0) {
      const check = (node) => {
        if (node.source?.type !== "Literal" || typeof node.source.value !== "string") return;
        const module = importedModule(node.source.value, file);
        const names = takenNames(node);
        for (const invariant of importInvariants) {
          for (const ban of invariant.imports) {
            if (!bansModule(ban, module)) continue;
            if (ban.names && names && !names.some((name) => ban.names.includes(name))) continue;
            report(invariant, node);
            break;
          }
        }
      };
      for (const selector of ["ImportDeclaration", "ExportNamedDeclaration", "ExportAllDeclaration", "ImportExpression"]) {
        listen(selector, check);
      }
    }

    return Object.fromEntries(
      [...listeners].map(([selector, handlers]) => [selector, (node) => handlers.forEach((handler) => handler(node))]),
    );
  },
};
