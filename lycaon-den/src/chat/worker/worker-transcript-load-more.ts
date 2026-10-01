import type { LycaonClient } from "../../api/client.ts";
import type { WorkerTask } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { observeScrollportOffset } from "../../platform/scrolling/scrollport-offset.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { LOAD_MORE_VIEWPORTS } from "../transcript/layout/transcript-load-more.ts";
import {
  DEFAULT_TRANSCRIPT_PAGE_LIMIT,
  transcriptPageQuery,
  transcriptPageRequest,
  transcriptPageRequestKey,
  type TranscriptPageEdge,
  type TranscriptWindow,
} from "../transcript/layout/transcript-window.ts";

/** Fetch one page at a worker transcript edge; false when the window did not change. */
export async function loadWorkerTranscriptPage(
  client: LycaonClient,
  appStore: AppStore,
  worker: WorkerTask,
  edge: TranscriptPageEdge,
): Promise<boolean> {
  const sessionId = worker.child_session_id?.trim();
  const entry = appStore.state.workerTranscripts[worker.id];
  const request = entry ? transcriptPageRequest(entry.window, edge) : undefined;
  if (!sessionId || !request) return false;
  const page = await client.listSessionMessages(sessionId, {
    workerId: worker.id,
    limit: DEFAULT_TRANSCRIPT_PAGE_LIMIT,
    ...transcriptPageQuery(request),
  });
  return appStore.actions.loadWorkerTranscriptPage(worker.id, request, page);
}

/** Resolves the next transcript edge to fetch based on reader position and resident range. */
export function workerTranscriptEdgeInReach(
  resident: TranscriptWindow,
  activity: { top: number; bottom: number },
  viewport: { top: number; bottom: number },
  opts: { fromAbove: boolean },
): TranscriptPageEdge | undefined {
  const reach = LOAD_MORE_VIEWPORTS * (viewport.bottom - viewport.top);
  if (resident.hasMoreBefore) {
    if (opts.fromAbove && activity.top >= viewport.top) return "start";
    if (activity.top < viewport.top && activity.top > viewport.top - reach) return "older";
  }
  if (resident.hasTailGap && activity.bottom < viewport.bottom + reach && activity.bottom > viewport.top) {
    return "newer";
  }
  return undefined;
}

/** Monitors scroll position to load additional worker transcript pages as edges near view. */
export function watchWorkerTranscriptLoadMore(opts: {
  viewport: HTMLElement;
  activity: () => HTMLElement | undefined;
  resident: () => TranscriptWindow | undefined;
  load: (edge: TranscriptPageEdge) => Promise<boolean>;
  onLoading: (edge: TranscriptPageEdge | undefined) => void;
}): { check: () => void; stop: () => void } {
  let stopped = false;
  let inFlight = false;
  let met = false;
  let stalled: string | undefined;

  const check = () => {
    if (stopped || inFlight) return;
    const activity = opts.activity();
    const resident = opts.resident();
    if (!activity || !resident || (resident.tail.length === 0 && Object.keys(resident.pages).length === 0)) return;
    const fromAbove = !met;
    met = true;
    const edge = workerTranscriptEdgeInReach(
      resident,
      activity.getBoundingClientRect(),
      opts.viewport.getBoundingClientRect(),
      { fromAbove },
    );
    const request = edge ? transcriptPageRequest(resident, edge) : undefined;
    if (!edge || !request) return;
    const key = transcriptPageRequestKey(request);
    if (key === stalled) return;
    inFlight = true;
    opts.onLoading(edge);
    void opts.load(edge)
      .then((changed) => {
        if (!changed) stalled = key;
      })
      .catch(() => undefined)
      .finally(() => {
        inFlight = false;
        if (stopped) return;
        opts.onLoading(undefined);
        // Continue once the new rows have laid out.
        scheduleScrollportFrame(opts.viewport, "observe", check);
      });
  };

  const stopScroll = observeScrollportOffset(opts.viewport, () =>
    scheduleScrollportFrame(opts.viewport, "observe", check),
  );
  return {
    check,
    stop: () => {
      stopped = true;
      stopScroll();
      cancelScrollportFrame(opts.viewport, check);
    },
  };
}
