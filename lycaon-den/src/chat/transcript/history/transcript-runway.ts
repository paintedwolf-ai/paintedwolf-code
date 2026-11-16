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

/** Unloaded history and the measured geometry of the resident window. */
export type TranscriptRunwayObservation = {
  unloaded: TranscriptUnloadedOrds;
  measuredHeightPx: number;
  measuredOrdSpan: number;
};

/** A target scroll offset inside an unloaded runway, frozen until the landed page mounts. */
export type TranscriptRunwayLanding = {
  side: "before" | "after";
  /** Pixels from the start of the target runway to the requested reading offset. */
  offsetIntoRunwayPx: number;
  /** Runway height when the seek was issued. */
  runwayPx: number;
};

type MeasurableRow = {
  key: string | number;
  kind?: string;
  entries?: readonly {
    kind: string;
    key?: string;
    part?: {
      assistantMessageId?: string;
      messageId?: string;
      argsMessageId?: string;
    };
  }[];
  parts?: readonly {
    assistantMessageId?: string;
    messageId?: string;
    argsMessageId?: string;
  }[];
  part?: {
    assistantMessageId?: string;
    messageId?: string;
    argsMessageId?: string;
  };
};

const NON_ORDINAL_ROW_KINDS: ReadonlySet<string> = new Set([
  "time_marker",
  "unread_marker",
  "turn_tail",
  "pending_user",
  "file_edit",
  "worker_file_edit",
  "checkpoint",
  "turn_load",
]);

/** Host message ordinals represented by a single projected row. */
export function transcriptRowOrdSpan(
  item: MeasurableRow,
  proseKeys?: ReadonlySet<string | number>,
): number {
  const kind = item.kind;
  if (!kind) return 1;
  if (NON_ORDINAL_ROW_KINDS.has(kind)) return 0;
  if (kind === "activity_span" && item.entries) {
    const ids = new Set<string>();
    let workEntries = 0;
    for (const entry of item.entries) {
      if (entry.kind === "turn_load") continue;
      workEntries += 1;
      if (entry.kind === "index_warming") {
        const key = entry.key?.trim();
        if (key) ids.add(key);
        continue;
      }
      const part = entry.part;
      if (!part) continue;
      const msgId = part.messageId?.trim();
      if (msgId) ids.add(msgId);
      const asstId = part.assistantMessageId?.trim();
      if (asstId && !proseKeys?.has(asstId)) ids.add(asstId);
      const argsId = part.argsMessageId?.trim();
      if (argsId) ids.add(argsId);
    }
    return Math.max(1, ids.size, workEntries);
  }
  if (kind === "worker_group" && item.parts) {
    const ids = new Set<string>();
    for (const part of item.parts) {
      const msgId = part.messageId?.trim();
      if (msgId) ids.add(msgId);
      const asstId = part.assistantMessageId?.trim();
      if (asstId && !proseKeys?.has(asstId)) ids.add(asstId);
      const argsId = part.argsMessageId?.trim();
      if (argsId) ids.add(argsId);
    }
    return Math.max(1, ids.size, item.parts.length);
  }
  if (kind === "tool" && item.part) {
    const ids = new Set<string>();
    const msgId = item.part.messageId?.trim();
    if (msgId) ids.add(msgId);
    const asstId = item.part.assistantMessageId?.trim();
    if (asstId && !proseKeys?.has(asstId)) ids.add(asstId);
    const argsId = item.part.argsMessageId?.trim();
    if (argsId) ids.add(argsId);
    return Math.max(1, ids.size);
  }
  return 1;
}

