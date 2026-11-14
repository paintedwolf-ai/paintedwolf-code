import { containerStartColumn, DATA_LIKE_RULE, readJsonContainer } from "./pretty-recognition.ts";
import type { Fence, PrettyRule } from "./pretty-scan-state.ts";
import type { TextWindow } from "./pretty-text-window.ts";

export interface OpeningFence {
  readonly char: number;
  readonly length: number;
  readonly info: string;
  readonly quoteDepth: number;
}

function parseQuotePrefix(lineText: string): { quoteDepth: number; rest: string } {
  let pos = 0;
  let quoteDepth = 0;
  while (pos < lineText.length) {
    const match = /^ {0,3}> ?/.exec(lineText.slice(pos));
    if (!match) break;
    quoteDepth += 1;
    pos += match[0].length;
  }
  return { quoteDepth, rest: lineText.slice(pos) };
}

/** A Markdown opening fence (with optional blockquote and up to 12 spaces of list indent) and its info string. */
export function parseOpeningFence(lineText: string): OpeningFence | null {
  const { quoteDepth, rest } = parseQuotePrefix(lineText);
  const match = /^ {0,12}(`{3,}|~{3,})(.*)$/.exec(rest);
  if (!match) return null;
  const fence = match[1]!;
  const info = match[2] ?? "";
  if (fence[0] === "`" && info.includes("`")) return null;
  return {
    char: fence.charCodeAt(0),
    length: fence.length,
    info: (info.trim().split(/\s+/)[0] ?? "").toLowerCase(),
    quoteDepth,
  };
}

export function isClosingFence(lineText: string, fence: Fence): boolean {
  const { quoteDepth, rest } = parseQuotePrefix(lineText);
  if (quoteDepth !== (fence.quoteDepth ?? 0)) return false;
  const match = /^ {0,12}(`{3,}|~{3,})[ \t]*$/.exec(rest);
  return !!match && match[1]!.charCodeAt(0) === fence.char && match[1]!.length >= fence.length;
}

/** Recognizes a JSON/Python-dict container inside an unlabelled fence starting on `from..to`. */
export function unlabelledFenceSpan(
  text: TextWindow,
  from: number,
  to: number,
  fence: Fence,
): { rule: PrettyRule; spanEnd: number } | null {
  const line = text.slice(from, Math.min(to, from + 8192));
  const { quoteDepth, rest } = parseQuotePrefix(line);
  if (quoteDepth !== (fence.quoteDepth ?? 0)) return null;
  const prefixLen = line.length - rest.length;
  const col = containerStartColumn(rest);
  if (col === null) return null;
  const probe = text.slice(from, Math.min(text.length, from + 16_384));
  const end = readJsonContainer(probe, prefixLen + col);
  if (end <= 0) return null;
  return { rule: DATA_LIKE_RULE, spanEnd: from + end };
}
