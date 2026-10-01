import type { Message, SessionTranscriptPage } from "../../../api/types.ts";

/** One fetched window: its rows and whether more history lies on each side. */
export type TranscriptWindowPage = {
  messages: Message[];
  hasMoreBefore: boolean;
  hasMoreAfter: boolean;
};

/** A host page's rows; a cursor on a side means more history lies there. */
export function transcriptWindowPage(
  page: Pick<SessionTranscriptPage, "messages" | "before_cursor" | "after_cursor">,
): TranscriptWindowPage {
  return {
    messages: page.messages,
    hasMoreBefore: Boolean(page.before_cursor),
    hasMoreAfter: Boolean(page.after_cursor),
  };
}

/** Matches the host page size, `DefaultTranscriptPageLimit`. */
export const DEFAULT_TRANSCRIPT_PAGE_LIMIT = 100;

/** Maximum older pages retained beside the live tail. */
export const TRANSCRIPT_PAGE_BUDGET = 5;

/** Tail overflow becomes older pages while remaining available to scrollback. */
export const TRANSCRIPT_TAIL_BUDGET = DEFAULT_TRANSCRIPT_PAGE_LIMIT * 2;

/** Newest tail rows included in app-state persistence. */
export const TRANSCRIPT_PERSIST_TAIL_LIMIT = DEFAULT_TRANSCRIPT_PAGE_LIMIT;

export type TranscriptWindow = {
  /** Live SSE append target. */
  tail: Message[];
  /** Older loaded windows keyed by `${oldestOrd}:${newestOrd}`. */
  pages: Record<string, Message[]>;
  hasTailGap: boolean;
  hasMoreBefore: boolean;
  hasMoreAfter: boolean;
};

export function emptyTranscriptWindow(): TranscriptWindow {
  return {
    tail: [],
    pages: {},
    hasTailGap: false,
    hasMoreBefore: false,
    hasMoreAfter: false,
  };
}

/** A window whose row arrays can be patched without touching the source's. */
export function copyTranscriptWindow(tw: TranscriptWindow): TranscriptWindow {
  return {
    ...tw,
    tail: tw.tail.slice(),
    pages: Object.fromEntries(Object.entries(tw.pages).map(([key, rows]) => [key, rows.slice()])),
  };
}

export function pageKey(messages: readonly Message[]): string | undefined {
  if (messages.length === 0) return undefined;
  const oldest = messages[0]?.ord ?? 0;
  const newest = messages[messages.length - 1]?.ord ?? 0;
  return `${oldest}:${newest}`;
}

/** Merge resident rows by Ord and deduplicate by id. */
export function materializeTranscriptWindow(tw: TranscriptWindow): Message[] {
  const byId = new Map<string, Message>();
  const ordered: Message[] = [];
  const pushAll = (rows: readonly Message[]) => {
    for (const row of rows) {
      const prev = byId.get(row.id);
      if (prev && (prev.seq ?? 0) >= (row.seq ?? 0)) continue;
      if (prev) {
        const idx = ordered.findIndex((m) => m.id === row.id);
        if (idx >= 0) ordered[idx] = row;
      } else {
        ordered.push(row);
      }
      byId.set(row.id, row);
    }
  };
  const pageEntries = Object.entries(tw.pages).sort((a, b) => {
    const aOrd = a[1][0]?.ord ?? 0;
    const bOrd = b[1][0]?.ord ?? 0;
    return aOrd - bOrd;
  });
  for (const [, rows] of pageEntries) pushAll(rows);
  pushAll(tw.tail);
  ordered.sort((a, b) => (a.ord ?? 0) - (b.ord ?? 0));
  return ordered;
}

/** Oldest resident row across the tail and older pages. */
export function oldestLoadedMessage(tw: TranscriptWindow): Message | undefined {
  let oldest: Message | undefined;
  const consider = (rows: readonly Message[]) => {
    const first = rows[0];
    if (first?.ord == null) return;
    if (oldest?.ord == null || first.ord < oldest.ord) oldest = first;
  };
  consider(tw.tail);
  for (const rows of Object.values(tw.pages)) consider(rows);
  return oldest;
}

/** Newest Ord in the live tail. */
export function newestTailOrd(tw: TranscriptWindow): number | undefined {
  return tw.tail[tw.tail.length - 1]?.ord;
}

