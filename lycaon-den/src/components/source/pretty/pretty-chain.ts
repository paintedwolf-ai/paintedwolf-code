import { PRETTY_MAX_DEPTH, type ScanCursor, type ScanState, targetColumns } from "./pretty-scan-state.ts";

const SPACE = 0x20;
const TAB = 0x09;
const DQUOTE = 0x22;
const SQUOTE = 0x27;
const BACKTICK = 0x60;
const BACKSLASH = 0x5c;
const HASH = 0x23;
const PIPE = 0x7c;
const AMP = 0x26;
const SEMICOLON = 0x3b;
const COMMA = 0x2c;
const OPEN_PAREN = 0x28;
const CLOSE_PAREN = 0x29;
const OPEN_BRACE = 0x7b;
const CLOSE_BRACE = 0x7d;
const OPEN_BRACKET = 0x5b;
const CLOSE_BRACKET = 0x5d;

const DOCKER_EXEC = /^(?:cmd|entrypoint|run|shell)\s+\[/i;
const HEREDOC_RE = /<<-?\s*(?:'([^']+)'|"([^"]+)"|([A-Za-z_][A-Za-z0-9_]*))/g;

export interface ChainState {
  readonly heredocEnd: string | null;
  readonly quote: number;
  readonly depth: number;
  readonly inJsonExec: boolean;
}

export const INITIAL_CHAIN_STATE: ChainState = {
  heredocEnd: null,
  quote: 0,
  depth: 0,
  inJsonExec: false,
};

export function chainStatesEqual(a: ChainState, b: ChainState): boolean {
  return (
    a.heredocEnd === b.heredocEnd &&
    a.quote === b.quote &&
    a.depth === b.depth &&
    a.inJsonExec === b.inJsonExec
  );
}

export function startChainLine(state: ScanState, lineText: string): ChainState {
  const prev = state.chainState;
  if (prev.heredocEnd !== null) {
    if (lineText.trim() === prev.heredocEnd) {
      return { ...prev, heredocEnd: null };
    }
    return prev;
  }
  HEREDOC_RE.lastIndex = 0;
  let heredocEnd: string | null = null;
  const match = HEREDOC_RE.exec(lineText);
  if (match) {
    heredocEnd = match[1] ?? match[2] ?? match[3] ?? null;
  }
  const inJsonExec = DOCKER_EXEC.test(lineText.trimStart());
  return { heredocEnd, quote: prev.quote, depth: 0, inJsonExec };
}

export function closeChainLine(state: ScanState): void {
  state.chainState = { ...state.chainState, depth: 0, inJsonExec: false };
}

function skipQuoted(cursor: ScanCursor, pos: number, end: number, quote: number): { next: number; closed: boolean } {
  const { text, state } = cursor;
  let p = pos;
  while (p < end) {
    const c = text.at(p);
    if (quote !== SQUOTE && c === BACKSLASH && p + 1 < end) {
      p += 2;
      continue;
    }
    if (c === quote) {
      state.visits += p + 1 - pos;
      return { next: p + 1, closed: true };
    }
    p += 1;
  }
  state.visits += end - pos;
  return { next: end, closed: false };
}

function nextSegmentWidth(cursor: ScanCursor, pos: number, limit: number): number {
  const { text, state } = cursor;
  const lineEnd = state.lineEnd;
  let width = 0;
  let p = pos;
  let depth = 0;
  while (p < lineEnd && width <= limit) {
    state.visits += 1;
    const c = text.at(p);
    if (c === BACKSLASH && p + 1 < lineEnd) {
      width += 2;
      p += 2;
      continue;
    }
    if (c === SQUOTE || c === DQUOTE || c === BACKTICK) {
      const q = skipQuoted(cursor, p + 1, lineEnd, c);
      width += q.next - p;
      p = q.next;
      continue;
    }
    if (depth === 0 && p > pos) {
      if (c === AMP && text.at(p + 1) === AMP) return width;
      if (c === PIPE) return width;
      if (c === SEMICOLON) return width;
      if (c === HASH && (text.at(p - 1) === SPACE || text.at(p - 1) === TAB)) return width;
    }
    if (c === OPEN_PAREN || c === OPEN_BRACE || c === OPEN_BRACKET) depth += 1;
    else if ((c === CLOSE_PAREN || c === CLOSE_BRACE || c === CLOSE_BRACKET) && depth > 0) depth -= 1;
    width += c === TAB ? cursor.options.tabSize : 1;
    p += 1;
  }
  return width;
}

