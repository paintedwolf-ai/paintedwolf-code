import type { StreamParser } from "@codemirror/language";
import type { Text } from "@codemirror/state";

type StreamArg = Parameters<StreamParser<unknown>["token"]>[0];

/** Column threshold past which CodeMirror's StreamLanguage stops emitting tokens. */
export const STREAM_LINE_LIMIT = 10_000;
const STREAM_WINDOW = 4096;

/** Minimal StringStream implementation for tokenizing lines past 10,000 columns. */
class LineStream {
  pos = 0;
  start = 0;
  lastColumnPos = 0;
  lastColumnValue = 0;
  lineStart = 0;

  constructor(
    readonly string: string,
    readonly tabSize: number,
  ) {}

  eol(): boolean {
    return this.pos >= this.string.length;
  }

  sol(): boolean {
    return this.pos === this.lineStart;
  }

  peek(): string | undefined {
    return this.string.charAt(this.pos) || undefined;
  }

  next(): string | undefined {
    if (this.pos < this.string.length) {
      return this.string.charAt(this.pos++);
    }
    return undefined;
  }

  eat(match: string | RegExp | ((ch: string) => boolean)): string | undefined {
    const ch = this.string.charAt(this.pos);
    if (!ch) return undefined;
    const ok =
      typeof match === "string"
        ? ch === match
        : match instanceof RegExp
          ? match.test(ch)
          : match(ch);
    if (ok) {
      this.pos++;
      return ch;
    }
    return undefined;
  }

  eatWhile(match: string | RegExp | ((ch: string) => boolean)): boolean {
    const start = this.pos;
    while (this.eat(match)) {
      // advance
    }
    return this.pos > start;
  }

  eatSpace(): boolean {
    const start = this.pos;
    while (/[\s\u00a0]/.test(this.string.charAt(this.pos))) {
      this.pos++;
    }
    return this.pos > start;
  }

  skipToEnd(): void {
    this.pos = this.string.length;
  }

  skipTo(ch: string): boolean | undefined {
    const found = this.string.indexOf(ch, this.pos);
    if (found > -1) {
      this.pos = found;
      return true;
    }
    return undefined;
  }

  backUp(n: number): void {
    this.pos -= n;
  }

  column(): number {
    if (this.lastColumnPos < this.start) {
      this.lastColumnValue = countColumn(this.string, this.lastColumnPos, this.start, this.tabSize, this.lastColumnValue);
      this.lastColumnPos = this.start;
    }
    return this.lastColumnValue - (this.lineStart ? countColumn(this.string, 0, this.lineStart, this.tabSize) : 0);
  }

  indentation(): number {
    return (
      countColumn(this.string, this.lineStart, this.string.length, this.tabSize) -
      (this.lineStart ? countColumn(this.string, 0, this.lineStart, this.tabSize) : 0)
    );
  }

  match(pattern: string | RegExp, consume?: boolean, caseInsensitive?: boolean): boolean | RegExpMatchArray | null {
    if (typeof pattern === "string") {
      const sub = this.string.slice(this.pos, this.pos + pattern.length);
      const matched = caseInsensitive ? sub.toLowerCase() === pattern.toLowerCase() : sub === pattern;
      if (!matched) return false;
      if (consume !== false) this.pos += pattern.length;
      return true;
    }
    const flags = pattern.flags.includes("y") ? pattern.flags : `${pattern.flags}y`;
    const re = new RegExp(pattern.source, flags);
    re.lastIndex = this.pos;
    const match = re.exec(this.string);
    if (match && consume !== false) {
      this.pos += match[0].length;
    }
    return match;
  }

  current(): string {
    return this.string.slice(this.start, this.pos);
  }
}

function countColumn(str: string, from: number, to: number, tabSize: number, startValue = 0): number {
  let col = startValue;
  for (let i = from; i < to; i++) {
    const ch = str.charCodeAt(i);
    if (ch === 9) col += tabSize - (col % tabSize);
    else col++;
  }
  return col;
}

function isProtectedStreamToken(token: string | null): boolean {
  if (!token) return false;
  for (const part of token.split(/\s+/)) {
    if (
      part === "string" ||
      part === "string-2" ||
      part === "string.special" ||
      part === "comment" ||
      part === "regexp" ||
      part === "character" ||
      part === "quote"
    ) {
      return true;
    }
  }
  return false;
}

function cloneParserState(parser: StreamParser<unknown>, state: unknown): unknown {
  if (parser.copyState) return parser.copyState(state);
  if (state && typeof state === "object") return { ...(state as Record<string, unknown>) };
  return state;
}

export interface ProtectedWindow {
  readonly from: number;
  readonly to: number;
  readonly starts: number[];
  readonly ends: number[];
}

/**
 * Lazily tokenizes lines longer than 10,000 columns in 4,096-character windows
 * so StreamParser-backed languages keep protecting strings and comments.
 */
export class StreamLineTokens {
  private cachedLineNumber = -1;
  private cachedLineText = "";
  private checkpoints: Array<{ offset: number; state: unknown }> = [];

  constructor(
    private readonly parser: StreamParser<unknown>,
    private readonly indentUnit: number,
  ) {}

  loadWindow(doc: Text, pos: number): ProtectedWindow {
    const line = doc.lineAt(pos);
    if (this.cachedLineNumber !== line.number || this.cachedLineText !== line.text) {
      this.cachedLineNumber = line.number;
      this.cachedLineText = line.text;
      this.checkpoints = [{ offset: 0, state: this.parser.startState ? this.parser.startState(this.indentUnit) : {} }];
    }

    const relPos = pos - line.from;
    const winStart = Math.floor(relPos / STREAM_WINDOW) * STREAM_WINDOW;
    const winEnd = Math.min(line.length, winStart + STREAM_WINDOW);

    let cp = this.checkpoints[0]!;
    for (let i = this.checkpoints.length - 1; i >= 0; i--) {
      if (this.checkpoints[i]!.offset <= winStart) {
        cp = this.checkpoints[i]!;
        break;
      }
    }

    const stream = new LineStream(line.text, this.indentUnit);
    stream.pos = cp.offset;
    stream.start = cp.offset;
    let state = cloneParserState(this.parser, cp.state);

    const starts: number[] = [];
    const ends: number[] = [];
    let nextCheckpoint = Math.floor(cp.offset / STREAM_WINDOW) * STREAM_WINDOW + STREAM_WINDOW;

    while (stream.pos < winEnd && !stream.eol()) {
      stream.start = stream.pos;
      const before = stream.pos;
      const tok = this.parser.token(stream as unknown as StreamArg, state);
      if (stream.pos === before) stream.next();

      if (stream.pos > winStart && isProtectedStreamToken(tok)) {
        const from = line.from + Math.max(winStart, stream.start);
        const to = line.from + Math.min(winEnd, stream.pos);
        if (to > from) {
          const last = starts.length - 1;
          if (last >= 0 && ends[last] === from) ends[last] = to;
          else {
            starts.push(from);
            ends.push(to);
          }
        }
      }

      if (stream.pos >= nextCheckpoint && stream.pos < line.length) {
        const lastCp = this.checkpoints[this.checkpoints.length - 1]!;
        if (stream.pos > lastCp.offset) {
          this.checkpoints.push({
            offset: stream.pos,
            state: cloneParserState(this.parser, state),
          });
        }
        nextCheckpoint = Math.floor(stream.pos / STREAM_WINDOW) * STREAM_WINDOW + STREAM_WINDOW;
      }
    }

    return {
      from: line.from + winStart,
      to: line.from + winEnd,
      starts,
      ends,
    };
  }
}
