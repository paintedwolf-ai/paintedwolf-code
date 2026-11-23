import type { ChatContentPage, ChatContentReference, RedactedSpan } from "../../../api/types.ts";
import { PRETTY_MAX_INPUT } from "../../../components/source/pretty/pretty-rescan.ts";
import type { ChatContentBoundedSnapshot } from "./chat-content-reader.ts";

const PAGE_RUNES = 16_384;
const MAX_IN_FLIGHT = 4;

function validPage(page: ChatContentPage, reference: ChatContentReference, from: number, end: number): boolean {
  if (page?.reference?.sha256 !== reference.sha256 || page.offset !== from ||
      page.end_offset <= from || page.end_offset > end || typeof page.text !== "string" ||
      page.complete !== (page.end_offset === reference.total_runes)) return false;
  const runes = page.end_offset - from;
  return Array.from(page.text).length === runes && Array.isArray(page.spans) &&
    page.spans.every(span => Number.isSafeInteger(span?.start) && Number.isSafeInteger(span.length) &&
      span.start >= 0 && span.length >= 0 && span.start + span.length <= runes);
}

/** Reads disjoint rune windows and publishes only a complete, ordered revision. */
export async function readRetainedSnapshot(
  reference: ChatContentReference,
  read: (offset: number, limit: number, signal: AbortSignal) => Promise<ChatContentPage>,
  signal: AbortSignal,
): Promise<ChatContentBoundedSnapshot | null> {
  signal.throwIfAborted();
  const cancel = new AbortController();
  const interest = AbortSignal.any([signal, cancel.signal]);
  const windows = Math.ceil(reference.total_runes / PAGE_RUNES);
  const parts: string[] = [];
  const spans: RedactedSpan[][] = [];
  const invalid = new Error("Invalid retained content page.");
  let next = 0;
  let units = 0;
  async function worker(): Promise<void> {
    while (next < windows) {
      const window = next++;
      const end = Math.min(reference.total_runes, (window + 1) * PAGE_RUNES);
      const text: string[] = [];
      const marks: RedactedSpan[] = [];
      for (let offset = window * PAGE_RUNES; offset < end;) {
        interest.throwIfAborted();
        const page = await read(offset, end - offset, interest);
        interest.throwIfAborted();
        if (!validPage(page, reference, offset, end)) throw invalid;
        units += page.text.length;
        if (units > PRETTY_MAX_INPUT) throw invalid;
        text.push(page.text);
        for (const span of page.spans) marks.push({ ...span, start: offset + span.start });
        offset = page.end_offset;
      }
      parts[window] = text.join("");
      spans[window] = marks;
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(windows, MAX_IN_FLIGHT) }, worker));
    signal.throwIfAborted();
    return { revision: reference.sha256, text: parts.join(""), spans: spans.flat() };
  } catch (cause) {
    cancel.abort();
    if (cause === invalid) return null;
    throw cause;
  }
}
