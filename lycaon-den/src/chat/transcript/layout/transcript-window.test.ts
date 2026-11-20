// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_RESIDENT_ORD_BUDGET,
  TRANSCRIPT_RESIDENT_ORD_HARD_CAP,
  TRANSCRIPT_TAIL_BUDGET,
  appendToTail,
  applyTranscriptPage,
  contiguousWindowRows,
  emptyTranscriptWindow,
  installTailWindow,
  loadOlderWindow,
  materializeTranscriptWindow,
  mergeIntoTail,
  precedesLiveTail,
  presentedOrdSpan,
  refreshTailWindow,
  residentPageOrds,
  transcriptPageQuery,
  transcriptPageRequest,
  transcriptSeekRequest,
  trimOldestPages,
  unloadedTranscriptOrds,
  type TranscriptEvictedPage,
  type TranscriptWindow,
  type TranscriptWindowPage,
} from "./transcript-window.ts";

function msg(id: string, ord: number, seq = ord): Message {
  return {
    id,
    role: "user",
    origin: "user" as const,
    authority: "user" as const,
    trust_tier: "trusted" as const,
    content: id,
    ord,
    seq,
    created_at: "2026-01-01T00:00:00Z",
  };
}

function page(rows: Message[], before = false, after = false): TranscriptWindowPage {
  return { messages: rows, hasMoreBefore: before, hasMoreAfter: after };
}

describe("TranscriptWindow", () => {
  it("installs a tail and materializes only that window", () => {
    const tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("a", 8), msg("b", 9), msg("c", 10)], true, false),
      { resetPages: true },
    );
    expect(tw.hasMoreBefore).toBe(true);
    expect(materializeTranscriptWindow(tw).map((m) => m.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("loads older pages and evicts beside the tail beyond the resident message budget", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("t", 1_000)], true, false),
      { resetPages: true },
    );
    const fullPages = TRANSCRIPT_RESIDENT_ORD_BUDGET / DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    for (let i = 0; i < fullPages + 2; i++) {
      const end = 900 - i * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
      const start = end - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1;
      tw = loadOlderWindow(
        tw,
        page(
          Array.from({ length: DEFAULT_TRANSCRIPT_PAGE_LIMIT }, (_, j) =>
            msg(`p${start + j}`, start + j),
          ),
          start > 1,
          true,
        ),
      );
    }
    expect(residentPageOrds(tw.pages)).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_BUDGET);
    expect(tw.hasTailGap).toBe(true);
    expect(materializeTranscriptWindow(tw).length).toBeLessThanOrEqual(
      TRANSCRIPT_RESIDENT_ORD_BUDGET + 1,
    );
    expect(tw.tail).toHaveLength(1);
  });

  it("records a tail gap only after page eviction", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("tail", 1_000)], true, false),
      { resetPages: true },
    );
    const fullPages = TRANSCRIPT_RESIDENT_ORD_BUDGET / DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    for (let index = 0; index < fullPages; index += 1) {
      const end = 900 - index * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
      const start = end - DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1;
      tw = loadOlderWindow(
        tw,
        page(
          Array.from({ length: DEFAULT_TRANSCRIPT_PAGE_LIMIT }, (_, j) =>
            msg(`p${start + j}`, start + j),
          ),
          true,
        ),
      );
    }
    expect(tw.hasTailGap).toBe(false);

    tw = loadOlderWindow(tw, page([msg("evicting", 100)], true));
    expect(tw.hasTailGap).toBe(true);
  });

  it("records a gap when a refreshed tail advances beyond retained pages", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("tail-50", 50)], true),
      { resetPages: true },
    );
    tw = loadOlderWindow(tw, page([msg("page-40", 40)], true));
    expect(tw.hasTailGap).toBe(false);

    tw = installTailWindow(tw, page([msg("tail-60", 60)], true), {
      resetPages: false,
    });
    expect(tw.hasTailGap).toBe(true);
  });

  it("treats an Ord inside the live tip as a history patch, not an append", () => {
    const tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("a", 10), msg("b", 11)], false, true),
      { resetPages: true },
    );
    expect(precedesLiveTail(tw, msg("ancient", 3, 99))).toBe(true);
    expect(precedesLiveTail(tw, msg("tip", 11, 100))).toBe(true);
    expect(precedesLiveTail(tw, msg("newer", 12, 101))).toBe(false);
  });

  it("keeps the default page limit aligned with the host const", () => {
    expect(DEFAULT_TRANSCRIPT_PAGE_LIMIT).toBe(100);
  });

  it("rolls tail overflow into page-shaped chunks with no row loss", () => {
    let tw = emptyTranscriptWindow();
    const total = TRANSCRIPT_TAIL_BUDGET + 1;
    for (let i = 0; i < total; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i));
    }
    expect(tw.tail.length).toBe(
      TRANSCRIPT_TAIL_BUDGET - DEFAULT_TRANSCRIPT_PAGE_LIMIT,
    );
    const pages = Object.values(tw.pages);
    expect(pages.length).toBe(1);
    expect(pages[0]?.length).toBe(DEFAULT_TRANSCRIPT_PAGE_LIMIT + 1);
    expect(materializeTranscriptWindow(tw).length).toBe(total);
    const ids = materializeTranscriptWindow(tw).map((m) => m.id);
    expect(ids.length).toBe(total);
    expect(ids[0]).toBe("m0");
    expect(ids[total - 1]).toBe(`m${total - 1}`);
    expect(tw.hasMoreBefore).toBe(false);
  });

  it("evicts the oldest rolled page over budget and re-arms hasMoreBefore", () => {
    let tw = emptyTranscriptWindow();
    const fullPages = TRANSCRIPT_RESIDENT_ORD_BUDGET / DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    const rolls = fullPages + 2;
    const total =
      TRANSCRIPT_TAIL_BUDGET + rolls * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    for (let i = 0; i < total; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i));
    }
    expect(residentPageOrds(tw.pages)).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_BUDGET);
    expect(tw.hasMoreBefore).toBe(true);
    // The retained window is the newest contiguous run: no interior holes.
    const ords = materializeTranscriptWindow(tw).map((m) => m.ord ?? 0);
    for (let i = 1; i < ords.length; i++) {
      expect(ords[i]).toBe((ords[i - 1] ?? 0) + 1);
    }
    expect(ords[ords.length - 1]).toBe(total - 1);
  });

});

