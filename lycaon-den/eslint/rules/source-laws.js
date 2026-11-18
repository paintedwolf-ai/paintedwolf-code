import path from "node:path";

const TEST_FILE = /\.test\.tsx?$/;

function posix(file) {
  return file.split(path.sep).join("/");
}

function matchesAny(file, globs) {
  return globs.some((glob) => path.posix.matchesGlob(file, glob));
}

/** A law governs production files under `files`; `tests: true` extends it to test files. */
export function governs(law, file) {
  if (!matchesAny(file, law.files)) return false;
  if (law.ignores && matchesAny(file, law.ignores)) return false;
  return law.tests === true || !TEST_FILE.test(file);
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
    docs: { description: "Source laws scoped to the files they govern." },
    schema: [{ type: "array", items: { type: "object" } }],
    messages: { law: "{{message}} [{{name}}]" },
  },
  create(context) {
    const file = posix(path.relative(context.cwd, context.filename));
    const laws = context.options[0].filter((law) => governs(law, file));
    const listeners = new Map();
    const listen = (selector, handler) => {
      const handlers = listeners.get(selector) ?? [];
      handlers.push(handler);
      listeners.set(selector, handlers);
    };
    const report = (law, node) =>
      context.report({ node, messageId: "law", data: { message: law.message, name: law.name } });

    for (const law of laws) {
      for (const selector of law.syntax ?? []) listen(selector, (node) => report(law, node));
    }

    const importLaws = laws.filter((law) => law.imports);
    if (importLaws.length > 0) {
      const check = (node) => {
        if (node.source?.type !== "Literal" || typeof node.source.value !== "string") return;
        const module = importedModule(node.source.value, file);
        const names = takenNames(node);
        for (const law of importLaws) {
          for (const ban of law.imports) {
            if (!bansModule(ban, module)) continue;
            if (ban.names && names && !names.some((name) => ban.names.includes(name))) continue;
            report(law, node);
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
