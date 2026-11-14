/** How Pretty lays out a language. Every catalog language declares one. */
export type PrettyLayout =
  | { readonly kind: "data"; readonly comments: boolean; readonly records: boolean }
  | { readonly kind: "markup" }
  | { readonly kind: "code"; readonly dialect: "script" | "style" | "style-line-comments" }
  | { readonly kind: "bracket"; readonly statements: boolean }
  | { readonly kind: "clause" }
  | { readonly kind: "fenced" }
  | { readonly kind: "none" };

export const DATA: PrettyLayout = { kind: "data", comments: false, records: false };
export const DATA_WITH_COMMENTS: PrettyLayout = { kind: "data", comments: true, records: false };
export const DATA_RECORDS: PrettyLayout = { kind: "data", comments: false, records: true };
export const MARKUP: PrettyLayout = { kind: "markup" };
export const SCRIPT: PrettyLayout = { kind: "code", dialect: "script" };
export const STYLE: PrettyLayout = { kind: "code", dialect: "style" };
export const STYLE_WITH_LINE_COMMENTS: PrettyLayout = { kind: "code", dialect: "style-line-comments" };
export const BRACKET_STATEMENTS: PrettyLayout = { kind: "bracket", statements: true };
export const BRACKET: PrettyLayout = { kind: "bracket", statements: false };
export const CLAUSE: PrettyLayout = { kind: "clause" };
export const FENCED: PrettyLayout = { kind: "fenced" };
export const NO_LAYOUT: PrettyLayout = { kind: "none" };