function rows(from: number, to: number): Message[] {
  return Array.from({ length: to - from + 1 }, (_, i) => msg(`m${from + i}`, from + i));
}

function ids(tw: TranscriptWindow): number[] {
  return materializeTranscriptWindow(tw).map((m) => m.ord ?? 0);
}

describe("TranscriptWindow page requests", () => {
  const tailAt = (from: number, to: number) =>
    installTailWindow(emptyTranscriptWindow(), page(rows(from, to), from > 1), { resetPages: true });

  it("refuses a page whose cursor the window has moved past", () => {
    const tw = tailAt(901, 1000);
    const stale = transcriptPageRequest(tw, "older")!;
    const moved = applyTranscriptPage(tw, stale, page(rows(801, 900), true, true))!;
    expect(applyTranscriptPage(moved, stale, page(rows(801, 900), true, true))).toBeUndefined();
  });

  it("starts from the first page and reads forward until it meets the tail", () => {
    let tw = tailAt(501, 600);
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "start")!, page(rows(1, 100), false, true))!;
    expect(tw.hasMoreBefore).toBe(false);
    expect(tw.hasTailGap).toBe(true);
    // Across the gap the reader sees only the history that reads contiguously.
    expect(contiguousWindowRows(tw).map((m) => m.ord)).toEqual(rows(1, 100).map((m) => m.ord));

    for (let from = 101; from < 501; from += 100) {
      const request = transcriptPageRequest(tw, "newer");
      expect(request).toEqual({ edge: "newer", afterMessageId: `m${from - 1}` });
      tw = applyTranscriptPage(tw, request!, page(rows(from, from + 99), true, true))!;
    }
    // The page before the tail has not yet been read.
    expect(tw.hasTailGap).toBe(true);
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "newer")!, page(rows(501, 600), true, false))!;
    expect(tw.hasTailGap).toBe(false);
    expect(transcriptPageRequest(tw, "newer")).toBeUndefined();
    const ords = contiguousWindowRows(tw).map((m) => m.ord ?? 0);
    expect(ords.at(-1)).toBe(600);
    for (let i = 1; i < ords.length; i++) expect(ords[i]).toBe((ords[i - 1] ?? 0) + 1);
  });

  it("evicts the oldest pages while reading forward and reopens older history", () => {
    let tw = tailAt(1901, 2000);
    const fullPages = TRANSCRIPT_RESIDENT_ORD_BUDGET / DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "start")!, page(rows(1, 100), false, true))!;
    for (let i = 0; i < fullPages + 1; i++) {
      tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "newer")!, page(rows(101 + i * 100, 200 + i * 100), true, true))!;
    }
    expect(residentPageOrds(tw.pages)).toBe(TRANSCRIPT_RESIDENT_ORD_BUDGET);
    expect(tw.hasMoreBefore).toBe(true);
    expect(transcriptPageRequest(tw, "older")).toEqual({ edge: "older", beforeMessageId: "m201" });
  });

  it("offers no edge that the window has already reached", () => {
    const tw = tailAt(1, 100);
    expect(transcriptPageRequest(tw, "older")).toBeUndefined();
    expect(transcriptPageRequest(tw, "newer")).toBeUndefined();
    expect(transcriptPageRequest(tw, "start")).toBeUndefined();
  });

  it("drops live overflow into an open gap instead of evicting history being read", () => {
    let tw = tailAt(1801, 2000);
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "start")!, page(rows(1, 100), false, true))!;
    const history = Object.keys(tw.pages);
    for (let ord = 2001; ord <= 2001 + DEFAULT_TRANSCRIPT_PAGE_LIMIT; ord++) tw = appendToTail(tw, msg(`m${ord}`, ord));
    expect(Object.keys(tw.pages)).toEqual(history);
    expect(tw.tail.length).toBeLessThanOrEqual(TRANSCRIPT_TAIL_BUDGET);
    expect(tw.hasTailGap).toBe(true);
  });
});

