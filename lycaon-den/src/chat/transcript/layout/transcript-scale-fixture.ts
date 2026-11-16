import type { Message, SessionTranscriptPage } from "../../../api/types.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  emptyTranscriptWindow,
  installTailWindow,
  loadOlderWindow,
  residentPageOrds,
  type TranscriptWindow,
} from "./transcript-window.ts";

export const SWEEP_FIT_SESSION_LENGTH = 305;
export const SWEEP_EVICT_SESSION_LENGTH = 1_200;

function msg(id: string, ord: number): Message {
  const user = ord % 2 === 0;
  return {
    id,
    role: user ? "user" : "assistant",
    origin: user ? "user" : "model",
    authority: user ? "user" : "none",
    trust_tier: "trusted",
    content: `row ${ord}`,
    ord,
    seq: ord,
    created_at: "2026-01-01T00:00:00Z",
  };
}

export function messagesInRange(start: number, end: number, prefix = "m"): Message[] {
  return Array.from({ length: Math.max(0, end - start + 1) }, (_, index) => {
    const ord = start + index;
    return msg(`${prefix}-${ord}`, ord);
  });
}

// Only the bounded window is allocated, regardless of session length.
export function sparseWindowForSessionLength(n: number): TranscriptWindow {
  const tailStart = Math.max(1, n - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
  const tail = messagesInRange(tailStart, n);
  let tw = installTailWindow(
    emptyTranscriptWindow(),
    {
      messages: tail,
      hasMoreBefore: n > DEFAULT_TRANSCRIPT_PAGE_LIMIT,
      hasMoreAfter: false,
    },
    { resetPages: true },
  );
  // Fill the older-page message budget to exercise maximum residency.
  let cursor = tail[0]?.ord;
  while (
    residentPageOrds(tw.pages) < TRANSCRIPT_RESIDENT_ORD_BUDGET &&
    cursor != null &&
    cursor > 1
  ) {
    const end = cursor - 1;
    const start = Math.max(1, end - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
    const rows = messagesInRange(start, end);
    if (rows.length === 0) break;
    tw = loadOlderWindow(tw, {
      messages: rows,
      hasMoreBefore: start > 1,
      hasMoreAfter: true,
    });
    cursor = rows[0]?.ord;
  }
  return tw;
}

/** Deterministic byte-bounded page length in [17, 53]. */
export function byteBoundedPageLength(seed: number, cursorOrd: number): number {
  const h = Math.imul((seed ^ cursorOrd) + 0x9e3779b9, 0x85ebca6b) >>> 0;
  return 17 + (h % 37);
}

export type ByteBoundedTranscriptFixture = {
  totalMessages: number;
  messages: readonly Message[];
  tail: Message[];
  fetchPage: (query?: {
    beforeMessageId?: string;
    afterMessageId?: string;
    afterOrd?: number;
    from?: "oldest";
  }) => SessionTranscriptPage;
  projectItems: (rows: readonly Message[]) => DisplayTranscriptItem[];
  measuredRowHeight: (item: DisplayTranscriptItem) => number;
};

/**
 * Builds a tool-heavy session fixture whose pages return 17..53 messages and whose
 * consecutive tool messages fold into compact activity rows (~20..33 px/message) mixed
 * with prose rows (80..200 px).
 */
export function createByteBoundedTranscriptSession(
  totalMessages: number,
  seed = 7,
): ByteBoundedTranscriptFixture {
  const all: Message[] = [];
  for (let ord = 1; ord <= totalMessages; ord += 1) {
    // Every 7th message is prose; the rest are tool-activity messages that fold in runs of 3.
    const isProse = ord % 7 === 1;
    const role = isProse ? (ord % 14 === 1 ? "user" : "assistant") : "tool";
    all.push({
      id: `m-${ord}`,
      role,
      origin: role === "user" ? "user" : role === "assistant" ? "model" : "tool",
      authority: role === "user" ? "user" : "none",
      trust_tier: "trusted",
      content: isProse ? `prose ${ord}` : `tool output ${ord}`,
      ord,
      seq: ord,
      created_at: "2026-01-01T00:00:00Z",
    });
  }

  const tailCount = Math.min(totalMessages, 51);
  const tail = all.slice(totalMessages - tailCount);
  const ordOfId = (id: string | undefined): number | undefined => {
    if (!id?.startsWith("m-")) return undefined;
    const parsed = Number(id.slice(2));
    return Number.isFinite(parsed) ? parsed : undefined;
  };

  const fetchPage: ByteBoundedTranscriptFixture["fetchPage"] = (query = {}) => {
    let startOrd = 1;
    let endOrd = totalMessages;
    if (query.beforeMessageId) {
      const beforeOrd = ordOfId(query.beforeMessageId) ?? 1;
      endOrd = Math.max(0, beforeOrd - 1);
      const len = byteBoundedPageLength(seed, beforeOrd);
      startOrd = Math.max(1, endOrd - len + 1);
    } else if (query.afterMessageId) {
      const afterOrd = ordOfId(query.afterMessageId) ?? totalMessages;
      startOrd = Math.min(totalMessages + 1, afterOrd + 1);
      const len = byteBoundedPageLength(seed, afterOrd);
      endOrd = Math.min(totalMessages, startOrd + len - 1);
    } else if (typeof query.afterOrd === "number") {
      startOrd = Math.max(1, query.afterOrd + 1);
      const len = byteBoundedPageLength(seed, startOrd);
      endOrd = Math.min(totalMessages, startOrd + len - 1);
    } else if (query.from === "oldest") {
      startOrd = 1;
      const len = byteBoundedPageLength(seed, 1);
      endOrd = Math.min(totalMessages, len);
    }
    const slice = startOrd <= endOrd ? all.slice(startOrd - 1, endOrd) : [];
    const first = slice[0]?.ord ?? startOrd;
    const last = slice[slice.length - 1]?.ord ?? endOrd;
    return {
      messages: slice,
      ...(first > 1 ? { before_cursor: `before-${first}` } : {}),
      ...(last < totalMessages ? { after_cursor: `after-${last}` } : {}),
      ord_bounds: { first_ord: 1, last_ord: totalMessages },
      watermark: totalMessages,
      turn_clocks: {},
      turn_loads: {},
    };
  };

  const heightsByKey = new Map<string, number>();
  const projectItems = (rows: readonly Message[]): DisplayTranscriptItem[] => {
    const items: DisplayTranscriptItem[] = [];
    let i = 0;
    while (i < rows.length) {
      const current = rows[i]!;
      if (current.role !== "tool") {
        const ord = current.ord ?? i + 1;
        const proseHeight = 80 + ((ord * 37) % 121);
        heightsByKey.set(current.id, proseHeight);
        items.push({
          kind: current.role === "user" ? "user" : "assistant",
          key: current.id,
          text: current.content,
        });
        i += 1;
        continue;
      }
      // Fold consecutive tool messages in the same turn triad, keyed by the newest message in the span.
      let run = 1;
      const baseGroup = Math.floor((current.ord ?? 1) / 3);
      while (
        run < 3 &&
        i + run < rows.length &&
        rows[i + run]?.role === "tool" &&
        Math.floor((rows[i + run]?.ord ?? 0) / 3) === baseGroup &&
        (rows[i + run]?.ord ?? 0) === (rows[i + run - 1]?.ord ?? 0) + 1
      ) {
        run += 1;
      }
      const spanRows = rows.slice(i, i + run);
      const key = spanRows[spanRows.length - 1]!.id;
      const height = run * (22 + ((current.ord ?? 1) % 7));
      heightsByKey.set(key, height);
      items.push({
        kind: "activity_span",
        key,
        label: `activity (${run})`,
        entries: spanRows.map((row) => ({
          kind: "index_warming" as const,
          key: row.id,
          meta: { trigger: "turn" },
        })),
      });
      i += run;
    }
    return items;
  };

  const measuredRowHeight = (item: DisplayTranscriptItem): number =>
    heightsByKey.get(item.key) ?? 66;

  return {
    totalMessages,
    messages: all,
    tail,
    fetchPage,
    projectItems,
    measuredRowHeight,
  };
}
