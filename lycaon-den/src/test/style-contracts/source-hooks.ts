import { readFileSync } from "node:fs";
import { classTokensFromExpressions, elementClassSites, extractClassHooksFromSource, splitCallArguments } from "./jsx-classes.ts";
import { walkFiles } from "./inventory.ts";


export function collectComponentClassHooks(componentRoots: string[]): Map<string, Set<string>> {
  const byFile = new Map<string, Set<string>>();
  for (const file of componentRoots) {
    const source = readFileSync(file, "utf8");
    const hooks = extractClassHooksFromSource(source);
    byFile.set(file, new Set(hooks));
  }
  return byFile;
}

/** Class tokens referenced by application code. */
export function collectReferencedClassTokens(denSrc: string): Set<string> {
  const files = walkFiles(
    denSrc,
    (p) =>
      (p.endsWith(".ts") || p.endsWith(".tsx")) && !p.endsWith(".test.ts") && !p.endsWith(".test.tsx"),
  );
  const tokens = new Set<string>();
  for (const file of files) {
    const src = readFileSync(file, "utf8");
    for (const hook of extractClassHooksFromSource(src)) tokens.add(hook);
    // Both prefixes the orphan check inspects: btn-* reach markup through
    // variant maps, which the attribute scan above cannot see.
    for (const match of src.matchAll(/["'`]\.?((?:den|btn)-[a-zA-Z0-9_-]+)["'`]/g)) {
      tokens.add(match[1] ?? "");
    }
    for (const match of src.matchAll(/classList\.(?:add|toggle|remove)\(\s*["'`](den-[a-zA-Z0-9_-]+)/g)) {
      tokens.add(match[1] ?? "");
    }
  }
  return tokens;
}

function stripSourceComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/\/\/[^\n]*/g, "");
}

/** Finds bases referenced through dynamic modifier classes. */
export function extractDynamicModifierBases(denSrc: string): Set<string> {
  const bases = new Set<string>();
  const files = walkFiles(
    denSrc,
    (p) =>
      (p.endsWith(".tsx") || p.endsWith(".ts")) &&
      !p.endsWith(".test.ts") &&
      !p.endsWith(".test.tsx"),
  );
  for (const file of files) {
    const src = stripSourceComments(readFileSync(file, "utf8"));
    for (const match of src.matchAll(/`[^`]*?(den-[a-z0-9-]+)--\$\{/g)) {
      const base = match[1];
      if (base) bases.add(base);
    }
  }
  return bases;
}

/** Source ranges of the exported components declared in one module. */
function exportedComponentRanges(src: string): Array<{ name: string; body: string }> {
  const decls = [
    ...src.matchAll(/\bexport\s+(?:function|const)\s+([A-Z][A-Za-z0-9_]*)/g),
  ];
  return decls.map((decl, i) => ({
    name: decl[1] ?? "",
    body: src.slice(decl.index ?? 0, decls[i + 1]?.index ?? src.length),
  }));
}

/** Forwarded classes share the root element with these base classes. */
function extractClassForwardingBases(denSrc: string): Map<string, Set<string>> {
  const bases = new Map<string, Set<string>>();
  const files = walkFiles(
    denSrc,
    (p) => p.endsWith(".tsx") && !p.endsWith(".test.tsx"),
  );
  for (const file of files) {
    const src = stripSourceComments(readFileSync(file, "utf8"));
    // Scoped per declaration: a module's components each have their own base.
    for (const { name, body } of exportedComponentRanges(src)) {
      const merged = new Set<string>();
      for (const call of body.matchAll(/\bcn\s*\(([\s\S]*?)\)/g)) {
        const args = call[1] ?? "";
        if (!/\b[A-Za-z_$][\w$]*\.class\b/.test(args)) continue;
        for (const token of classTokensFromExpressions(splitCallArguments(args)).always) {
          merged.add(token);
        }
      }
      if (merged.size === 0) continue;
      bases.set(name, new Set([...(bases.get(name) ?? []), ...merged]));
    }
  }
  return bases;
}

/** Finds class hooks that always share an element. */
export function findAlwaysCoOccurringHooks(denSrc: string): Map<string, Set<string>> {
  const forwarded = extractClassForwardingBases(denSrc);
  const together = new Map<string, Set<string>>();
  const files = walkFiles(
    denSrc,
    (p) => p.endsWith(".tsx") && !p.endsWith(".test.tsx"),
  );
  for (const file of files) {
    for (const site of elementClassSites(readFileSync(file, "utf8"))) {
      const always = new Set([...site.always, ...(forwarded.get(site.element) ?? [])]);
      for (const hook of [...always, ...site.maybe]) {
        const seen = together.get(hook);
        if (seen === undefined) {
          together.set(hook, new Set([...always].filter((c) => c !== hook)));
          continue;
        }
        for (const carried of [...seen]) {
          if (!always.has(carried)) seen.delete(carried);
        }
      }
    }
  }
  const result = new Map<string, Set<string>>();
  for (const [hook, carried] of together) {
    if (carried.size > 0) result.set(hook, carried);
  }
  return result;
}
