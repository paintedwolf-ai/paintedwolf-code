import { parse } from "postcss";
import { denSourceRoot, loadSourceCorpus, requireNonEmpty, type SourceFile } from "../source-corpus.ts";

type StylesheetRule = {
  readonly selector: string;
  readonly declarations: readonly { readonly prop: string; readonly value: string }[];
  readonly location: string;
};

/** Inspect physical rules, including nested rules, once without expanding imports. */
export function stylesheetRules(file: SourceFile): StylesheetRule[] {
  const rules: StylesheetRule[] = [];
  parse(file.text, { from: file.path }).walkRules((rule) => {
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
