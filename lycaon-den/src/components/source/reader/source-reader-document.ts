import type { SourceReaderRow } from "../../../api/types.ts";

/** A file reader has one section; a page composes one per comparison. */
export const MAIN_SECTION = "";

export type ReaderSlot = SourceReaderRow & {
  pending?: boolean;
  /** Latched prior comparison rows awaiting replacement by an in-flight read. */
  reloading?: boolean;
  lines?: number;
  displayIndex?: number;
  displayEnd?: number;
  /** The comparison this slot belongs to; absent means the only one. */
  section?: string;
  /** Anchors a section; the surface replaces it with its own block. */
  header?: boolean;
};
export type ReaderSide = "before" | "after";
/**
 * `line` and `lineFrom` are recorded as the text is built: fragments of one
 * long source line share a line, so an entry's position alone does not name it.
 */
export type ReaderEntry = {
  from: number; to: number; line: number; lineFrom: number;
  section: string; slot: ReaderSlot; row?: SourceReaderRow;
};
export type ReaderBoundary = { section: string; row: number; offset: number; side: ReaderSide };

/** Row indices repeat across comparisons, so a row is named with its section. */
function rowKey(section: string, index: number): string {
  return `${section}\u0000${index}`;
}

type SectionSpan = { from: number; to: number; first: number; last: number };

/** Editor text contains loaded source and one placeholder per omitted range. */
export class ReaderDocument {
  readonly text: string;
  readonly entries: ReaderEntry[] = [];
  private readonly byRow = new Map<string, ReaderEntry>();
  private readonly spans = new Map<string, SectionSpan>();

  constructor(readonly slots: readonly ReaderSlot[], readonly side?: ReaderSide) {
    const text: string[] = [];
    let offset = 0;
    let line = 1, lineFrom = 0;
    let previous: SourceReaderRow | undefined;
    let previousSection: string | undefined;
    for (const slot of slots) {
      const section = slot.section ?? MAIN_SECTION;
      const row = slot.kind === "gap" || slot.pending || slot.header ? undefined : side === "after"
        ? slot.peer ?? (slot.after_line ? slot : undefined)
        : side === "before" ? slot.before_line ? slot : undefined : slot;
      // Fragments of one long source line share a line; a new section never does.
      const continues = section === previousSection && row && previous && row.column
        && row.before_line === previous.before_line
        && row.after_line === previous.after_line && row.kind === previous.kind;
      if (text.length && !text[text.length - 1]!.endsWith("\n") && !continues) { text.push("\n"); offset++; line++; lineFrom = offset; }
      const body = slot.kind === "gap" || slot.pending || slot.header ? "￼\n" : row ? row.text : "\n";
      const entry: ReaderEntry = { from: offset, to: offset + body.length, line, lineFrom, section, slot, row };
      this.entries.push(entry);
      if (row) this.byRow.set(rowKey(section, row.index), entry);
      const span = this.spans.get(section);
      if (span) { span.to = entry.to; span.last = this.entries.length - 1; }
      else this.spans.set(section, { from: entry.from, to: entry.to, first: this.entries.length - 1, last: this.entries.length - 1 });
      text.push(body); offset += body.length;
      if (body.endsWith("\n")) { line++; lineFrom = offset; }
      previous = row; previousSection = section;
    }
    this.text = text.join("");
  }

  /** The entry holding one comparison's row, when it is loaded. */
  rowAt(section: string, index: number): ReaderEntry | undefined {
    return this.byRow.get(rowKey(section, index));
  }

  /** Where one comparison sits in the document. */
  sectionSpan(section: string): { from: number; to: number } | undefined {
    const span = this.spans.get(section);
    return span && { from: span.from, to: span.to };
  }

  get sections(): readonly string[] { return [...this.spans.keys()]; }

  at(position: number, association = 1): ReaderEntry | undefined {
    let low = 0, high = this.entries.length;
    while (low < high) {
      const mid = (low + high) >>> 1;
      if (this.entries[mid]!.from < position || association > 0 && this.entries[mid]!.from === position) low = mid + 1;
      else high = mid;
    }
    return this.entries[Math.max(0, low - 1)];
  }

  entryForSlot(section: string, index: number): ReaderEntry | undefined {
    const span = this.spans.get(section);
    if (!span) return undefined;
    let low = span.first, high = span.last + 1;
    while (low < high) {
      const mid = (low + high) >>> 1;
      if (this.entries[mid]!.slot.index < index) low = mid + 1;
      else high = mid;
    }
    const entry = this.entries[low];
    return entry?.slot.index === index && entry.section === section ? entry : undefined;
  }

  *entriesBetween(from: number, to: number): IterableIterator<ReaderEntry> {
    let low = 0, high = this.entries.length;
    while (low < high) {
      const mid = (low + high) >>> 1;
      if (this.entries[mid]!.to < from) low = mid + 1;
      else high = mid;
    }
    for (let index = low; index < this.entries.length && this.entries[index]!.from <= to; index++) yield this.entries[index]!;
  }

  position(boundary: ReaderBoundary, association: number): number {
    const exact = this.rowAt(boundary.section, boundary.row);
    if (exact?.row) return exact.from + Math.min(exact.row.text.length, boundary.offset);
    const span = this.spans.get(boundary.section);
    if (!span) return 0;
    for (let index = span.first; index <= span.last; index++) {
      const entry = this.entries[index]!;
      if (entry.slot.index <= boundary.row && entry.slot.end > boundary.row) {
        return association > 0 ? entry.from : entry.to;
      }
    }
    return boundary.row < (this.entries[span.first]?.slot.index ?? 0) ? span.from : span.to;
  }

  boundary(position: number, association: number): ReaderBoundary | undefined {
    const entry = this.at(position, association);
    if (!entry) return undefined;
    const side = this.side ?? (entry.row?.kind === "delete" ? "before" : "after");
    if (!entry.row) {
      return { section: entry.section, row: association > 0 ? entry.slot.index : entry.slot.end - 1, offset: association > 0 ? 0 : Infinity, side };
    }
    return { section: entry.section, row: entry.row.index, offset: Math.min(entry.row.text.length, Math.max(0, position - entry.from)), side };
  }
}

export function readerGap(index: number, end: number, pending = false, lines = 1): ReaderSlot {
  return { index, end, pending, lines, kind: "gap", text: "", before_line: 0, after_line: 0, changed: [] };
}