/** Newest row among older pages: the row just before a tail gap. */
export function newestPageMessage(tw: TranscriptWindow): Message | undefined {
  let newest: Message | undefined;
  for (const rows of Object.values(tw.pages)) {
    const last = rows[rows.length - 1];
    if (last?.ord != null && (newest?.ord == null || last.ord > newest.ord)) newest = last;
  }
  return newest;
}

/** Rows that read contiguously from the oldest resident row; a tail gap ends them before the tail. */
export function contiguousWindowRows(tw: TranscriptWindow): Message[] {
  return materializeTranscriptWindow(tw.hasTailGap ? { ...tw, tail: [] } : tw);
}

/** True when a nonresident row belongs before the live tail. */
export function precedesLiveTail(tw: TranscriptWindow, message: Message): boolean {
  const ord = message.ord;
  if (ord == null) return false;
  const newest = newestTailOrd(tw);
  if (newest == null) return false;
  return ord <= newest;
}

/** Drops older pages and resumes reading from the live tail. */
export function resetOlderPages(tw: TranscriptWindow): TranscriptWindow {
  if (Object.keys(tw.pages).length === 0 && !tw.hasTailGap) return tw;
  return { ...tw, pages: {}, hasTailGap: false, hasMoreBefore: true };
}

/** Install a page as the live tail. */
export function installTailWindow(
  tw: TranscriptWindow,
  page: TranscriptWindowPage,
  opts: { resetPages: boolean },
): TranscriptWindow {
  const previousTailOldest = tw.tail[0]?.ord;
  const next: TranscriptWindow = {
    tail: page.messages.slice(),
    pages: opts.resetPages ? {} : { ...tw.pages },
    hasTailGap: opts.resetPages ? false : tw.hasTailGap,
    hasMoreBefore: page.hasMoreBefore,
    hasMoreAfter: page.hasMoreAfter,
  };
  if (!opts.resetPages) {
    // Remove pages that overlap the new tail.
    const tailOldest = next.tail[0]?.ord;
    if (tailOldest != null) {
      for (const [key, rows] of Object.entries(next.pages)) {
        const newest = rows[rows.length - 1]?.ord;
        if (newest != null && newest >= tailOldest) {
          delete next.pages[key];
        }
      }
      if (
        previousTailOldest != null &&
        tailOldest > previousTailOldest &&
        Object.keys(next.pages).length > 0
      ) {
        next.hasTailGap = true;
      }
    }
  }
  return next;
}

/** Merge rows newer than the fetched tail watermark. */
export function mergeIntoTail(
  tw: TranscriptWindow,
  page: TranscriptWindowPage,
  watermark: number,
  localRows: readonly Message[],
): TranscriptWindow {
  const next = installTailWindow(tw, page, { resetPages: false });
  const indexById = new Map(next.tail.map((m, i) => [m.id, i] as const));
  for (const row of localRows) {
    if ((row.seq ?? 0) <= watermark) continue;
    const idx = indexById.get(row.id);
    if (idx == null) {
      // Keep rows that arrived after the fetched snapshot.
      next.tail.push(row);
      indexById.set(row.id, next.tail.length - 1);
    } else if ((next.tail[idx]?.seq ?? 0) < (row.seq ?? 0)) {
      next.tail[idx] = row;
    }
  }
  next.tail.sort((a, b) => (a.ord ?? 0) - (b.ord ?? 0));
  next.hasMoreBefore = page.hasMoreBefore;
  next.hasMoreAfter = page.hasMoreAfter;
  return next;
}

/**
 * Refresh the live tail from a durable tail page. A page that overlaps the resident tail
 * merges into it, newest revision winning; a page past the resident tail cannot be
 * bridged and replaces the window. An empty page leaves the window unchanged.
 */
export function refreshTailWindow(
  tw: TranscriptWindow,
  page: TranscriptWindowPage,
): TranscriptWindow {
  const oldest = page.messages[0]?.ord;
  if (oldest == null) return tw;
  const newest = newestTailOrd(tw);
  if (newest == null || newest < oldest) {
    return installTailWindow(tw, page, { resetPages: true });
  }
  const byId = new Map<string, Message>();
  for (const row of [...tw.tail, ...page.messages]) {
    const prior = byId.get(row.id);
    if (!prior || (prior.seq ?? 0) <= (row.seq ?? 0)) byId.set(row.id, row);
  }
  const tail = [...byId.values()].sort((a, b) => (a.ord ?? 0) - (b.ord ?? 0));
  const extendsOlder = oldest < (tw.tail[0]?.ord ?? oldest);
  const installed = installTailWindow(
    tw,
    {
      messages: tail,
      hasMoreBefore: extendsOlder && Object.keys(tw.pages).length === 0
        ? page.hasMoreBefore
        : tw.hasMoreBefore,
      hasMoreAfter: page.hasMoreAfter,
    },
    { resetPages: false },
  );
  return rollTailOverflowIntoPages(installed);
}

