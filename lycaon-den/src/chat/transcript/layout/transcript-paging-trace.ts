import type { Message } from "../../../api/types.ts";
import {
  denScrollDebugLog,
  isStreamScrollDebugEnabled,
} from "../../stream/den-scroll-debug.ts";
import type {
  TranscriptRunwayDensityCause,
  TranscriptRunwayDensityChange,
  TranscriptUnloadedOrds,
} from "../history/transcript-runway.ts";
import type { HistoryDirection } from "./transcript-page-intent.ts";
import {
  newestPageMessage,
  oldestLoadedMessage,
  presentedOrdSpan,
  unloadedTranscriptOrds,
  type TranscriptEvictedPage,
  type TranscriptOrdSpan,
  type TranscriptPageRequest,
  type TranscriptWindow,
} from "./transcript-window.ts";

export type { TranscriptRunwayDensityCause, TranscriptRunwayDensityChange };

export type TranscriptPagingTraceLayout = {
  residentStartPx: number;
  residentEndPx: number;
  residentSpan: TranscriptOrdSpan | undefined;
  unloaded: TranscriptUnloadedOrds;
};

function cursorOrdForRequest(
  tw: TranscriptWindow,
  request: TranscriptPageRequest,
): number {
  switch (request.edge) {
    case "older":
      return oldestLoadedMessage(tw)?.ord ?? 0;
    case "newer":
      return newestPageMessage(tw)?.ord ?? 0;
    case "seek":
      return request.afterOrd;
    case "start":
      return tw.bounds?.firstOrd ?? 1;
  }
}

export function traceTranscriptPageRequest(opts: {
  tw: TranscriptWindow;
  request: TranscriptPageRequest;
  intent: HistoryDirection | undefined;
  scrollTop: number;
  scrollHeight: number;
  layout: TranscriptPagingTraceLayout | null;
}): void {
  if (!isStreamScrollDebugEnabled()) return;
  const { tw, request, intent, scrollTop, scrollHeight, layout } = opts;
  const span = layout?.residentSpan ?? presentedOrdSpan(tw);
  const unloaded = layout?.unloaded ?? unloadedTranscriptOrds(tw);
  denScrollDebugLog("scroll", "transcript.page.request", {
    edge: request.edge,
    cursor_ord: cursorOrdForRequest(tw, request),
    reason: request.edge === "seek" ? "seek" : "edge",
    intent: intent ?? "none",
    resident_first_ord: span?.firstOrd ?? 0,
    resident_last_ord: span?.lastOrd ?? 0,
    unloaded_before: unloaded.before,
    unloaded_after: unloaded.after,
    scroll_top: Math.round(scrollTop),
    resident_start_px: Math.round(layout?.residentStartPx ?? 0),
    resident_end_px: Math.round(layout?.residentEndPx ?? scrollHeight),
  });
}

export function traceTranscriptPageApply(opts: {
  request: TranscriptPageRequest;
  messages: readonly Message[];
  applied: boolean;
  advanced: boolean;
  tw: TranscriptWindow;
}): void {
  if (!isStreamScrollDebugEnabled()) return;
  const { request, messages, applied, advanced, tw } = opts;
  denScrollDebugLog("scroll", "transcript.page.apply", {
    edge: request.edge,
    rows: messages.length,
    first_ord: messages[0]?.ord ?? 0,
    last_ord: messages[messages.length - 1]?.ord ?? 0,
    applied,
    stale: !applied,
    advanced,
    has_tail_gap: tw.hasTailGap,
    has_more_before: tw.hasMoreBefore,
  });
}

export function traceTranscriptPageEvictions(
  evicted: readonly TranscriptEvictedPage[] | undefined,
): void {
  if (!evicted || evicted.length === 0 || !isStreamScrollDebugEnabled()) return;
  for (const victim of evicted) {
    denScrollDebugLog("scroll", "transcript.page.evict", {
      victim_first_ord: victim.firstOrd,
      victim_last_ord: victim.lastOrd,
      side: victim.side,
      resident_ords: victim.residentOrds,
      deferred_by_reach: victim.deferredByReach,
    });
  }
}

export function traceTranscriptRunwayDensity(
  opts: TranscriptRunwayDensityChange,
): void {
  if (!isStreamScrollDebugEnabled()) return;
  denScrollDebugLog("scroll", "transcript.runway.density", {
    from_px: Math.round(opts.fromPx * 100) / 100,
    to_px: Math.round(opts.toPx * 100) / 100,
    measured_ords: Math.round(opts.measuredOrds),
    before_px: opts.beforePx,
    after_px: opts.afterPx,
    cause: opts.cause,
  });
}
