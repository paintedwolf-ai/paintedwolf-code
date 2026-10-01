import type { SourceComparisonDigest } from "../../api/types.ts";
import { readerGap, type ReaderSlot } from "../../components/source/reader/source-reader-document.ts";
import { wholeFileChange, type SourceReaderChange } from "../../components/source/reader/source-reader-change.ts";
import { digestIsBinary, digestIsNoop, digestStat } from "./diffs-digests.ts";

/** A page header reserves this much before its block renders. */
export const PAGE_HEADER_RESERVE_PX = 148;
/** Reserved vertical space for a section header block prior to layout. */
export const SECTION_HEADER_RESERVE_PX = 70;
/** The page's own heading is a section with no comparison behind it. */
export const PAGE_SECTION = "\u0000page";

/** One file's place in the page: what it is, and how much of it is loaded. */
export type DiffsSectionState = {
  key: string;
  /** Measured by the host before the section opens; absent while unmeasured. */
  digest: SourceComparisonDigest | undefined;
  /** What the comparison did to the file; absent until the source answers. */
  change?: SourceReaderChange;
  open: boolean;
  /** Rows the section has loaded; empty until it comes near the viewport. */
  window: readonly ReaderSlot[];
  /** Reserved slot reused while size is unchanged. */
  placeholder?: ReaderSlot;
  /** null reads the net comparison; an index reads one recorded write. */
  revision?: number | null;
  /** Latches previous window during an in-flight revision transition. */
  reloading?: boolean;
};

/** Title-only sections have no diff rows to expand; whole-file views render summary details in the section header. */
export function sectionIsTitleOnly(
  section: Pick<DiffsSectionState, "digest" | "change" | "revision">,
): boolean {
  if (wholeFileChange(section.change)) return true;
  // The digest measures the net comparison; a chosen revision is read on its own terms.
  return section.revision == null && sectionIsNoop(section.digest);
}

/** Display rows a section occupies once open, matching host measurement to prevent reflow. */
export function sectionRows(digest: SourceComparisonDigest | undefined): number {
  if (!digest?.in_range || digest.changes_rows === undefined) return 1;
  return Math.max(1, digest.changes_rows);
}

function sectionIsNoop(digest: SourceComparisonDigest | undefined): boolean {
  return digestIsNoop(digest) || digestIsBinary(digest) || (digest !== undefined && !digest.in_range);
}

const headerSlots = new Map<string, ReaderSlot>();

function headerSlot(section: string): ReaderSlot {
  let slot = headerSlots.get(section);
  if (!slot) {
    slot = Object.freeze({ ...readerGap(0, 1), section, header: true, pending: false });
    if (headerSlots.size >= 2048) {
      const oldest = headerSlots.keys().next().value;
      if (oldest !== undefined) headerSlots.delete(oldest);
    }
    headerSlots.set(section, slot);
  }
  return slot;
}

/** Sized placeholder slot for an unread section. */
export function sectionPlaceholder(
  section: string,
  digest: SourceComparisonDigest | undefined,
  held?: ReaderSlot,
): ReaderSlot {
  const rows = sectionRows(digest);
  if (held && held.section === section && held.lines === rows) return held;
  return { ...readerGap(0, rows, true, rows), section };
}

/** Assembles document slots: page heading, section headers, and either loaded rows or measured placeholders. */
export function diffsDocumentSlots(sections: readonly DiffsSectionState[]): ReaderSlot[] {
  const slots: ReaderSlot[] = [headerSlot(PAGE_SECTION)];
  for (const section of sections) {
    slots.push(headerSlot(section.key));
    if (!section.open || sectionIsTitleOnly(section)) continue;
    if (section.window.length) {
      if (section.reloading) slots.push(...section.window.map((slot) => (slot.reloading ? slot : { ...slot, reloading: true })));
      else slots.push(...section.window);
    } else {
      slots.push(section.placeholder ?? sectionPlaceholder(section.key, section.digest));
    }
  }
  return slots;
}

/** Sums line additions and removals across all measured sections. */
export function diffsTotal(sections: readonly DiffsSectionState[]): { added: number; removed: number } | null {
  let added = 0, removed = 0;
  for (const section of sections) {
    const stat = digestStat(section.digest);
    if (!stat) return null;
    added += stat.added;
    removed += stat.removed;
  }
  return sections.length ? { added, removed } : null;
}
