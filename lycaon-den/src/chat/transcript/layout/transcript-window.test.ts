// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  TRANSCRIPT_PAGE_BUDGET,
  TRANSCRIPT_TAIL_BUDGET,
  appendToTail,
  applyTranscriptPage,
  contiguousWindowRows,
  emptyTranscriptWindow,
  installTailWindow,
  loadOlderWindow,
  materializeTranscriptWindow,
  patchRowInWindow,
  precedesLiveTail,
  refreshTailWindow,
  transcriptPageRequest,
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

  it("appends SSE rows to the live tail", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("a", 1)], false, false),
      { resetPages: true },
    );
    tw = appendToTail(tw, msg("b", 2));
    expect(tw.tail.map((m) => m.id)).toEqual(["a", "b"]);
  });

  it("patches an in-slice edit without rebuilding history", () => {
    const tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("a", 1, 1)], false, false),
      { resetPages: true },
    );
    expect(patchRowInWindow(tw, msg("a", 1, 2))).toBe(true);
    expect(tw.tail[0]?.seq).toBe(2);
  });

  it("loads older pages and evicts beside the tail beyond the page budget", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("t", 100)], true, false),
      { resetPages: true },
    );
    for (let i = 0; i < TRANSCRIPT_PAGE_BUDGET + 2; i++) {
      const base = 90 - i * 10;
      tw = loadOlderWindow(
        tw,
        page([msg(`p${i}a`, base), msg(`p${i}b`, base + 1)], true, true),
      );
    }
    expect(Object.keys(tw.pages).length).toBe(TRANSCRIPT_PAGE_BUDGET);
    expect(tw.hasTailGap).toBe(true);
    expect(materializeTranscriptWindow(tw).length).toBeLessThanOrEqual(
      TRANSCRIPT_PAGE_BUDGET * 2 + 1,
    );
    expect(tw.tail).toHaveLength(1);
  });

  it("records a tail gap only after page eviction", () => {
    let tw = installTailWindow(
      emptyTranscriptWindow(),
      page([msg("tail", 100)], true, false),
      { resetPages: true },
    );
    for (let index = 0; index < TRANSCRIPT_PAGE_BUDGET; index += 1) {
      tw = loadOlderWindow(tw, page([msg(`p${index}`, 90 - index)], true));
    }
    expect(tw.hasTailGap).toBe(false);

    tw = loadOlderWindow(tw, page([msg("evicting", 70)], true));
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
    // Enough appends to roll TRANSCRIPT_PAGE_BUDGET + 2 pages.
    const rolls = TRANSCRIPT_PAGE_BUDGET + 2;
    const total =
      TRANSCRIPT_TAIL_BUDGET + rolls * DEFAULT_TRANSCRIPT_PAGE_LIMIT;
    for (let i = 0; i < total; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i));
    }
    expect(Object.keys(tw.pages).length).toBe(TRANSCRIPT_PAGE_BUDGET);
    expect(tw.hasMoreBefore).toBe(true);
    // The retained window is the newest contiguous run: no interior holes.
    const ords = materializeTranscriptWindow(tw).map((m) => m.ord ?? 0);
    for (let i = 1; i < ords.length; i++) {
      expect(ords[i]).toBe((ords[i - 1] ?? 0) + 1);
    }
    expect(ords[ords.length - 1]).toBe(total - 1);
  });

  it("patches rows that rolled out of the tail into a page", () => {
    let tw = emptyTranscriptWindow();
    for (let i = 0; i <= TRANSCRIPT_TAIL_BUDGET; i++) {
      tw = appendToTail(tw, msg(`m${i}`, i));
    }
    const rolled = { ...msg("m0", 0, 500), content: "patched" };
    expect(patchRowInWindow(tw, rolled)).toBe(true);
    expect(
      materializeTranscriptWindow(tw).find((m) => m.id === "m0")?.content,
    ).toBe("patched");
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

  it("reads older history before the oldest resident row", () => {
    const tw = tailAt(901, 1000);
    const request = transcriptPageRequest(tw, "older");
    expect(request).toEqual({ edge: "older", beforeMessageId: "m901" });
    const next = applyTranscriptPage(tw, request!, page(rows(801, 900), true, true));
    expect(ids(next!)[0]).toBe(801);
    expect(next!.hasMoreBefore).toBe(true);
  });

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
    tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "start")!, page(rows(1, 100), false, true))!;
    for (let i = 0; i < TRANSCRIPT_PAGE_BUDGET + 1; i++) {
      tw = applyTranscriptPage(tw, transcriptPageRequest(tw, "newer")!, page(rows(101 + i * 100, 200 + i * 100), true, true))!;
    }
    expect(Object.keys(tw.pages)).toHaveLength(TRANSCRIPT_PAGE_BUDGET);
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
});

