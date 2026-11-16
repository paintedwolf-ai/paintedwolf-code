import { StringStream, type StreamParser } from "@codemirror/language";
import type { Text } from "@codemirror/state";

/** Column threshold past which CodeMirror's StreamLanguage stops emitting tokens. */
export const STREAM_LINE_LIMIT = 10_000;
const STREAM_WINDOW = 4096;
const LINE_STATE_STRIDE = 64;

const PROTECTED_STREAM_BASES = new Set([
  "string",
  "string-2",
  "docString",
  "character",
  "comment",
  "lineComment",
  "blockComment",
  "docComment",
  "regexp",
]);

function isProtectedStreamToken(token: string | null): boolean {
  if (!token) return false;
  for (const part of token.split(/\s+/)) {
    const base = part.split(".", 1)[0] ?? "";
    if (PROTECTED_STREAM_BASES.has(base)) return true;
  }
  return false;
}

function deepCloneValue(value: unknown, seen = new Map<object, unknown>()): unknown {
  if (Array.isArray(value)) {
    const existing = seen.get(value);
    if (existing) return existing;
    const copy: unknown[] = [];
    seen.set(value, copy);
    for (const item of value) copy.push(deepCloneValue(item, seen));
    return copy;
  }
  if (value && typeof value === "object") {
    const proto = Object.getPrototypeOf(value);
    if (proto === Object.prototype || proto === null) {
      const existing = seen.get(value);
      if (existing) return existing;
      const copy: Record<string, unknown> = {};
      seen.set(value, copy);
      for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
        copy[k] = deepCloneValue(v, seen);
      }
      return copy;
    }
  }
  return value;
}

function cloneParserState(parser: StreamParser<unknown>, state: unknown): unknown {
  if (parser.copyState) return parser.copyState(state);
  return deepCloneValue(state);
}

export interface ProtectedWindow {
  readonly from: number;
  readonly to: number;
  readonly starts: number[];
  readonly ends: number[];
}

/**
 * Lazily tokenizes stream-mode lines from the first >10,000-column line onward
 * in 4,096-character windows, preserving multi-line parser state from earlier lines.
 */
export class StreamLineTokens {
  private firstLongLineNumber = 0;
  private readonly lineStartStates = new Map<number, unknown>();
  private lastAdvancedLine = 0;
  private lastAdvancedState: unknown = null;
  private cachedLineNumber = -1;
  private cachedLineText = "";
  private checkpoints: Array<{ offset: number; state: unknown }> = [];

  constructor(
    private readonly parser: StreamParser<unknown>,
    private readonly tabSize: number,
    private readonly indentUnit: number,
  ) {}

  usesStreamAt(doc: Text, lineNumber: number): boolean {
    if (this.firstLongLineNumber === 0) {
      let n = 1;
      const iter = doc.iterLines();
      while (!iter.next().done) {
        if (iter.value.length > STREAM_LINE_LIMIT) {
          this.firstLongLineNumber = n;
          break;
        }
        n += 1;
      }
      if (this.firstLongLineNumber === 0) this.firstLongLineNumber = -1;
    }
    return this.firstLongLineNumber > 0 && lineNumber >= this.firstLongLineNumber;
  }

  private advanceLine(text: string, state: unknown): void {
    const stream = new StringStream(text, this.tabSize, this.indentUnit);
    if (stream.eol()) {
      this.parser.blankLine?.(state, this.indentUnit);
      return;
    }
    while (!stream.eol()) {
      stream.start = stream.pos;
      const before = stream.pos;
      this.parser.token(stream, state);
      if (stream.pos === before) stream.next();
    }
  }

  private startStateForLine(doc: Text, targetLine: number): unknown {
    if (targetLine <= 1) {
      return this.parser.startState ? this.parser.startState(this.indentUnit) : {};
    }
    if (this.lastAdvancedLine === targetLine) {
      return cloneParserState(this.parser, this.lastAdvancedState);
    }
    let line = 1;
    let state: unknown;
    if (this.lastAdvancedLine > 0 && this.lastAdvancedLine < targetLine) {
      line = this.lastAdvancedLine;
      state = cloneParserState(this.parser, this.lastAdvancedState);
    } else {
      let anchor = Math.floor((targetLine - 1) / LINE_STATE_STRIDE) * LINE_STATE_STRIDE + 1;
      while (anchor > 1 && !this.lineStartStates.has(anchor)) anchor -= LINE_STATE_STRIDE;
      if (this.lineStartStates.has(anchor)) {
        line = anchor;
        state = cloneParserState(this.parser, this.lineStartStates.get(anchor));
      } else {
        state = this.parser.startState ? this.parser.startState(this.indentUnit) : {};
        this.lineStartStates.set(1, cloneParserState(this.parser, state));
      }
    }
    while (line < targetLine) {
      if ((line - 1) % LINE_STATE_STRIDE === 0 && !this.lineStartStates.has(line)) {
        this.lineStartStates.set(line, cloneParserState(this.parser, state));
      }
      this.advanceLine(doc.line(line).text, state);
      line += 1;
    }
    if ((targetLine - 1) % LINE_STATE_STRIDE === 0 && !this.lineStartStates.has(targetLine)) {
      this.lineStartStates.set(targetLine, cloneParserState(this.parser, state));
    }
    this.lastAdvancedLine = targetLine;
    this.lastAdvancedState = cloneParserState(this.parser, state);
    return state;
  }

  loadWindow(doc: Text, pos: number): ProtectedWindow {
    const line = doc.lineAt(pos);
    if (this.cachedLineNumber !== line.number || this.cachedLineText !== line.text) {
      const startState = this.startStateForLine(doc, line.number);
      this.cachedLineNumber = line.number;
      this.cachedLineText = line.text;
      this.checkpoints = [{ offset: 0, state: startState }];
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

    const stream = new StringStream(line.text, this.tabSize, this.indentUnit);
    stream.pos = cp.offset;
    stream.start = cp.offset;
    const state = cloneParserState(this.parser, cp.state);

    const starts: number[] = [];
    const ends: number[] = [];
    let nextCheckpoint = Math.floor(cp.offset / STREAM_WINDOW) * STREAM_WINDOW + STREAM_WINDOW;

    if (line.length === 0) {
      this.parser.blankLine?.(state, this.indentUnit);
    } else {
      while (stream.pos < winEnd && !stream.eol()) {
        stream.start = stream.pos;
        const before = stream.pos;
        const tok = this.parser.token(stream, state);
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
    }

    if (stream.eol()) {
      this.lastAdvancedLine = line.number + 1;
      this.lastAdvancedState = cloneParserState(this.parser, state);
    }

    return {
      from: line.from + winStart,
      to: line.from + Math.max(winEnd, 1),
      starts,
      ends,
    };
  }
}
