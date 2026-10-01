export type SearchQueryToken = {
  kind: "text" | "filter" | "and" | "or" | "not" | "lparen" | "rparen";
  raw: string;
  text: string;
  start: number;
  end: number;
  field?: string;
  value?: string;
};

const WORD_CHAR = /[\p{L}\p{N}_\-./*#']/u;
const FIELD_START = /[A-Za-z]/;
const FIELD_CHAR = /[A-Za-z0-9_]/;

function skipWhitespace(query: string, from: number): number {
  let i = from;
  while (i < query.length && /\s/u.test(query[i]!)) i++;
  return i;
}

/** End of a quoted phrase, exclusive; a missing closing quote runs to the end. */
function quotedEnd(query: string, from: number): { end: number; closed: boolean } {
  const quote = query[from];
  let i = from + 1;
  while (i < query.length) {
    if (query[i] === quote) return { end: i + 1, closed: true };
    if (query[i] === "\\" && i + 1 < query.length) i += 2;
    else i++;
  }
  return { end: query.length, closed: false };
}

function quotedBody(query: string, from: number): { text: string; end: number } {
  const { end, closed } = quotedEnd(query, from);
  const raw = query.slice(from + 1, closed ? end - 1 : end);
  return { text: unescapeQuoted(raw, query[from]!), end };
}

/**
 * Resolve exactly the escapes the host lexer resolves: the delimiting quote
 * and the backslash. A backslash before any other character stays literal.
 */
function unescapeQuoted(raw: string, quote: string): string {
  let out = "";
  for (let i = 0; i < raw.length; i++) {
    if (
      raw[i] === "\\" &&
      i + 1 < raw.length &&
      (raw[i + 1] === quote || raw[i + 1] === "\\")
    ) {
      out += raw[i + 1];
      i++;
      continue;
    }
    out += raw[i];
  }
  return out;
}

function wordEnd(query: string, from: number): number {
  let i = from;
  while (i < query.length) {
    const codePoint = query.codePointAt(i);
    if (codePoint === undefined) break;
    const char = String.fromCodePoint(codePoint);
    if (!WORD_CHAR.test(char)) break;
    i += char.length;
  }
  return i;
}

function fieldEnd(query: string, from: number): number {
  if (!FIELD_START.test(query[from] ?? "")) return from;
  let i = from + 1;
  while (i < query.length && FIELD_CHAR.test(query[i]!)) i++;
  return i;
}

function filterValueEnd(query: string, from: number): number {
  let i = from;
  while (i < query.length && !/\s/u.test(query[i]!) && query[i] !== ")") i++;
  return i;
}

function token(
  kind: SearchQueryToken["kind"],
  query: string,
  start: number,
  end: number,
  text = query.slice(start, end),
): SearchQueryToken {
  return { kind, raw: query.slice(start, end), text, start, end };
}

function scanFilter(
  query: string,
  start: number,
  endOfField: number,
  allowedFields: ReadonlySet<string>,
): { token?: SearchQueryToken; next: number } {
  if (query[endOfField] !== ":") return { next: endOfField };
  const field = query.slice(start, endOfField).toLowerCase();
  if (!allowedFields.has(field)) {
    const valueStart = endOfField + 1;
    const next = valueStart < query.length && !/\s/u.test(query[valueStart]!)
      ? filterValueEnd(query, valueStart)
      : valueStart;
    return { next };
  }

  const valueStart = skipWhitespace(query, endOfField + 1);
  if (valueStart >= query.length || query[valueStart] === ")") {
    return {
      next: valueStart,
      token: {
        ...token("filter", query, start, valueStart),
        field,
        value: "",
      },
    };
  }
  if (query[valueStart] === '"' || query[valueStart] === "'") {
    const { text, end } = quotedBody(query, valueStart);
    return {
      next: end,
      token: {
        ...token("filter", query, start, end),
        field,
        value: text,
      },
    };
  }
  const end = filterValueEnd(query, valueStart);
  return {
    next: end,
    token: {
      ...token("filter", query, start, end),
      field,
      value: query.slice(valueStart, end),
    },
  };
}

/**
 * Tokenize a query as the host lexer does: unknown punctuation separates
 * words, an open quote runs to the end, a stray ")" is dropped.
 */
export function scanSearchQuery(
  query: string,
  allowedFields: ReadonlySet<string>,
): SearchQueryToken[] {
  const tokens: SearchQueryToken[] = [];
  let i = 0;
  let depth = 0;
  while (i < query.length) {
    if (/\s/u.test(query[i]!)) {
      i++;
      continue;
    }
    if (query[i] === "(") {
      depth++;
      tokens.push(token("lparen", query, i, i + 1));
      i++;
      continue;
    }
    if (query[i] === ")") {
      if (depth > 0) {
        depth--;
        tokens.push(token("rparen", query, i, i + 1));
      }
      i++;
      continue;
    }
    if (query[i] === '"' || query[i] === "'") {
      const { text, end } = quotedBody(query, i);
      if (text.trim()) tokens.push(token("text", query, i, end, text));
      i = end;
      continue;
    }

    const endOfField = fieldEnd(query, i);
    if (endOfField > i && query[endOfField] === ":") {
      const scanned = scanFilter(query, i, endOfField, allowedFields);
      if (scanned.token) {
        tokens.push(scanned.token);
      } else {
        tokens.push(token("text", query, i, scanned.next));
      }
      i = Math.max(scanned.next, i + 1);
      continue;
    }

    const end = wordEnd(query, i);
    if (end > i) {
      // A dot ending a bare word is sentence punctuation, not part of it.
      let wordEndTrimmed = end;
      while (wordEndTrimmed > i && query[wordEndTrimmed - 1] === ".") wordEndTrimmed--;
      if (wordEndTrimmed > i) {
        const raw = query.slice(i, wordEndTrimmed);
        // Operators are uppercase only, as in the host lexer; "not" is a word.
        const kind = raw === "AND" ? "and" : raw === "OR" ? "or" : raw === "NOT" ? "not" : "text";
        tokens.push(token(kind, query, i, wordEndTrimmed));
      }
      i = end;
      continue;
    }
    // Any other punctuation separates words.
    const codePoint = query.codePointAt(i);
    i += codePoint !== undefined && codePoint > 0xffff ? 2 : 1;
  }
  return tokens;
}

type QueryExpr =
  | { kind: "leaf"; token: SearchQueryToken }
  | { kind: "not"; child: QueryExpr }
  | { kind: "and" | "or"; children: QueryExpr[] };

/** Incomplete structure returns null so mid-edit query text stays verbatim. */
class QueryParser {
  private index = 0;

  constructor(private readonly tokens: SearchQueryToken[]) {}

  parse(): QueryExpr | null {
    const expression = this.parseOr();
    return this.index === this.tokens.length ? expression : null;
  }

  private peek(): SearchQueryToken | undefined {
    return this.tokens[this.index];
  }

  private take(): SearchQueryToken | undefined {
    return this.tokens[this.index++];
  }

  private parseOr(): QueryExpr | null {
    const first = this.parseAnd();
    if (!first) return null;
    const children = [first];
    while (this.peek()?.kind === "or") {
      this.take();
      const next = this.parseAnd();
      if (!next) return null;
      children.push(next);
    }
    return children.length === 1 ? first : { kind: "or", children };
  }

  private parseAnd(): QueryExpr | null {
    const first = this.parseUnary();
    if (!first) return null;
    const children = [first];
    while (this.startsUnary(this.peek()) || this.peek()?.kind === "and") {
      if (this.peek()?.kind === "and") this.take();
      const next = this.parseUnary();
      if (!next) return null;
      children.push(next);
    }
    return children.length === 1 ? first : { kind: "and", children };
  }

  private startsUnary(next: SearchQueryToken | undefined): boolean {
    return next?.kind === "not" || next?.kind === "lparen" || next?.kind === "filter" || next?.kind === "text";
  }

  private parseUnary(): QueryExpr | null {
    if (this.peek()?.kind === "not") {
      this.take();
      const child = this.parseUnary();
      return child ? { kind: "not", child } : null;
    }
    return this.parsePrimary();
  }

  private parsePrimary(): QueryExpr | null {
    const next = this.peek();
    if (!next) return null;
    if (next.kind === "lparen") {
      this.take();
      const child = this.parseOr();
      if (!child || this.peek()?.kind !== "rparen") return null;
      this.take();
      return child;
    }
    if (next.kind !== "filter" && next.kind !== "text") return null;
    this.take();
    return { kind: "leaf", token: next };
  }
}

/** Number of unquoted words from which a pasted sentence is likely meant literally. */
const PHRASE_HINT_MIN_WORDS = 3;

/**
 * True when the query is several bare words and no phrase: the words match in
 * any order, which a sentence pasted from a file rarely intends.
 */
export function suggestsQuotingAsPhrase(tokens: readonly SearchQueryToken[]): boolean {
  let bareWords = 0;
  for (const current of tokens) {
    if (current.kind !== "text") continue;
    if (current.raw.startsWith('"') || current.raw.startsWith("'")) return false;
    bareWords++;
  }
  return bareWords >= PHRASE_HINT_MIN_WORDS;
}

function precedence(expression: QueryExpr): number {
  if (expression.kind === "or") return 1;
  if (expression.kind === "and") return 2;
  if (expression.kind === "not") return 3;
  return 4;
}

function renderExpression(expression: QueryExpr, parentPrecedence = 0): string {
  const expressionPrecedence = precedence(expression);
  let rendered: string;
  if (expression.kind === "leaf") rendered = expression.token.raw;
  else if (expression.kind === "not") {
    rendered = `NOT ${renderExpression(expression.child, expressionPrecedence)}`;
  }
  else {
    const separator = expression.kind === "or" ? " OR " : " ";
    rendered = expression.children
      .map((child) => renderExpression(child, expressionPrecedence))
      .join(separator);
  }
  return expressionPrecedence < parentPrecedence ? `(${rendered})` : rendered;
}

/**
 * Remove filter tokens textually when the query does not parse. A filter's
 * polarity is the run of NOT tokens before it; removal takes that run too.
 */
function spliceOutFilterRuns(
  query: string,
  tokens: SearchQueryToken[],
  remove: (token: SearchQueryToken, negated: boolean) => boolean,
): string {
  const drop = new Set<number>();
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i]!;
    if (token.kind !== "filter") continue;
    let runStart = i;
    while (runStart > 0 && tokens[runStart - 1]!.kind === "not") runStart--;
    const negated = (i - runStart) % 2 === 1;
    if (!remove(token, negated)) continue;
    for (let j = runStart; j <= i; j++) drop.add(j);
  }
  if (drop.size === 0) return query.replace(/\s{2,}/g, " ").trim();
  let out = "";
  let last = 0;
  tokens.forEach((token, index) => {
    if (!drop.has(index)) return;
    out += query.slice(last, token.start);
    last = token.end;
  });
  out += query.slice(last);
  return out.replace(/\s{2,}/g, " ").trim();
}

