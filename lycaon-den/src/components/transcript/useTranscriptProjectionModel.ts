import { createMemo } from "solid-js";
import type { Message, TurnClock, TurnLoad, WorkerTask } from "../../api/types.ts";
import { unreadSince } from "../../attention/unread-boundary.ts";
import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { measureSync } from "../../chat/stream/den-main-thread-perf.ts";
import {
  transcriptRowContentEstimate,
  type PresentedVisual,
  type TranscriptRowMetrics,
} from "../../chat/transcript/layout/transcript-row-content-estimate.ts";
import {
  transcriptRowContentRevision,
  transcriptRowHeightForScope,
  type TranscriptRowHeightKey,
} from "../../chat/transcript/layout/transcript-row-heights-persist.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { transcriptRowSeams } from "../../chat/transcript/layout/transcript-row-seams.ts";
import {
  transcriptRootRemPx,
  transcriptSeamPx,
  transcriptSpacing,
  type TranscriptSeam,
} from "../../chat/transcript/layout/transcript-spacing.ts";
import { reviewTurnsByAssistant } from "../../chat/transcript/presentation/review-turns.ts";
import { noteTranscriptRowWithdrawals } from "../../chat/transcript/presentation/row-withdrawal-watch.ts";
import type { TranscriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import {
  transcriptRowPresentation,
  transcriptRowPresentationEstimate,
} from "../../chat/transcript/presentation/transcript-row-presentation.ts";
import {
  isTranscriptTimeItem,
  withTranscriptTimeRows,
} from "../../chat/transcript/presentation/transcript-time-rows.ts";
import {
  createTranscriptDisplayProjector,
  retainTranscriptItemIdentity,
  transcriptItemsByKey,
} from "../../chat/transcript/projection/transcript-display-projection.ts";
import type {
  DisplayTranscriptItem,
  RawTranscriptItem,
} from "../../chat/transcript/projection/transcript-item-model.ts";
import { sameWireValue } from "../../chat/transcript/projection/wire-equal.ts";
import {
  buildCanonicalArtifactPlacement,
  isCanonicalArtifactEntry,
  visualPresentEntryKey,
} from "../../chat/visual/visual-artifact-canonical.ts";
import type { WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import { taskWorkerMatchesForTranscript } from "../../chat/worker/workers-model.ts";
import { isCatalogSpan, type ChatSpanBlock } from "../../chat/workflow/workflow-spans.ts";
import { messageTimesPref } from "../../settings/chat/chat-prefs.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";

type TranscriptItemsModel = {
  items: DisplayTranscriptItem[];
  spanIndexByItemKey: ReadonlyMap<string, number>;
};

/** Memo equality for per-row lookup tables: readers see a change only when an entry moved. */
function sameMapEntries<K, V>(
  before: ReadonlyMap<K, V> | undefined,
  after: ReadonlyMap<K, V>,
): boolean {
  if (!before || before.size !== after.size) return false;
  for (const [key, value] of after) {
    if (!before.has(key) || before.get(key) !== value) return false;
  }
  return true;
}

function resolveExploreLegId(workers: readonly WorkerTask[]): string | undefined {
  for (let i = workers.length - 1; i >= 0; i--) {
    const leg = workers[i]?.leg_id?.trim();
    if (leg) return leg;
  }
  return undefined;
}

export type UseTranscriptProjectionModelOptions = {
  layout: () => TranscriptLayout;
  messages: () => Message[];
  pendingSends: () => readonly PendingSend[] | undefined;
  sessionId: () => string | null | undefined;
  projectId: () => string | undefined;
  workers: () => WorkerTask[];
  workerTranscripts: () => WorkerTranscriptCache | undefined;
  transcriptItems: () => RawTranscriptItem[] | undefined;
  transcriptSpans: () => ChatSpanBlock[] | undefined;
  visibleTurnActive: () => boolean;
  turnClocks: () => Readonly<Record<string, TurnClock>> | undefined;
  turnLoads: () => Readonly<Record<string, readonly TurnLoad[]>> | undefined;
  hasMoreBefore: () => boolean | undefined;
  checkpointAppStore: () => AppStore | undefined;
  exploreContext: () => CitationExploreContext | undefined;
  disclosureOpen: (key: TranscriptDisclosureKey) => boolean;
  rowMetrics: () => TranscriptRowMetrics | null;
  rowGeometry: () => string | undefined;
};

export function useTranscriptProjectionModel(options: UseTranscriptProjectionModelOptions) {
  const projectTranscript = createTranscriptDisplayProjector();
  const transcriptItemsModel = createMemo((): TranscriptItemsModel => {
    const common = {
      layout: options.layout(),
    };
    return measureSync(
      "transcript.rebuild",
      () => {
        const spanIndexByItemKey = new Map<string, number>();
        const transcriptSpans = options.transcriptSpans();
        const transcriptItems = options.transcriptItems();
        const messages = options.messages();
        const spans =
          transcriptSpans?.map((span) => span.items) ??
          (transcriptItems ? [transcriptItems] : undefined);
        const projected = projectTranscript(messages, spans, {
          ...common,
          verboseMode: verboseModePref(),
          turnLoads: options.turnLoads(),
        });
        const items: DisplayTranscriptItem[] = [];
        for (const [spanIndex, spanItems] of projected.entries()) {
          for (const item of spanItems) {
            if (transcriptSpans) spanIndexByItemKey.set(item.key, spanIndex);
            items.push(item);
          }
        }
        noteTranscriptRowWithdrawals(
          options.sessionId() ?? "chat",
          items,
          new Set(messages.map((message) => message.id)),
        );
        return { items, spanIndexByItemKey };
      },
      { msgs: options.messages().length, layout: options.layout() },
    );
  });

  const messageById = createMemo(
    () => new Map(options.messages().map((message) => [message.id, message])),
  );

  const timedItemsModel = createMemo(
    (previous: TranscriptItemsModel | undefined): TranscriptItemsModel => {
      const base = transcriptItemsModel();
      if (options.layout() !== "chat") return base;
      const hasMoreBefore =
        options.hasMoreBefore() ??
        options.checkpointAppStore()?.state.transcript.hasMoreBefore ??
        false;
      // Time rows are rebuilt each pass; an unchanged one keeps its identity like every other row.
      const timed = retainTranscriptItemIdentity(
        previous ? transcriptItemsByKey(previous.items) : new Map(),
        withTranscriptTimeRows(base.items, {
          messageById: messageById(),
          pendingSends: options.pendingSends(),
          turnClocks: options.turnClocks() ?? {},
          unreadSince: unreadSince(options.sessionId()),
          hasMoreBefore,
          sessionLive: options.visibleTurnActive(),
          dayKey: viewerCalendarTimeFormat().dayKey,
        }),
      );
      const transcriptSpans = options.transcriptSpans();
      if (!transcriptSpans) {
        return { items: timed, spanIndexByItemKey: base.spanIndexByItemKey };
      }
      // Markers join the next row's span; tails join the previous row's.
      const spanIndexByItemKey = new Map(base.spanIndexByItemKey);
      let pendingMarkers: string[] = [];
      let previousSpan: number | undefined;
      for (const item of timed) {
        if (item.kind === "turn_tail") {
          const span =
            previousSpan ?? base.spanIndexByItemKey.get(item.anchorMessageId);
          if (span !== undefined) spanIndexByItemKey.set(item.key, span);
          continue;
        }
        if (isTranscriptTimeItem(item)) {
          pendingMarkers.push(item.key);
          continue;
        }
        const span =
          base.spanIndexByItemKey.get(item.key) ??
          (item.kind === "pending_user" && transcriptSpans.length > 0
            ? transcriptSpans.length - 1
            : undefined);
        if (span !== undefined) {
          spanIndexByItemKey.set(item.key, span);
          for (const key of pendingMarkers) spanIndexByItemKey.set(key, span);
          previousSpan = span;
          pendingMarkers = [];
        }
      }
      if (pendingMarkers.length > 0) {
        const fallbackSpan =
          previousSpan ??
          (transcriptSpans.length > 0 ? transcriptSpans.length - 1 : 0);
        for (const key of pendingMarkers) {
          spanIndexByItemKey.set(key, fallbackSpan);
        }
      }
      return { items: timed, spanIndexByItemKey };
    },
  );

  const items = () => timedItemsModel().items;
  const spanIndexByItemKey = () => timedItemsModel().spanIndexByItemKey;

  // Each terminal assistant row's reviewable turn, resolved once for the whole transcript.
  const reviewTurns = createMemo(
    () =>
      reviewTurnsByAssistant(
        items(),
        messageById(),
        options.visibleTurnActive(),
      ),
    undefined,
    { equals: sameMapEntries },
  );

  // Each row's day, from the day label above it.
  const rowDays = createMemo(() => {
    const days = new Map<string, { at: number; labelKey: string }>();
    let current: { at: number; labelKey: string } | undefined;
    let labels = 0;
    for (const item of items()) {
      if (item.kind === "time_marker" && item.variant === "day") {
        current = { at: item.at, labelKey: item.key };
        labels += 1;
      }
      if (current) days.set(item.key, current);
    }
    return { days, labels };
  });

  // Keys identify rows while virtualizer order settles.
  const itemByKey = createMemo(() => {
    const map = new Map<string, DisplayTranscriptItem>();
    for (const item of items()) map.set(item.key, item);
    return map;
  });

  // A catalog span's edges are seams, so rows carry their run identity.
  const catalogSpanKeyOf = (itemKey: string): string | undefined => {
    const spans = options.transcriptSpans();
    if (!spans) return undefined;
    const index = spanIndexByItemKey().get(itemKey);
    if (index === undefined) return undefined;
    const span = spans[index];
    return span && isCatalogSpan(span) ? span.runId || `span-${index}` : undefined;
  };

  const continuationKeys = createMemo(
    () => {
      const keys = new Set<string>();
      for (const message of options.messages()) {
        if (message.kind === "user_continuation") keys.add(message.id);
      }
      return keys;
    },
    undefined,
    {
      equals: (before, after) =>
        before?.size === after.size && [...after].every((key) => before.has(key)),
    },
  );

  const rowSeams = createMemo(
    () =>
      transcriptRowSeams(items(), {
        spanKeyOf: catalogSpanKeyOf,
        continuation: (key) => continuationKeys().has(key),
      }),
    undefined,
    { equals: sameMapEntries },
  );

  const seamForItem = (key: string): TranscriptSeam | undefined =>
    rowSeams().get(key);

  // A seam belongs to the row beneath it, so measured heights include it.
  const rowInsetsByItemKey = createMemo(
    () => {
      const spacing = transcriptSpacing();
      const remPx = transcriptRootRemPx();
      const insets = new Map<string, number>();
      for (const [key, seam] of rowSeams()) {
        insets.set(key, transcriptSeamPx(seam, spacing, remPx));
      }
      return insets;
    },
    undefined,
    { equals: sameMapEntries },
  );

  const rowInsetPx = (key: string) => rowInsetsByItemKey().get(key) ?? 0;

  const taskMatches = createMemo(
    () => {
      const sid = options.sessionId()?.trim();
      if (!sid) return new Map<string, WorkerTask>();
      return taskWorkerMatchesForTranscript(items(), options.workers(), sid);
    },
    undefined,
    { equals: sameMapEntries },
  );

  const exploreContext = createMemo(
    (): CitationExploreContext => ({
      ...options.exploreContext(),
      sessionId:
        options.sessionId()?.trim() || options.exploreContext()?.sessionId,
      legId:
        options.exploreContext()?.legId ?? resolveExploreLegId(options.workers()),
    }),
    undefined,
    { equals: sameWireValue },
  );

  const rowHeightScope = () => {
    const projectId = options.projectId()?.trim();
    const sessionId = options.sessionId()?.trim();
    return projectId && sessionId ? { projectId, sessionId } : undefined;
  };

  const workerMetaMessages = createMemo((): Message[] => {
    const extra = options.workerTranscripts();
    if (!extra) return [];
    const out: Message[] = [];
    for (const entry of Object.values(extra)) {
      if (entry.rows.length) out.push(...entry.rows);
    }
    return out;
  });

  const artifactPlacements = createMemo(() =>
    buildCanonicalArtifactPlacement(options.messages(), {
      metaMessages: workerMetaMessages(),
    }),
  );

  /** Artifact dimensions and reference chips used for row estimates. */
  const presentedVisualsFor = (
    item: DisplayTranscriptItem,
  ): PresentedVisual[] => {
    if (item.kind !== "assistant" && item.kind !== "user") return [];
    const fromWire = messageById().get(item.key)?.artifact_ids ?? [];
    const ids =
      item.kind === "user" && fromWire.length === 0
        ? (item.artifactIds ?? [])
        : fromWire;
    const placements = artifactPlacements();
    return ids
      .map((id) => id.trim())
      .filter(Boolean)
      .map((id): PresentedVisual => {
        if (
          !isCanonicalArtifactEntry(
            placements,
            id,
            visualPresentEntryKey(item.key, id),
          )
        ) {
          return { kind: "reference" };
        }
        const meta = placements.meta(id);
        return meta?.width && meta.height
          ? { kind: "image", width: meta.width, height: meta.height }
          : { kind: "unsized" };
      });
  };

  const contentEstimates = new WeakMap<
    DisplayTranscriptItem,
    { metrics: TranscriptRowMetrics; visuals: string; height: number | undefined }
  >();
  const contentRowEstimate = (
    item: DisplayTranscriptItem,
  ): number | undefined => {
    const metrics = options.rowMetrics();
    const visuals = presentedVisualsFor(item);
    const visualKey = JSON.stringify(visuals);
    const previous = contentEstimates.get(item);
    let estimate =
      previous && previous.metrics === metrics && previous.visuals === visualKey
        ? previous.height
        : undefined;
    if (
      metrics &&
      (!previous || previous.metrics !== metrics || previous.visuals !== visualKey)
    ) {
      estimate = transcriptRowContentEstimate(item, metrics, visuals);
      contentEstimates.set(item, {
        metrics,
        visuals: visualKey,
        height: estimate,
      });
    }
    return estimate !== undefined &&
      (item.kind === "user" || item.kind === "pending_user") &&
      messageTimesPref() === "always"
      ? estimate + 16 + transcriptSpacing().messageTimeGap * transcriptRootRemPx()
      : estimate;
  };

  // A time under the bubble changes the user row's height.
  const rowPresentation = (item: DisplayTranscriptItem): string => {
    const presentation = transcriptRowPresentation(item, options.disclosureOpen);
    return (item.kind === "user" || item.kind === "pending_user") &&
      messageTimesPref() === "always"
      ? `${presentation}+time-below`
      : presentation;
  };

  /** A remembered height is reused only for the same content, geometry, and presentation. */
  const rowHeightKey = (
    item: DisplayTranscriptItem,
  ): TranscriptRowHeightKey | undefined => {
    const geometry = options.rowGeometry();
    return geometry
      ? {
          itemKey: item.key,
          revision: transcriptRowContentRevision(item),
          variant: `${geometry}:${rowPresentation(item)}`,
        }
      : undefined;
  };

  const restoredRowEstimate = (
    item: DisplayTranscriptItem,
  ): number | undefined => {
    if (item.kind === "pending_user") return undefined;
    const scope = rowHeightScope();
    const key = rowHeightKey(item);
    return scope && key ? transcriptRowHeightForScope(scope, key) : undefined;
  };

  const estimatedContentHeight = (
    item: DisplayTranscriptItem,
  ): number | undefined =>
    restoredRowEstimate(item) ??
    transcriptRowPresentationEstimate(item, options.disclosureOpen) ??
    contentRowEstimate(item);

  return {
    items,
    spanIndexByItemKey,
    messageById,
    reviewTurns,
    rowDays,
    itemByKey,
    seamForItem,
    rowInsetsByItemKey,
    rowInsetPx,
    taskMatches,
    exploreContext,
    rowHeightScope,
    artifactPlacements,
    rowHeightKey,
    estimatedContentHeight,
  };
}
