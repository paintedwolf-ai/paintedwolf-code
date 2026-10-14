import type { TranscriptOrdSpan } from "../layout/transcript-window.ts";

/** Fallback pixels per message ordinal before any resident rows have sizes. */
export const DEFAULT_TRANSCRIPT_ORD_DENSITY_PX = 84;

const MIN_ORD_DENSITY_PX = 16;
const MAX_ORD_DENSITY_PX = 2_400;

/** Unloaded ordinal spans on each side of the presented resident window. */
export type TranscriptUnloadedOrds = {
  before: number;
  after: number;
};

/** Space reserved in the virtualizer for unloaded history on each side of the resident window. */
export type TranscriptRunwayState = {
  beforePx: number;
  afterPx: number;
  densityPxPerOrd: number;
};

/** Facts the virtualizer knows about the resident window and the scrollport. */
export type TranscriptRunwayObservation = {
  unloaded: TranscriptUnloadedOrds;
  residentRowCount: number;
  residentOrdSpan: number;
  residentHeightPx: number;
  scrollOffsetPx: number;
  viewportHeightPx: number;
  following: boolean;
};

/** A target scroll offset inside an unloaded runway, frozen until the landed page mounts. */
export type TranscriptRunwayLanding = {
  side: "before" | "after";
  /** Pixels from the start of the target runway to the requested reading offset. */
  offsetIntoRunwayPx: number;
  /** Runway height when the seek was issued. */
  runwayPx: number;
};

/** Pure density from resident geometry, falling back to `prior` when the window is empty. */
export function transcriptRunwayDensity(
  obs: Pick<
    TranscriptRunwayObservation,
    "residentRowCount" | "residentOrdSpan" | "residentHeightPx"
  >,
  prior = DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
): number {
  const height = Number.isFinite(obs.residentHeightPx) ? obs.residentHeightPx : 0;
  const ords = Math.max(obs.residentOrdSpan, obs.residentRowCount);
  if (height <= 0 || ords <= 0) return prior;
  const raw = height / ords;
  return Math.min(MAX_ORD_DENSITY_PX, Math.max(MIN_ORD_DENSITY_PX, raw));
}

export function transcriptRunwayHeight(unloadedOrds: number, densityPxPerOrd: number): number {
  if (!Number.isFinite(unloadedOrds) || unloadedOrds <= 0) return 0;
  const density = Number.isFinite(densityPxPerOrd) && densityPxPerOrd > 0
    ? densityPxPerOrd
    : DEFAULT_TRANSCRIPT_ORD_DENSITY_PX;
  return Math.round(unloadedOrds * density);
}

/**
 * Ordinal at a scroll offset that falls inside the older or newer unloaded runway. Returns
 * undefined when the offset is inside the resident window or the side has no unloaded span.
 */
export function transcriptRunwayOrd(opts: {
  offsetPx: number;
  originPx: number;
  runway: TranscriptRunwayState;
  residentHeightPx: number;
  residentSpan: TranscriptOrdSpan | undefined;
  bounds: TranscriptOrdSpan | undefined;
  unloaded: TranscriptUnloadedOrds;
}): number | undefined {
  const { offsetPx, originPx, runway, residentHeightPx, residentSpan, bounds, unloaded } = opts;
  if (!residentSpan || !bounds) return undefined;
  const local = Math.max(0, offsetPx - originPx);
  if (unloaded.before > 0 && runway.beforePx > 0 && local < runway.beforePx) {
    const ratio = Math.min(1, Math.max(0, local / runway.beforePx));
    const span = Math.max(0, residentSpan.firstOrd - 1 - bounds.firstOrd);
    return Math.round(bounds.firstOrd + ratio * span);
  }
  const afterStart = runway.beforePx + Math.max(0, residentHeightPx);
  if (unloaded.after > 0 && runway.afterPx > 0 && local > afterStart) {
    const into = local - afterStart;
    const ratio = Math.min(1, Math.max(0, into / runway.afterPx));
    const firstAfter = residentSpan.lastOrd + 1;
    const span = Math.max(0, bounds.lastOrd - firstAfter);
    return Math.round(firstAfter + ratio * span);
  }
  return undefined;
}

