import { parse, type Root } from "postcss";
import { denSourceRoot, loadSourceCorpus, requireNonEmpty, type SourceFile } from "../source-corpus.ts";

type StylesheetRule = {
  readonly selector: string;
  readonly declarations: readonly { readonly prop: string; readonly value: string }[];
  readonly location: string;
};

const parsed = new WeakMap<SourceFile, Root>();

/** Return the read-only-by-convention AST for a physical corpus file. */
export function parseStylesheet(file: SourceFile): Root {
  const cached = parsed.get(file);
  if (cached) return cached;
  const root = parse(file.text, { from: file.path });
  parsed.set(file, root);
  return root;
}

/** Inspect physical rules, including nested rules, once without expanding imports. */
export function stylesheetRules(file: SourceFile): StylesheetRule[] {
  const rules: StylesheetRule[] = [];
  parseStylesheet(file).walkRules((rule) => {
    rules.push({
      selector: rule.selector,
      declarations: rule.nodes.flatMap((node) =>
        node.type === "decl" ? [{ prop: node.prop.toLowerCase(), value: node.value }] : [],
      ),
      location: `${file.rel}:${rule.source?.start?.line ?? 1}`,
    });
  });
  return rules;
}

export function denStylesheetRules(): readonly StylesheetRule[] {
  const { files } = loadSourceCorpus(denSourceRoot, { extensions: [".css"] });
  return requireNonEmpty("Den stylesheet rules",
    requireNonEmpty("Den stylesheets", files).flatMap(stylesheetRules),
  );
}
