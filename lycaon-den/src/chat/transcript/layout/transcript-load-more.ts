import type { LycaonClient } from "../../../api/client.ts";
import type { Message } from "../../../api/types.ts";
import type { AppStore } from "../../../store/app-state-model.ts";
import {
  subscribeStreamScroll,
} from "../../stream/stream-scroll.ts";
import type { TranscriptRevealAnchor } from "../presentation/transcript-reveal-target.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  contiguousWindowRows,
  transcriptPageQuery,
  transcriptPageRequest,
  transcriptPageRequestKey,
  type TranscriptPageEdge,
} from "./transcript-window.ts";

/** History loads while the reader is within this many viewports of a resident edge. */
export const LOAD_MORE_VIEWPORTS = 2;

/** Fetches one page at a resident edge; returns true when the resident edge advanced. */
export async function loadTranscriptPage(
  client: LycaonClient,
  appStore: AppStore,
  sessionId: string,
  edge: Exclude<TranscriptPageEdge, "start">,
): Promise<boolean> {
  const request = transcriptPageRequest(appStore.state.transcript, edge);
  if (!request) return false;
  const page = await client.listSessionMessages(sessionId, {
    limit: DEFAULT_TRANSCRIPT_PAGE_LIMIT,
    ...transcriptPageQuery(request),
  });
  if (!appStore.actions.loadTranscriptPage(request, page) || page.messages.length === 0) return false;
  const next = transcriptPageRequest(appStore.state.transcript, edge);
  return !next || transcriptPageRequestKey(next) !== transcriptPageRequestKey(request);
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
  if (resident()) return true;
  // A tail gap requires a contiguous scan from the tail.
  if (appStore.state.transcript.hasTailGap) {
    appStore.actions.resetOlderTranscriptPages();
    if (resident()) return true;
  }
  while (await loadTranscriptPage(client, appStore, sessionId, "older")) {
    if (resident()) return true;
  }
  return resident();
}

/**
 * Load history while the reader nears a resident edge: older pages near the top, and,
 * across a tail gap, the pages that lead back to the live tail near the bottom.
 */
export function watchTranscriptLoadMore(
  streamTarget: HTMLElement,
  opts: {
    client: () => LycaonClient | undefined;
    appStore: AppStore;
    sessionId: () => string | undefined;
  },
): () => void {
  let inFlight = false;
  let stopped = false;
  let stalled: string | undefined;
  let settleFrame: number | undefined;

  const edgeInReach = (): Exclude<TranscriptPageEdge, "start"> | undefined => {
    const reach = streamTarget.clientHeight * LOAD_MORE_VIEWPORTS;
    const transcript = opts.appStore.state.transcript;
    if (transcript.hasMoreBefore && streamTarget.scrollTop <= reach) return "older";
    const belowReader = streamTarget.scrollHeight - streamTarget.scrollTop - streamTarget.clientHeight;
    if (transcript.hasTailGap && belowReader <= reach) return "newer";
    return undefined;
  };

  const maybeLoad = () => {
    if (stopped || inFlight) return;
    const sessionId = opts.sessionId()?.trim();
    const client = opts.client();
    if (!sessionId || !client) return;
    const edge = edgeInReach();
    const request = edge ? transcriptPageRequest(opts.appStore.state.transcript, edge) : undefined;
    if (!edge || !request) return;
    const key = transcriptPageRequestKey(request);
    // Wait for the window to move if the last request added nothing.
    if (key === stalled) return;
    inFlight = true;
    void loadTranscriptPage(client, opts.appStore, sessionId, edge)
      .then((added) => {
        if (!added) {
          stalled = key;
          return;
        }
        if (stopped) return;
        // Continue after the reading anchor settles.
        settleFrame = requestAnimationFrame(maybeLoad);
      })
      .catch(() => undefined)
      .finally(() => {
        inFlight = false;
      });
  };

  const unsubscribe = subscribeStreamScroll(streamTarget, maybeLoad);
  queueMicrotask(maybeLoad);
  return () => {
    stopped = true;
    unsubscribe();
    if (settleFrame !== undefined) cancelAnimationFrame(settleFrame);
  };
}
