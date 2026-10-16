import { createMemo, type Accessor } from "solid-js";
import { scrollportMotionForViewport } from "../../../platform/scrolling/scrollport-motion.ts";
import { TranscriptPageIntent } from "./transcript-page-intent.ts";
import type { LycaonClient } from "../../../api/client.ts";
import type { Message } from "../../../api/types.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import {
  subscribeStreamScroll,
} from "../../stream/stream-scroll.ts";
import type { TranscriptRevealAnchor } from "../presentation/transcript-reveal-target.ts";
import { isTranscriptTimeItem } from "../presentation/transcript-time-rows.ts";
import type { DisplayTranscriptItem } from "../projection/transcript-item-model.ts";
import { transcriptItemOrd } from "../projection/transcript-item-order.ts";
import { publishTranscriptReaderReach } from "../history/transcript-reader-reach.ts";
import type { TranscriptUnloadedOrds } from "../history/transcript-runway.ts";
import type { MeasuredTranscriptVirtualizer } from "./transcript-virtualizer.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  contiguousWindowRows,
  presentedOrdSpan,
  transcriptPageQuery,
  transcriptPageRequest,
  transcriptPageRequestKey,
  transcriptSeekRequest,
  unloadedTranscriptOrds,
  type TranscriptOrdSpan,
  type TranscriptPageEdge,
  type TranscriptPageRequest,
} from "./transcript-window.ts";

/** History loads while the reader is within this many viewports of a resident edge. */
export const LOAD_MORE_VIEWPORTS = 2;

/** Session-coordinate geometry for the presented resident window and unloaded runways. */
export type TranscriptHistoryGeometry = {
  /** Scroll offset where the first resident row begins (after the older runway). */
  residentStartPx: number;
  /** Scroll offset where the last resident row ends (before the newer runway). */
  residentEndPx: number;
  /** Ordinal span of the presented resident window, when any rows are resident. */
  residentSpan: TranscriptOrdSpan | undefined;
  /** Unloaded ordinal counts on each side of the presented resident window. */
  unloaded: TranscriptUnloadedOrds;
  /** Freezes the runway side a scrollbar seek targets until the sought page lands. */
  prepareLanding: (targetPx: number) => void;
};

/** Session-coordinate history inputs and geometry for a chat transcript's virtualizer and paging. */
export function createTranscriptHistoryView(opts: {
  enabled: () => boolean;
  messages: () => readonly Message[];
  items: Accessor<readonly DisplayTranscriptItem[]>;
  appStore: () => AppStore | undefined;
}): {
  unloadedOrds: () => TranscriptUnloadedOrds;
  residentRowSpan: () => { count: number; ordSpan: number };
  geometry: (virtualizer: MeasuredTranscriptVirtualizer) => TranscriptHistoryGeometry | null;
} {
  const ordById = createMemo(() => {
    const map = new Map<string, number>();
    for (const msg of opts.messages()) {
      if (msg.ord != null && msg.ord > 0) map.set(msg.id, msg.ord);
    }
    return map;
  });
  const residentOrdSpan = createMemo<TranscriptOrdSpan | undefined>(() => {
    const store = opts.appStore();
    if (store) return presentedOrdSpan(store.state.transcript);
    let firstOrd = Infinity;
    let lastOrd = -Infinity;
    for (const item of opts.items()) {
      const ord = transcriptItemOrd(item, ordById());
      if (ord === undefined) continue;
      if (ord < firstOrd) firstOrd = ord;
      if (ord > lastOrd) lastOrd = ord;
    }
    return firstOrd <= lastOrd ? { firstOrd, lastOrd } : undefined;
  });
  const residentRowSpan = createMemo(() => {
    const span = residentOrdSpan();
    const messageBacked = opts
      .items()
      .filter((item) => !isTranscriptTimeItem(item) && item.kind !== "pending_user").length;
    const ordSpan = span ? Math.max(1, span.lastOrd - span.firstOrd + 1) : messageBacked;
    return { count: messageBacked, ordSpan };
  });
  const unloadedOrds = createMemo<TranscriptUnloadedOrds>(() => {
    if (!opts.enabled()) return { before: 0, after: 0 };
    const store = opts.appStore();
    return store ? unloadedTranscriptOrds(store.state.transcript) : { before: 0, after: 0 };
  });
  const geometry = (
    virtualizer: MeasuredTranscriptVirtualizer,
  ): TranscriptHistoryGeometry | null => {
    if (!opts.enabled()) return null;
    const extent = virtualizer.residentExtent();
    return {
      residentStartPx: extent.startPx,
      residentEndPx: extent.endPx,
      residentSpan: residentOrdSpan(),
      unloaded: unloadedOrds(),
      prepareLanding: (targetPx) => virtualizer.prepareLanding(targetPx),
    };
  };
  return { unloadedOrds, residentRowSpan, geometry };
}

