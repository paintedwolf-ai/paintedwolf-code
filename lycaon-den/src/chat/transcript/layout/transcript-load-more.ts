import { scrollportMotionForViewport, scrollportMotionContaining } from "../../../platform/scrolling/scrollport-motion.ts";
import { TranscriptPageIntent } from "./transcript-page-intent.ts";
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
  if (appStore.state.transcriptSessionId !== sessionId) return false;
  const epoch = appStore.state.sessionViewEpoch;
  const request = transcriptPageRequest(appStore.state.transcript, edge);
  if (!request) return false;
  const page = await client.listSessionMessages(sessionId, {
    limit: DEFAULT_TRANSCRIPT_PAGE_LIMIT,
    ...transcriptPageQuery(request),
  });
  if (appStore.state.transcriptSessionId !== sessionId || appStore.state.sessionViewEpoch !== epoch) return false;
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
  const intent = new TranscriptPageIntent();
  let settleFrame: number | undefined;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let previousOffset = streamTarget.scrollTop;
  const motion = scrollportMotionForViewport(streamTarget);

  const edgeInReach = () => {
    const reach = streamTarget.clientHeight * LOAD_MORE_VIEWPORTS;
    const transcript = opts.appStore.state.transcript;
    const belowReader = streamTarget.scrollHeight - streamTarget.scrollTop - streamTarget.clientHeight;
    return intent.edge(
      transcript.hasMoreBefore && streamTarget.scrollTop <= reach,
      transcript.hasTailGap && belowReader <= reach,
    );
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
    const delay = intent.retryDelay(key, performance.now());
    if (delay === undefined) return;
    if (delay > 0) {
      retryTimer ??= setTimeout(() => { retryTimer = undefined; maybeLoad(); }, delay);
      return;
    }
    inFlight = true;
    void loadTranscriptPage(client, opts.appStore, sessionId, edge)
      .then((added) => {
        if (!added) {
          intent.failed(key, performance.now());
          return;
        }
        intent.advanced();
        if (stopped) return;
        // Continue after the reading anchor settles.
        settleFrame = requestAnimationFrame(maybeLoad);
      })
      .catch(() => intent.failed(key, performance.now()))
      .finally(() => {
        inFlight = false;
      });
  };

  const onWheel = (event: WheelEvent) => {
    if (motion && event.target instanceof Element && scrollportMotionContaining(event.target) !== motion) return;
    if (event.ctrlKey || event.deltaY === 0 || Math.abs(event.deltaX) > Math.abs(event.deltaY)) return;
    intent.noteInput(event.deltaY < 0 ? "older" : "newer");
    maybeLoad();
  };
  const stopCommits = motion?.subscribeCommits((source, position) => {
    if ((source === "thumb_drag" || source === "track_click") && position.top !== previousOffset) {
      intent.noteInput(position.top < previousOffset ? "older" : "newer");
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
  streamTarget.addEventListener("wheel", onWheel, { passive: true });
  queueMicrotask(maybeLoad);
  return () => {
    stopped = true;
    unsubscribe();
    stopCommits?.();
    streamTarget.removeEventListener("wheel", onWheel);
    if (retryTimer !== undefined) clearTimeout(retryTimer);
    if (settleFrame !== undefined) cancelAnimationFrame(settleFrame);
  };
}
