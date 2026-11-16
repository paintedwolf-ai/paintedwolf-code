// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "../../../api/types.ts";
import { createAppStore } from "../../../store/app-state.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { resetStreamScrollDebugForTests } from "../../stream/den-scroll-debug.ts";
import { createTranscriptRunwayBudget } from "../history/transcript-runway.ts";
import { createTranscriptPaging } from "./transcript-load-more.ts";
import {
  traceTranscriptPageApply,
  traceTranscriptRunwayDensity,
} from "./transcript-paging-trace.ts";
import { TRANSCRIPT_RESIDENT_ORD_BUDGET } from "./transcript-window.ts";

function msg(ord: number): Message {
  return {
    id: `secret-msg-id-${ord}`,
    role: "user",
    origin: "user",
    authority: "user",
    trust_tier: "trusted",
    content: `secret-body-${ord}`,
    ord,
    seq: ord,
    created_at: "2026-01-01T00:00:00Z",
  };
}

function rows(from: number, to: number): Message[] {
  return Array.from({ length: to - from + 1 }, (_, i) => msg(from + i));
}

function setDebugLevel(level: "0" | "1"): void {
  localStorage.setItem("den:scroll-debug", level);
  resetStreamScrollDebugForTests();
}

describe("transcript paging decision trace", () => {
  afterEach(() => {
    localStorage.removeItem("den:scroll-debug");
    resetStreamScrollDebugForTests();
    vi.restoreAllMocks();
  });

  it("emits no paging or runway records when scroll debug is disabled", async () => {
    setDebugLevel("0");
    const info = vi.spyOn(console, "info").mockImplementation(() => {});

    const budget = createTranscriptRunwayBudget(traceTranscriptRunwayDensity);
    budget.observe({
      unloaded: { before: 200, after: 0 },
      measuredHeightPx: 1_500,
      measuredOrdSpan: 50,
    });

    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-quiet", rows(51, 100), 100, {
      hasMoreBefore: true,
      bounds: { firstOrd: 1, lastOrd: 100 },
    });
    const el = document.createElement("div");
    Object.defineProperty(el, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 0, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 2_000, configurable: true });
    document.body.appendChild(el);
    const paging = createTranscriptPaging(el, {
      client: () =>
        stubClient({
          listSessionMessages: vi.fn().mockResolvedValue({
            messages: rows(1, 50),
            watermark: 100,
            turn_clocks: {},
            turn_loads: {},
          }),
        }),
      appStore,
      sessionId: () => "s-quiet",
    });
    try {
      await vi.waitFor(() => expect(appStore.state.messages).toHaveLength(100));
      expect(info).not.toHaveBeenCalled();
    } finally {
      paging.dispose();
      el.remove();
    }
  });

  it("emits request, apply, evict, and runway density records without message IDs or content", async () => {
    setDebugLevel("1");
    const lines: string[] = [];
    vi.spyOn(console, "info").mockImplementation((...args: unknown[]) => {
      lines.push(args.map(String).join(" "));
    });

    const budget = createTranscriptRunwayBudget(traceTranscriptRunwayDensity);
    budget.observe({
      unloaded: { before: 200, after: 0 },
      measuredHeightPx: 1_250,
      measuredOrdSpan: 50,
    });
    budget.observe({
      unloaded: { before: 100, after: 0 },
      measuredHeightPx: 2_000,
      measuredOrdSpan: 100,
    });
    budget.observe({
      unloaded: { before: 0, after: 0 },
      measuredHeightPx: 6_000,
      measuredOrdSpan: 200,
    });

    const densityLines = lines.filter((line) =>
      line.includes("transcript.runway.density"),
    );
    expect(densityLines).toEqual([
      "[den:scroll] transcript.runway.density from_px=84 to_px=25 measured_ords=50 before_px=5000 after_px=0 cause=first_measure",
      "[den:scroll] transcript.runway.density from_px=25 to_px=20 measured_ords=100 before_px=2000 after_px=0 cause=page_landed",
      "[den:scroll] transcript.runway.density from_px=20 to_px=30 measured_ords=200 before_px=0 after_px=0 cause=start_loaded",
    ]);

    lines.length = 0;
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline("s-trace", rows(601, 700), 700, {
      hasMoreBefore: true,
      bounds: { firstOrd: 1, lastOrd: 700 },
    });
    // Preload 500 older messages (ords 101..600) so the next older page evicts 501..600.
    for (let end = 600; end >= 200; end -= 100) {
      const start = end - 99;
      appStore.actions.loadTranscriptPage(
        { edge: "older", beforeMessageId: msg(end + 1).id },
        {
          messages: rows(start, end),
          before_cursor: "older",
          after_cursor: "newer",
          watermark: 700,
          turn_clocks: {},
          turn_loads: {},
        },
      );
    }
    expect(
      Object.values(appStore.state.transcript.pages).reduce((n, p) => n + p.length, 0),
    ).toBe(TRANSCRIPT_RESIDENT_ORD_BUDGET);

    const el = document.createElement("div");
    Object.defineProperty(el, "clientHeight", { value: 500, configurable: true });
    Object.defineProperty(el, "scrollTop", { value: 200, configurable: true });
    Object.defineProperty(el, "scrollHeight", { value: 15_000, configurable: true });
    document.body.appendChild(el);

    const paging = createTranscriptPaging(el, {
      client: () =>
        stubClient({
          listSessionMessages: vi.fn().mockResolvedValue({
            messages: rows(1, 100),
            watermark: 700,
            turn_clocks: {},
            turn_loads: {},
          }),
        }),
      appStore,
      sessionId: () => "s-trace",
      geometry: () => ({
        residentStartPx: 100,
        residentEndPx: 12_100,
        densityPxPerOrd: 20,
        residentSpan: { firstOrd: 101, lastOrd: 700 },
        unloaded: { before: 100, after: 0 },
        prepareLanding: () => {},
      }),
    });

    try {
      await vi.waitFor(() =>
        expect(appStore.state.transcript.hasMoreBefore).toBe(false),
      );
      expect(
        lines.some((line) =>
          line.includes(
            "[den:scroll] transcript.page.request edge=older cursor_ord=101 reason=edge intent=older resident_first_ord=101 resident_last_ord=700 unloaded_before=100 unloaded_after=0 scroll_top=200 resident_start_px=100 resident_end_px=12100",
          ),
        ),
      ).toBe(true);
      expect(
        lines.some((line) =>
          line.includes(
            "[den:scroll] transcript.page.evict victim_first_ord=501 victim_last_ord=600 side=newer resident_ords=500 deferred_by_reach=false",
          ),
        ),
      ).toBe(true);
      expect(
        lines.some((line) =>
          line.includes(
            "[den:scroll] transcript.page.apply edge=older rows=100 first_ord=1 last_ord=100 applied=true stale=false advanced=true has_tail_gap=true has_more_before=false",
          ),
        ),
      ).toBe(true);
      traceTranscriptPageApply({
        request: { edge: "seek", afterOrd: 0 },
        messages: rows(1, 100),
        applied: true,
        advanced: false,
        tw: appStore.state.transcript,
      });
      expect(
        lines.some((line) =>
          line.includes(
            "[den:scroll] transcript.page.apply edge=seek rows=100 first_ord=1 last_ord=100 applied=true stale=false advanced=false has_tail_gap=true has_more_before=false",
          ),
        ),
      ).toBe(true);
      for (const line of lines) {
        expect(line).not.toContain("secret-msg-id");
        expect(line).not.toContain("secret-body");
      }
    } finally {
      paging.dispose();
      el.remove();
    }
  });
});