/** One recognized filter with the polarity it carries in the expression. */
export type QueryFilterOccurrence = {
  field: string;
  value: string;
  negated: boolean;
};

/**
 * Every filter in the query with its polarity. Falls back to polarity-blind
 * scan tokens while the query is mid-edit and unparseable.
 */
export function collectQueryFilters(
  query: string,
  allowedFields: ReadonlySet<string>,
): QueryFilterOccurrence[] {
  const tokens = scanSearchQuery(query, allowedFields);
  const flat = () => {
    const out: QueryFilterOccurrence[] = [];
    tokens.forEach((token, index) => {
      if (token.kind !== "filter") return;
      let runStart = index;
      while (runStart > 0 && tokens[runStart - 1]!.kind === "not") runStart--;
      out.push({
        field: token.field ?? "",
        value: token.value ?? "",
        negated: (index - runStart) % 2 === 1,
      });
    });
    return out;
  };
  const parsed = new QueryParser(tokens).parse();
  if (!parsed) return flat();
  const out: QueryFilterOccurrence[] = [];
  const walk = (expression: QueryExpr, negated: boolean) => {
    if (expression.kind === "leaf") {
      if (expression.token.kind === "filter") {
        out.push({
          field: expression.token.field ?? "",
          value: expression.token.value ?? "",
          negated,
        });
      }
      return;
    }
    if (expression.kind === "not") {
      walk(expression.child, !negated);
      return;
    }
    for (const child of expression.children) walk(child, negated);
  };
  walk(parsed, false);
  return out;
}