describe("refreshTailWindow", () => {
  it("merges an overlapping durable page, newest revision winning", () => {
    let tw = appendToTail(emptyTranscriptWindow(), msg("m3", 3, 9));
    tw = refreshTailWindow(tw, page([msg("m2", 2), msg("m3", 3, 1)], true));
    expect(tw.tail.map((m) => [m.id, m.seq])).toEqual([["m2", 2], ["m3", 9]]);
    expect(tw.hasMoreBefore).toBe(true);
  });

  it("keeps resident older pages when the refreshed tail overlaps", () => {
    let tw = installTailWindow(emptyTranscriptWindow(), page(rows(101, 200), true), { resetPages: true });
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "older")!, page(rows(1, 100), false, true))!;
    tw = refreshTailWindow(tw, page(rows(151, 250), true));
    expect(ids(tw)[0]).toBe(1);
    expect(ids(tw).at(-1)).toBe(250);
    expect(tw.hasMoreBefore).toBe(false);
    expect(tw.hasTailGap).toBe(false);
  });

  it("replaces a window the durable tail has moved past", () => {
    let tw = installTailWindow(emptyTranscriptWindow(), page(rows(1, 100)), { resetPages: true });
    tw = refreshTailWindow(tw, page(rows(401, 500), true));
    expect(ids(tw)).toEqual(rows(401, 500).map((m) => m.ord));
    expect(tw.hasMoreBefore).toBe(true);
  });

  it("leaves the window unchanged for an empty page", () => {
    const tw = appendToTail(emptyTranscriptWindow(), msg("live", 1));
    expect(refreshTailWindow(tw, page([]))).toBe(tw);
  });

  it("tracks session ordinal bounds, unloaded spans, and ordinal seeks", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      {
        messages: rows(401, 500),
        hasMoreBefore: true,
        hasMoreAfter: false,
        bounds: { firstOrd: 1, lastOrd: 500 },
      },
      { resetPages: true },
    );
    expect(presentedOrdSpan(tw)).toEqual({ firstOrd: 401, lastOrd: 500 });
    expect(unloadedTranscriptOrds(tw)).toEqual({ before: 400, after: 0 });

    const seek = transcriptSeekRequest(tw, 200, 100)!;
    expect(seek).toEqual({ edge: "seek", afterOrd: 149 });
    expect(transcriptPageQuery(seek)).toEqual({ afterOrd: 149 });

    tw = applyTranscriptPage(tw, seek, {
      messages: rows(150, 249),
      hasMoreBefore: true,
      hasMoreAfter: true,
      bounds: { firstOrd: 1, lastOrd: 500 },
    })!;
    expect(tw.hasTailGap).toBe(true);
    expect(presentedOrdSpan(tw)).toEqual({ firstOrd: 150, lastOrd: 249 });
    expect(unloadedTranscriptOrds(tw)).toEqual({ before: 149, after: 251 });
  });

  it("retains the reader's page during tail overflow until the reader moves away", () => {
    let tw = emptyTranscriptWindow();
    const fullPages = TRANSCRIPT_RESIDENT_ORD_BUDGET / DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    const rolls = fullPages + 2;
    const total = TRANSCRIPT_TAIL_BUDGET + rolls * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    const keepOrds = { firstOrd: 10, lastOrd: 20 };
    for (let i = 1; i <= total; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i), { keepOrds });
    }
    const residentBeforeTrim = materializeTranscriptWindow(tw);
    expect(residentBeforeTrim.some((m) => m.ord === 15)).toBe(true);

    // Once the reader moves away from the oldest page, trimming reclaims it.
    const trimmed = trimOldestPages(tw);
    expect(residentPageOrds(trimmed.pages)).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_BUDGET);
    expect(materializeTranscriptWindow(trimmed).some((m) => m.ord === 15)).toBe(false);
  });

  it("evicts an unprotected page instead of the reader's keepOrds page when exceeding the hard cap", () => {
    let tw = emptyTranscriptWindow();
    const keepOrds = { firstOrd: 10, lastOrd: 20 };
    const rolls = TRANSCRIPT_RESIDENT_ORD_HARD_CAP / DEFAULT_TRANSCRIPT_PAGE_LIMIT + 2;
    const total = TRANSCRIPT_TAIL_BUDGET + rolls * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    for (let i = 1; i <= total; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i), { keepOrds });
    }
    expect(residentPageOrds(tw.pages)).toBeLessThanOrEqual(TRANSCRIPT_RESIDENT_ORD_HARD_CAP);
    expect(materializeTranscriptWindow(tw).some((m) => m.ord === 15)).toBe(true);
    expect(tw.hasTailGap).toBe(true);
  });

  it("keeps seven byte-bounded older pages resident when their total messages fit the budget", () => {
    // 254 messages across 7 older pages + 51-message tail = 305 messages total.
    const lengths = [31, 34, 48, 33, 39, 34, 35];
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page(rows(255, 305), true, false),
      { resetPages: true },
    );
    let end = 254;
    for (const len of lengths) {
      const start = end - len + 1;
      const req = transcriptPageRequest(tw, "older")!;
      tw = applyTranscriptPage(tw, req, page(rows(start, end), start > 1, true))!;
      end = start - 1;
    }
    expect(Object.keys(tw.pages)).toHaveLength(7);
    expect(residentPageOrds(tw.pages)).toBe(254);
    expect(tw.hasTailGap).toBe(false);
    expect(tw.hasMoreBefore).toBe(false);
    expect(contiguousWindowRows(tw).map((m) => m.ord)).toEqual(
      rows(1, 305).map((m) => m.ord),
    );
  });

  it("never evicts the newly applied page even if a single page exceeds the budget", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page(rows(701, 750), true, false),
      { resetPages: true },
    );
    const evicted: TranscriptEvictedPage[] = [];
    tw = loadOlderWindow(tw, page(rows(601, 700), true, true));
    tw = loadOlderWindow(tw, page(rows(1, 600), false, true), { evicted });
    expect(tw.pages["1:600"]).toHaveLength(600);
    expect(tw.pages["601:700"]).toBeUndefined();
    expect(evicted).toEqual([
      {
        firstOrd: 601,
        lastOrd: 700,
        side: "newer",
        residentOrds: 600,
        deferredByReach: false,
      },
    ]);
  });
});