/** Sums total resident height alongside the height and ordinal span of measured or persisted rows. */
export function measureTranscriptResidentRows<T extends MeasurableRow>(opts: {
  items: readonly T[];
  cachedSize: (key: string | number) => number | undefined;
  persistedSize?: (item: T) => number | undefined;
  estimatedSize: (item: T, index: number) => number;
}): {
  residentHeightPx: number;
  measuredHeightPx: number;
  measuredOrdSpan: number;
} {
  const count = opts.items.length;
  if (count === 0) {
    return {
      residentHeightPx: 0,
      measuredHeightPx: 0,
      measuredOrdSpan: 0,
    };
  }
  const proseKeys = new Set<string | number>();
  for (let i = 0; i < count; i += 1) {
    const item = opts.items[i]!;
    if (item.kind === "assistant" || item.kind === "draft") {
      proseKeys.add(item.key);
    }
  }
  let residentHeightPx = 0;
  let measuredHeightPx = 0;
  let measuredOrdSpan = 0;
  for (let i = 0; i < count; i += 1) {
    const item = opts.items[i]!;
    const known = opts.cachedSize(item.key) ?? opts.persistedSize?.(item);
    if (known !== undefined && known > 0) {
      residentHeightPx += known;
      const itemSpan = transcriptRowOrdSpan(item, proseKeys);
      if (itemSpan > 0) {
        measuredHeightPx += known;
        measuredOrdSpan += itemSpan;
      }
    } else {
      residentHeightPx += opts.estimatedSize(item, i);
    }
  }
  return {
    residentHeightPx,
    measuredHeightPx,
    measuredOrdSpan,
  };
}

/** Pure density from measured resident geometry, falling back to `prior` when no row is measured. */
export function transcriptRunwayDensity(
  obs: Pick<TranscriptRunwayObservation, "measuredHeightPx" | "measuredOrdSpan">,
  prior = DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
): number {
  const height = Number.isFinite(obs.measuredHeightPx) ? obs.measuredHeightPx : 0;
  const ords = Number.isFinite(obs.measuredOrdSpan) ? obs.measuredOrdSpan : 0;
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

export type TranscriptRunwayDensityCause =
  | "start_loaded"
  | "page_landed"
  | "first_measure";

export type TranscriptRunwayDensityChange = {
  fromPx: number;
  toPx: number;
  measuredOrds: number;
  beforePx: number;
  afterPx: number;
  cause: TranscriptRunwayDensityCause;
};

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
 * Stateful runway budget. Density refreshes on the first measured observation, whenever
 * `unloaded.before` changes (a page lands or is evicted above), or when `unloaded.before === 0`,
 * so pure resident re-measurements never resize `beforePx` and push the reading anchor.
 */
export function createTranscriptRunwayBudget(
  onDensityChange?: (change: TranscriptRunwayDensityChange) => void,
): TranscriptRunwayBudget {
  let state: TranscriptRunwayState = {
    beforePx: 0,
    afterPx: 0,
    densityPxPerOrd: DEFAULT_TRANSCRIPT_ORD_DENSITY_PX,
  };
  let previousUnloaded: TranscriptUnloadedOrds = { before: 0, after: 0 };
  let hasMeasured = false;
  let landing: TranscriptRunwayLanding | null = null;

  const observe = (obs: TranscriptRunwayObservation): TranscriptRunwayState => {
    const unloaded = {
      before: Math.max(0, Math.floor(obs.unloaded.before)),
      after: Math.max(0, Math.floor(obs.unloaded.after)),
    };
    const measuredOrds = Number.isFinite(obs.measuredOrdSpan)
      ? Math.max(0, obs.measuredOrdSpan)
      : 0;
    const firstMeasure = !hasMeasured && measuredOrds > 0;
    if (measuredOrds > 0) hasMeasured = true;

    const canRefreshDensity =
      unloaded.before === 0 ||
      firstMeasure ||
      unloaded.before !== previousUnloaded.before;
    const densityPxPerOrd = canRefreshDensity
      ? transcriptRunwayDensity(obs, state.densityPxPerOrd)
      : state.densityPxPerOrd;

    let beforePx = unloaded.before === 0
      ? 0
      : unloaded.before !== previousUnloaded.before || firstMeasure || state.beforePx === 0
        ? transcriptRunwayHeight(unloaded.before, densityPxPerOrd)
        : state.beforePx;
    let afterPx = unloaded.after === 0
      ? 0
      : unloaded.after !== previousUnloaded.after || densityPxPerOrd !== state.densityPxPerOrd || state.afterPx === 0
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
    if (densityPxPerOrd !== state.densityPxPerOrd) {
      const cause: TranscriptRunwayDensityCause = firstMeasure
        ? "first_measure"
        : unloaded.before === 0
          ? "start_loaded"
          : "page_landed";
      onDensityChange?.({
        fromPx: state.densityPxPerOrd,
        toPx: densityPxPerOrd,
        measuredOrds,
        beforePx,
        afterPx,
        cause,
      });
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