/**
 * Polarity-aware removal: prunes filter leaves whose (field, value, polarity)
 * the predicate matches, re-rendering the expression with parens kept sane.
 */
export function pruneQueryFilters(
  query: string,
  allowedFields: ReadonlySet<string>,
  remove: (occurrence: QueryFilterOccurrence) => boolean,
): string {
  const tokens = scanSearchQuery(query, allowedFields);
  const parsed = new QueryParser(tokens).parse();
  const matchToken = (token: SearchQueryToken, negated: boolean) =>
    token.kind === "filter" &&
    remove({ field: token.field ?? "", value: token.value ?? "", negated });
  if (!parsed) {
    return spliceOutFilterRuns(query, tokens, matchToken);
  }
  const pruned = prunePolarized(parsed, false, matchToken);
  return pruned ? renderExpression(pruned) : "";
}

function prunePolarized(
  expression: QueryExpr,
  negated: boolean,
  remove: (token: SearchQueryToken, negated: boolean) => boolean,
): QueryExpr | null {
  if (expression.kind === "leaf") {
    return remove(expression.token, negated) ? null : expression;
  }
  if (expression.kind === "not") {
    const child = prunePolarized(expression.child, !negated, remove);
    return child ? { kind: "not", child } : null;
  }
  const children = expression.children
    .map((child) => prunePolarized(child, negated, remove))
    .filter((child): child is QueryExpr => child !== null);
  if (children.length === 0) return null;
  if (children.length === 1) return children[0]!;
  return { kind: expression.kind, children };
}