/** Find the older page nearest the live tail. */
function tailAdjacentPageKey(
  pages: Record<string, Message[]>,
): string | undefined {
  let bestKey: string | undefined;
  let bestOrd = -Infinity;
  for (const [key, rows] of Object.entries(pages)) {
    const ord = rows[0]?.ord ?? 0;
    if (ord > bestOrd) {
      bestOrd = ord;
      bestKey = key;
    }
  }
  return bestKey;
}

/** Load an older page and preserve one contiguous older range. */
export function loadOlderWindow(
  tw: TranscriptWindow,
  page: TranscriptWindowPage,
): TranscriptWindow {
  const rows = page.messages.slice();
  const key = pageKey(rows);
  if (!key || rows.length === 0) {
    return {
      ...tw,
      hasMoreBefore: page.hasMoreBefore,
      pages: { ...tw.pages },
    };
  }
  const pages = { ...tw.pages, [key]: rows };
  let hasTailGap = tw.hasTailGap;
  // Evict beside the tail to keep retained pages contiguous.
  while (Object.keys(pages).length > TRANSCRIPT_PAGE_BUDGET) {
    const evict = tailAdjacentPageKey(pages);
    if (!evict) break;
    delete pages[evict];
    hasTailGap = true;
  }
  return {
    ...tw,
    pages,
    hasTailGap,
    hasMoreBefore: page.hasMoreBefore,
  };
}

export function appendToTail(tw: TranscriptWindow, message: Message): TranscriptWindow {
  const tail = tw.tail.slice();
  const idx = tail.findIndex((m) => m.id === message.id);
  if (idx >= 0) {
    if ((tail[idx]?.seq ?? 0) > (message.seq ?? 0)) return tw;
    tail[idx] = message;
  } else {
    let lo = 0;
    let hi = tail.length;
    const ord = message.ord ?? 0;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if ((tail[mid]?.ord ?? 0) <= ord) lo = mid + 1;
      else hi = mid;
    }
    tail.splice(lo, 0, message);
  }
  return rollTailOverflowIntoPages({
    ...tw,
    tail,
    pages: { ...tw.pages },
  });
}

/** Find the page furthest from the live tail. */
function oldestPageKey(pages: Record<string, Message[]>): string | undefined {
  let bestKey: string | undefined;
  let bestOrd = Infinity;
  for (const [key, rows] of Object.entries(pages)) {
    const ord = rows[0]?.ord ?? 0;
    if (ord < bestOrd) {
      bestOrd = ord;
      bestKey = key;
    }
  }
  return bestKey;
}

/**
 * Tail overflow evicts the oldest page; evicted history remains fetchable. Across a tail
 * gap the overflow joins the gap instead, so live rows never evict history being read.
 */
function rollTailOverflowIntoPages(tw: TranscriptWindow): TranscriptWindow {
  if (tw.tail.length <= TRANSCRIPT_TAIL_BUDGET) return tw;
  const keep = TRANSCRIPT_TAIL_BUDGET - DEFAULT_TRANSCRIPT_PAGE_LIMIT;
  const overflowCount = tw.tail.length - keep;
  const overflow = tw.tail.slice(0, overflowCount);
  const rolledKey = pageKey(overflow);
  if (!rolledKey || tw.hasTailGap) return { ...tw, tail: tw.tail.slice(overflowCount) };
  const pages = { ...tw.pages, [rolledKey]: overflow };
  let hasMoreBefore = tw.hasMoreBefore;
  while (Object.keys(pages).length > TRANSCRIPT_PAGE_BUDGET) {
    const evict = oldestPageKey(pages);
    if (!evict) break;
    delete pages[evict];
    hasMoreBefore = true;
  }
  return { ...tw, tail: tw.tail.slice(overflowCount), pages, hasMoreBefore };
}