export function stepChain(cursor: ScanCursor, pos: number, end: number): number {
  const { state, text } = cursor;
  if (state.chainState.heredocEnd !== null) {
    state.visits += end - pos;
    return end;
  }

  if (state.chainState.quote !== 0) {
    const res = skipQuoted(cursor, pos, end, state.chainState.quote);
    state.col += res.next - pos;
    state.hasContent = true;
    if (res.closed) state.chainState = { ...state.chainState, quote: 0 };
    return res.next;
  }

  state.visits += 1;
  const c = text.at(pos);
  if (c === SPACE || c === TAB) {
    state.col += c === TAB ? cursor.options.tabSize : 1;
    return pos + 1;
  }

  if (c === BACKSLASH && pos + 1 < end) {
    state.col += 2;
    state.hasContent = true;
    return pos + 2;
  }

  // Comments start at line start or after whitespace, preserving `${var#pat}` and URLs.
  if (c === HASH && (pos === 0 || !state.hasContent || text.at(pos - 1) === SPACE || text.at(pos - 1) === TAB)) {
    state.visits += end - pos;
    state.col += end - pos;
    return end;
  }

  if (c === SQUOTE || c === DQUOTE || c === BACKTICK) {
    const res = skipQuoted(cursor, pos + 1, end, c);
    state.col += res.next - pos;
    state.hasContent = true;
    if (!res.closed) state.chainState = { ...state.chainState, quote: c };
    return res.next;
  }

  // Dockerfile JSON exec arrays (`CMD [...]`, `ENTRYPOINT [...]`, etc.)
  if (state.chainState.inJsonExec) {
    if (c === OPEN_BRACKET) {
      const nextDepth = state.chainState.depth + 1;
      if (nextDepth > PRETTY_MAX_DEPTH) {
        state.failure = "depth";
        return pos + 1;
      }
      const next = cursor.skipSpaces(pos + 1, state.lineEnd);
      if (state.laidOut && next < state.lineEnd && text.at(next) !== CLOSE_BRACKET) {
        state.pending = next;
        state.pendingColumns = cursor.columns(1);
      }
      state.chainState = { ...state.chainState, depth: nextDepth };
      state.col += 1;
      state.hasContent = true;
      return pos + 1;
    }
    if (c === COMMA && state.chainState.depth === 1 && state.laidOut) {
      const next = cursor.skipSpaces(pos + 1, state.lineEnd);
      if (next < state.lineEnd && text.at(next) !== CLOSE_BRACKET) {
        state.pending = next;
        state.pendingColumns = cursor.columns(1);
      }
      state.col += 1;
      state.hasContent = true;
      return pos + 1;
    }
    if (c === CLOSE_BRACKET && state.chainState.depth === 1 && state.laidOut && state.hasContent) {
      cursor.emitBreak(pos, state.leadColumns);
      state.chainState = { ...state.chainState, depth: 0, inJsonExec: false };
      state.col += 1;
      return pos + 1;
    }
  }

  if (c === OPEN_PAREN || c === OPEN_BRACE || c === OPEN_BRACKET) {
    const nextDepth = state.chainState.depth + 1;
    if (nextDepth > PRETTY_MAX_DEPTH) {
      state.failure = "depth";
      return pos + 1;
    }
    state.chainState = { ...state.chainState, depth: nextDepth };
    state.col += 1;
    state.hasContent = true;
    return pos + 1;
  }

  if (c === CLOSE_PAREN || c === CLOSE_BRACE || c === CLOSE_BRACKET) {
    state.chainState = { ...state.chainState, depth: Math.max(0, state.chainState.depth - 1) };
    state.col += 1;
    state.hasContent = true;
    return pos + 1;
  }

  const target = targetColumns(cursor.options.lineWidth);
  const remaining = target - state.col;

  if (state.laidOut && state.chainState.depth === 0) {
    if ((c === AMP && text.at(pos + 1) === AMP) || (c === PIPE && (text.at(pos + 1) === PIPE || text.at(pos + 1) === AMP))) {
      if (state.hasContent && nextSegmentWidth(cursor, pos, remaining) > remaining) {
        cursor.emitBreak(pos, cursor.columns(1));
      }
      state.col += 2;
      state.hasContent = true;
      return pos + 2;
    }
    if (c === PIPE) {
      if (state.hasContent && nextSegmentWidth(cursor, pos, remaining) > remaining) {
        cursor.emitBreak(pos, cursor.columns(1));
      }
      state.col += 1;
      state.hasContent = true;
      return pos + 1;
    }
    if (c === SEMICOLON) {
      const nextChar = text.at(pos + 1);
      if (nextChar === SEMICOLON || nextChar === AMP) {
        const len = nextChar === SEMICOLON && text.at(pos + 2) === AMP ? 3 : 2;
        state.col += len;
        state.hasContent = true;
        return pos + len;
      }
      const next = cursor.skipSpaces(pos + 1, state.lineEnd);
      if (next < state.lineEnd && nextSegmentWidth(cursor, next, remaining) > remaining) {
        state.pending = next;
        state.pendingColumns = cursor.columns(0);
      }
      state.col += 1;
      state.hasContent = true;
      return pos + 1;
    }
  }

  state.col += 1;
  state.hasContent = true;
  return pos + 1;
}