async function fetchTranscriptRequest(
  client: LycaonClient,
  appStore: AppStore,
  sessionId: string,
  request: TranscriptPageRequest,
): Promise<boolean> {
  if (appStore.state.transcriptSessionId !== sessionId) return false;
  const epoch = appStore.state.sessionViewEpoch;
  const prevSpan = presentedOrdSpan(appStore.state.transcript);
  const page = await client.listSessionMessages(sessionId, {
    limit: DEFAULT_TRANSCRIPT_PAGE_LIMIT,
    ...transcriptPageQuery(request),
  });
  if (appStore.state.transcriptSessionId !== sessionId || appStore.state.sessionViewEpoch !== epoch) return false;
  if (!appStore.actions.loadTranscriptPage(request, page) || page.messages.length === 0) return false;
  if (request.edge === "seek") {
    const nextSpan = presentedOrdSpan(appStore.state.transcript);
    return Boolean(
      nextSpan &&
        (!prevSpan ||
          nextSpan.firstOrd !== prevSpan.firstOrd ||
          nextSpan.lastOrd !== prevSpan.lastOrd),
    );
  }
  const next = transcriptPageRequest(appStore.state.transcript, request.edge);
  return !next || transcriptPageRequestKey(next) !== transcriptPageRequestKey(request);
}

/** Fetches one page at a resident edge; returns true when the resident edge advanced. */
export async function loadTranscriptPage(
  client: LycaonClient,
  appStore: AppStore,
  sessionId: string,
  edge: Exclude<TranscriptPageEdge, "start">,
): Promise<boolean> {
  const request = transcriptPageRequest(appStore.state.transcript, edge);
  if (!request) return false;
  return fetchTranscriptRequest(client, appStore, sessionId, request);
}

/** True when the row containing an anchor is resident. */
function revealAnchorResident(
  messages: readonly Message[],
  anchor: TranscriptRevealAnchor,
): boolean {
  const id = anchor.anchorId.trim();
  if (!id) return false;
  if (anchor.chicklet === "tool") {
    return messages.some(
      (msg) =>
        (msg.tool_calls ?? []).some((call) => (call.id ?? "").trim() === id) ||
        (msg.tool_result?.tool_call_id ?? "").trim() === id,
    );
  }
  return messages.some((msg) => msg.id === id);
}

/** Load older pages until an anchor is resident or history is exhausted. */
export function ensureTranscriptRevealAnchorLoaded(
  client: LycaonClient,
  appStore: AppStore,
  sessionId: string,
  anchor: TranscriptRevealAnchor,
): Promise<boolean> {
  // Across a tail gap only presented rows count; the walk resets to reach the tail.
  return loadTranscriptHistoryUntil(client, appStore, sessionId, () =>
    revealAnchorResident(contiguousWindowRows(appStore.state.transcript), anchor),
  );
}

/** Load older pages until `resident` holds or history is exhausted. */
export async function loadTranscriptHistoryUntil(
  client: LycaonClient,
  appStore: AppStore,
  sessionId: string,
  resident: () => boolean,
): Promise<boolean> {
  const epoch = appStore.state.sessionViewEpoch;
  const current = () => appStore.state.transcriptSessionId === sessionId && appStore.state.sessionViewEpoch === epoch;
  if (!current()) return false;
  if (resident()) return true;
  // A tail gap requires a contiguous scan from the tail.
  if (appStore.state.transcript.hasTailGap) {
    appStore.actions.resetOlderTranscriptPages();
    if (resident()) return true;
  }
  while (current() && await loadTranscriptPage(client, appStore, sessionId, "older")) {
    if (!current()) return false;
    if (resident()) return true;
  }
  return current() && resident();
}

function ordInRunway(
  targetPx: number,
  layout: TranscriptHistoryGeometry,
): number | undefined {
  const span = layout.residentSpan;
  if (!span) return undefined;
  if (
    layout.unloaded.before > 0 &&
    layout.residentStartPx > 0 &&
    targetPx < layout.residentStartPx
  ) {
    const ratio = Math.min(1, Math.max(0, targetPx / layout.residentStartPx));
    const firstOrd = Math.max(1, span.firstOrd - layout.unloaded.before);
    const beforeSpan = Math.max(0, span.firstOrd - 1 - firstOrd);
    return Math.round(firstOrd + ratio * beforeSpan);
  }
  return undefined;
}

function readerReachOrds(
  layout: TranscriptHistoryGeometry | null,
  reachTopPx: number,
  reachBottomPx: number,
): TranscriptOrdSpan | undefined {
  if (!layout?.residentSpan) return undefined;
  const residentHeight = layout.residentEndPx - layout.residentStartPx;
  if (residentHeight <= 0) return layout.residentSpan;
  const { firstOrd, lastOrd } = layout.residentSpan;
  const ordSpan = Math.max(0, lastOrd - firstOrd);
  const topRatio = Math.min(
    1,
    Math.max(0, (reachTopPx - layout.residentStartPx) / residentHeight),
  );
  const bottomRatio = Math.min(
    1,
    Math.max(0, (reachBottomPx - layout.residentStartPx) / residentHeight),
  );
  const start = Math.round(firstOrd + topRatio * ordSpan);
  const end = Math.max(start, Math.round(firstOrd + bottomRatio * ordSpan));
  return { firstOrd: start, lastOrd: end };
}