describe("mergeIntoTail", () => {
  const tailAt = (from: number, to: number) =>
    installTailWindow(emptyTranscriptWindow(), page(rows(from, to), from > 1), { resetPages: true });

  it("keeps resident tail rows older than an overlapping refreshed page without opening a gap", () => {
    let tw = tailAt(201, 400);
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "older")!, page(rows(101, 200), true, true))!;
    tw = mergeIntoTail(tw, page(rows(311, 410), true), 410);
    expect(tw.hasTailGap).toBe(false);
    expect(ids(tw)).toEqual(rows(101, 410).map((m) => m.ord));
  });

  it("keeps an unbridged old tail as the newest older page behind a gap", () => {
    let tw = tailAt(201, 300);
    tw = mergeIntoTail(tw, page(rows(401, 500), true), 500);
    expect(tw.hasTailGap).toBe(true);
    expect(contiguousWindowRows(tw).map((m) => m.ord)).toEqual(rows(201, 300).map((m) => m.ord));
    expect(tw.tail.map((m) => m.ord)).toEqual(rows(401, 500).map((m) => m.ord));
  });

  it("keeps the older edge of resident pages that reach the start", () => {
    let tw = tailAt(101, 200);
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "older")!, page(rows(1, 100), false, true))!;
    tw = mergeIntoTail(tw, page(rows(151, 250), true), 250);
    expect(tw.hasMoreBefore).toBe(false);
    expect(ids(tw)).toEqual(rows(1, 250).map((m) => m.ord));
  });
});
