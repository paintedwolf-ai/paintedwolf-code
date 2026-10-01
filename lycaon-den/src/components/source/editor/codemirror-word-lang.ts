import { StreamLanguage, type StreamParser } from "@codemirror/language";

/** Word-list syntax definitions for languages without a bundled grammar. */
export type WordLangSpec = {
  name: string;
  keywords: string[];
  /** Rendered with the type color (e.g. `uint256`, `felt252`, `String`). */
  types?: string[];
  /** Rendered with the constant color (`true`, `nil`, `msg`, …). */
  atoms?: string[];
  /** Extension-heavy type families (`uint8…uint256`) matched by pattern. */
  typePattern?: RegExp;
  /** Line-comment openers, longest first when one prefixes another. */
  lineComments?: string[];
  blockComment?: [open: string, close: string];
  /** String delimiters; escapes with `\` are honored. */
  strings?: string[];
  /** `$name` reads as a special variable (PHP, Hack). */
  dollarVars?: boolean;
  /** `:name` reads as an atom (Elixir). */
  colonAtoms?: boolean;
  /** `@name` reads as meta (annotations, module attributes, addresses). */
  atMeta?: boolean;
  /** Words starting with an uppercase letter read as type names (Elixir aliases). */
  capitalizedTypes?: boolean;
  /** Keyword lookup ignores case (Apex). */
  caseInsensitive?: boolean;
  /** Extra anchored matches rendered as meta (`<?php`, `?>`). */
  metaPatterns?: RegExp[];
};

type WordLangState = { blockComment: boolean };

const WORD_RE = /^[A-Za-z_][A-Za-z0-9_]*/;
// Trailing letters cover unit/duration suffixes (5m in PromQL, 10u64 in Move).
const NUMBER_RE = /^(?:0[xXbBoO][0-9a-fA-F_]+|\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?[a-zA-Z]*)/;

function toSet(words: string[] | undefined, caseInsensitive: boolean): Set<string> {
  return new Set(
    (words ?? []).map((w) => (caseInsensitive ? w.toLowerCase() : w)),
  );
}

export function wordLanguage(spec: WordLangSpec): StreamLanguage<unknown> {
  const ci = spec.caseInsensitive === true;
  const keywords = toSet(spec.keywords, ci);
  const types = toSet(spec.types, ci);
  const atoms = toSet(spec.atoms, ci);
  const lineComments = spec.lineComments ?? [];
  const strings = spec.strings ?? ['"', "'"];

  const consumeString = (
    stream: Parameters<StreamParser<WordLangState>["token"]>[0],
    quote: string,
  ): string => {
    let escaped = false;
    while (!stream.eol()) {
      const ch = stream.next();
      if (escaped) {
        escaped = false;
      } else if (ch === "\\") {
        escaped = true;
      } else if (ch === quote) {
        break;
      }
    }
    return "string";
  };

  const parser: StreamParser<WordLangState> = {
    name: spec.name,
    startState: () => ({ blockComment: false }),
    token(stream, state) {
      if (state.blockComment) {
        const close = spec.blockComment?.[1] ?? "*/";
        while (!stream.eol()) {
          if (stream.match(close)) {
            state.blockComment = false;
            return "comment";
          }
          stream.next();
        }
        return "comment";
      }
      if (stream.eatSpace()) return null;

      for (const re of spec.metaPatterns ?? []) {
        if (stream.match(re)) return "meta";
      }
      for (const lc of lineComments) {
        if (stream.match(lc)) {
          stream.skipToEnd();
          return "comment";
        }
      }
      if (spec.blockComment && stream.match(spec.blockComment[0])) {
        state.blockComment = true;
        while (!stream.eol()) {
          if (stream.match(spec.blockComment[1])) {
            state.blockComment = false;
            break;
          }
          stream.next();
        }
        return "comment";
      }

      const ch = stream.peek() ?? "";
      if (strings.includes(ch)) {
        stream.next();
        return consumeString(stream, ch);
      }
      if (stream.match(NUMBER_RE)) return "number";
      if (spec.dollarVars && ch === "$") {
        stream.next();
        if (stream.match(WORD_RE)) return "variableName.special";
        return null;
      }
      if (spec.colonAtoms && ch === ":") {
        stream.next();
        if (stream.match(WORD_RE)) return "atom";
        return null;
      }
      if (spec.atMeta && ch === "@") {
        stream.next();
        stream.match(WORD_RE);
        return "meta";
      }

      const word = stream.match(WORD_RE);
      if (word) {
        const raw = (word as RegExpMatchArray)[0];
        const text = ci ? raw.toLowerCase() : raw;
        if (keywords.has(text)) return "keyword";
        if (types.has(text)) return "typeName";
        if (atoms.has(text)) return "atom";
        if (spec.typePattern?.test(raw)) return "typeName";
        if (spec.capitalizedTypes && /^[A-Z]/.test(raw)) return "typeName";
        return "variableName";
      }

      stream.next();
      return null;
    },
    languageData: {
      commentTokens: {
        line: lineComments[0],
        block: spec.blockComment
          ? { open: spec.blockComment[0], close: spec.blockComment[1] }
          : undefined,
      },
    },
  };

  return StreamLanguage.define(parser);
}