/** Which end of the resident range a page extends. */
export type TranscriptPageEdge = "older" | "newer" | "start";

/** Page fetch parameters: the resident message a page extends from, or the transcript start. */
export type TranscriptPageRequest =
  | { edge: "older"; beforeMessageId: string }
  | { edge: "newer"; afterMessageId: string }
  | { edge: "start" };

export function transcriptPageRequest(
  tw: TranscriptWindow,
  edge: TranscriptPageEdge,
): TranscriptPageRequest | undefined {
  switch (edge) {
    case "older": {
      if (!tw.hasMoreBefore) return undefined;
      const beforeMessageId = oldestLoadedMessage(tw)?.id;
      return beforeMessageId ? { edge, beforeMessageId } : undefined;
    }
    case "newer": {
      if (!tw.hasTailGap) return undefined;
      const afterMessageId = newestPageMessage(tw)?.id;
      return afterMessageId ? { edge, afterMessageId } : undefined;
    }
    case "start":
      return tw.hasMoreBefore ? { edge } : undefined;
  }
}

/** Query parameters that fetch the page a request names. */
export function transcriptPageQuery(
  request: TranscriptPageRequest,
): { beforeMessageId?: string; afterMessageId?: string; from?: "oldest" } {
  switch (request.edge) {
    case "older": return { beforeMessageId: request.beforeMessageId };
    case "newer": return { afterMessageId: request.afterMessageId };
    case "start": return { from: "oldest" };
  }
}

/** Stable identity of a request, for detecting a resident edge that did not move. */
export function transcriptPageRequestKey(request: TranscriptPageRequest): string {
  switch (request.edge) {
    case "older": return `older:${request.beforeMessageId}`;
    case "newer": return `newer:${request.afterMessageId}`;
    case "start": return "start";
  }
}

/** Load the page after the newest older page; reaching the tail closes the gap. */
function loadNewerWindow(tw: TranscriptWindow, page: TranscriptWindowPage): TranscriptWindow {
  const tailOldest = tw.tail[0]?.ord;
  const rows = tailOldest == null
    ? page.messages.slice()
    : page.messages.filter((row) => (row.ord ?? 0) < tailOldest);
  const reachedTail = !page.hasMoreAfter || rows.length < page.messages.length;
  const pages = { ...tw.pages };
  const key = pageKey(rows);
  if (key) pages[key] = rows;
  let hasMoreBefore = tw.hasMoreBefore;
  // Evict away from the reader, who is reading toward the tail.
  while (Object.keys(pages).length > TRANSCRIPT_PAGE_BUDGET) {
    const evict = oldestPageKey(pages);
    if (!evict) break;
    delete pages[evict];
    hasMoreBefore = true;
  }
  return {
    ...tw,
    pages,
    hasMoreBefore,
    hasTailGap: tw.hasTailGap && !reachedTail,
  };
}

/** Applies a fetched page if the resident window matches the request boundary; returns undefined if stale. */
export function applyTranscriptPage(
  tw: TranscriptWindow,
  request: TranscriptPageRequest,
  page: TranscriptWindowPage,
): TranscriptWindow | undefined {
  const current = transcriptPageRequest(tw, request.edge);
  if (!current) return undefined;
  switch (request.edge) {
    case "older":
      return current.edge === "older" && current.beforeMessageId === request.beforeMessageId
        ? loadOlderWindow(tw, page)
        : undefined;
    case "newer":
      return current.edge === "newer" && current.afterMessageId === request.afterMessageId
        ? loadNewerWindow(tw, page)
        : undefined;
    case "start":
      return loadNewerWindow(
        { ...tw, pages: {}, hasMoreBefore: false, hasTailGap: true },
        page,
      );
  }
}

/** Patch a row in whichever loaded window holds it; no-op if not resident. */
export function patchRowInWindow(tw: TranscriptWindow, message: Message): boolean {
  const tryPatch = (rows: Message[]): boolean => {
    const idx = rows.findIndex((m) => m.id === message.id);
    if (idx < 0) return false;
    if ((rows[idx]?.seq ?? 0) > (message.seq ?? 0)) return true;
    rows[idx] = message;
    return true;
  };
  if (tryPatch(tw.tail)) return true;
  for (const key of Object.keys(tw.pages)) {
    const rows = tw.pages[key];
    if (!rows) continue;
    if (tryPatch(rows)) return true;
  }
  return false;
}