/**
 * Load history while the reader nears a resident edge: older pages near the resident start,
 * newer pages across a tail gap near the resident end, and an ordinal seek when the reader
 * jumps into an unloaded session runway beyond the resident reach.
 */
export function createTranscriptPaging(
  streamTarget: HTMLElement,
  opts: {
    client: () => LycaonClient | undefined;
    appStore: AppStore;
    sessionId: () => string | undefined;
    geometry?: () => TranscriptHistoryGeometry | null;
  },
): { dispose(): void; readerInput(direction: "up" | "down"): void } {
  let inFlight = false;
  let stopped = false;
  const intent = new TranscriptPageIntent();
  let settleFrame: number | undefined;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let previousOffset = streamTarget.scrollTop;
  const motion = scrollportMotionForViewport(streamTarget);

  const nextRequest = (): TranscriptPageRequest | undefined => {
    const top = streamTarget.scrollTop;
    const height = streamTarget.clientHeight;
    const bottom = top + height;
    const reach = height * LOAD_MORE_VIEWPORTS;
    const transcript = opts.appStore.state.transcript;
    const layout = opts.geometry?.() ?? null;
    const sessionId = opts.sessionId()?.trim();
    if (sessionId) {
      publishTranscriptReaderReach(
        sessionId,
        readerReachOrds(layout, top - reach, bottom + reach),
      );
      opts.appStore.actions.trimTranscriptPages();
    }
    if (layout) {
      if (bottom < layout.residentStartPx - reach || top > layout.residentEndPx + reach) {
        const ord = ordInRunway(top, layout);
        if (ord !== undefined) return transcriptSeekRequest(transcript, ord);
      }
      const toOlder = top - layout.residentStartPx;
      const toNewer = layout.residentEndPx - bottom;
      const edge = intent.edge(
        transcript.hasMoreBefore && toOlder <= reach,
        transcript.hasTailGap && toNewer <= reach,
      );
      return edge ? transcriptPageRequest(transcript, edge) : undefined;
    }
    const belowReader = streamTarget.scrollHeight - top - height;
    const edge = intent.edge(
      transcript.hasMoreBefore && top <= reach,
      transcript.hasTailGap && belowReader <= reach,
    );
    return edge ? transcriptPageRequest(transcript, edge) : undefined;
  };

  const maybeLoad = () => {
    if (stopped || inFlight) return;
    const sessionId = opts.sessionId()?.trim();
    const client = opts.client();
    if (!sessionId || !client) return;
    const request = nextRequest();
    if (!request) return;
    const key = transcriptPageRequestKey(request);
    const delay = intent.retryDelay(key, performance.now());
    if (delay === undefined) return;
    if (delay > 0) {
      retryTimer ??= setTimeout(() => { retryTimer = undefined; maybeLoad(); }, delay);
      return;
    }
    if (request.edge === "seek") {
      opts.geometry?.()?.prepareLanding(streamTarget.scrollTop);
    }
    inFlight = true;
    intent.started();
    let advanced = false;
    void fetchTranscriptRequest(client, opts.appStore, sessionId, request)
      .then((added) => {
        if (!added) {
          intent.failed(key, performance.now());
          return;
        }
        advanced = true;
        intent.advanced();
        if (stopped) return;
        // Continue after the reading anchor settles.
        settleFrame = requestAnimationFrame(maybeLoad);
      })
      .catch(() => intent.failed(key, performance.now()))
      .finally(() => {
        inFlight = false;
        if (!advanced) maybeLoad();
      });
  };

  const stopCommits = motion?.subscribeCommits((source, position) => {
    if ((source === "thumb_drag" || source === "track_click") && position.top !== previousOffset) {
      intent.noteInput(position.top < previousOffset ? "older" : "newer");
      maybeLoad();
    }
    previousOffset = position.top;
  });
  const unsubscribe = subscribeStreamScroll(streamTarget, () => {
    const offset = streamTarget.scrollTop;
    if (motion?.input.isDirectInputActive() && Math.abs(offset - previousOffset) > 1) {
      intent.noteInput(offset < previousOffset ? "older" : "newer");
    }
    previousOffset = offset;
    maybeLoad();
  });
  queueMicrotask(maybeLoad);
  return {
    readerInput: (direction) => {
      intent.noteInput(direction === "up" ? "older" : "newer");
      maybeLoad();
    },
    dispose: () => {
      stopped = true;
      const sessionId = opts.sessionId()?.trim();
      if (sessionId) publishTranscriptReaderReach(sessionId, undefined);
      unsubscribe();
      stopCommits?.();
      if (retryTimer !== undefined) clearTimeout(retryTimer);
      if (settleFrame !== undefined) cancelAnimationFrame(settleFrame);
    },
  };
}