export type TranscriptRunwayBudget = {
  /** Updates runway heights from the latest resident observation. */
  observe: (obs: TranscriptRunwayObservation) => TranscriptRunwayState;
  /** Freezes the runway side a scrollbar seek targets until the sought page lands. */
  prepareLanding: (
    targetOffsetPx: number,
    originPx: number,
    residentHeightPx: number,
  ) => void;
  state: () => TranscriptRunwayState;
};

/**
 * Stateful runway budget. Density refreshes only when no unloaded runway is above the reader
 * (at the live tail before any older runway exists, or when `before === 0`), so row
 * measurements inside the resident window never resize `beforePx` and push the reading anchor.
 */
export function createTranscriptRunwayBudget(): TranscriptRunwayBudget {
  let state: TranscriptRunwayState = {
    beforePx: 0,
    afterPx: 0,
    densityPxPerOrd: DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
  };
  let previousUnloaded: TranscriptUnloadedOrds = { before: 0, after: 0 };
  let landing: TranscriptRunwayLanding | null = null;

  const observe = (obs: TranscriptRunwayObservation): TranscriptRunwayState => {
    const unloaded = {
      before: Math.max(0, Math.floor(obs.unloaded.before)),
      after: Math.max(0, Math.floor(obs.unloaded.after)),
    };
    // Density can change freely before an older runway exists, or once the start of history is loaded.
    const canRefreshDensity =
      (state.beforePx === 0 && unloaded.before === 0) ||
      unloaded.before === 0 ||
      (obs.following && previousUnloaded.before === 0);
    const densityPxPerOrd = canRefreshDensity
      ? transcriptRunwayDensity(obs, state.densityPxPerOrd)
      : state.densityPxPerOrd;

    let beforePx = unloaded.before === 0
      ? 0
      : unloaded.before !== previousUnloaded.before || state.beforePx === 0
        ? transcriptRunwayHeight(unloaded.before, densityPxPerOrd)
        : state.beforePx;
    let afterPx = unloaded.after === 0
      ? 0
      : unloaded.after !== previousUnloaded.after || state.afterPx === 0
        ? transcriptRunwayHeight(unloaded.after, densityPxPerOrd)
        : state.afterPx;

    if (landing) {
      if (landing.side === "before") {
        if (unloaded.before !== previousUnloaded.before || unloaded.before === 0) {
          landing = null;
        } else {
          beforePx = landing.runwayPx;
        }
      } else if (unloaded.after !== previousUnloaded.after || unloaded.after === 0) {
        landing = null;
      } else {
        afterPx = landing.runwayPx;
      }
    }

    previousUnloaded = unloaded;
    if (
      state.beforePx === beforePx &&
      state.afterPx === afterPx &&
      state.densityPxPerOrd === densityPxPerOrd
    ) {
      return state;
    }
    state = { beforePx, afterPx, densityPxPerOrd };
    return state;
  };

  const prepareLanding = (
    targetOffsetPx: number,
    originPx: number,
    residentHeightPx: number,
  ): void => {
    const local = Math.max(0, targetOffsetPx - originPx);
    if (state.beforePx > 0 && local < state.beforePx) {
      landing = {
        side: "before",
        offsetIntoRunwayPx: local,
        runwayPx: state.beforePx,
      };
      return;
    }
    const afterStart = state.beforePx + Math.max(0, residentHeightPx);
    if (state.afterPx > 0 && local > afterStart) {
      landing = {
        side: "after",
        offsetIntoRunwayPx: local - afterStart,
        runwayPx: state.afterPx,
      };
    }
  };

  return {
    observe,
    prepareLanding,
    state: () => state,
  };
}
