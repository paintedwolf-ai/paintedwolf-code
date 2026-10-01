import { createTranscriptKeyboard } from "../../chat/transcript/presentation/transcript-keyboard.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import {
  createTranscriptDisclosureStore,
  TranscriptDisclosureProvider,
} from "../../chat/transcript/presentation/disclosure-state.tsx";
import {
  parseTranscriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { createTranscriptGeometry } from "../../chat/transcript/layout/transcript-geometry.ts";
import {
  transcriptRootRemPx,
  transcriptSeamPx,
  transcriptSpacing,
  type TranscriptSeam,
} from "../../chat/transcript/layout/transcript-spacing.ts";
import { transcriptRowSeams } from "../../chat/transcript/layout/transcript-row-seams.ts";
import { noteTranscriptRowWithdrawals } from "../../chat/transcript/presentation/row-withdrawal-watch.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { For, Show, createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { KeyedIndex } from "../keyed-index.tsx";
import { VirtualCardList } from "../primitives/VirtualCardList.tsx";
import type { WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import { searchWorkerItem, workerItemHasExternalFind } from "../../chat/worker/worker-item-find.ts";
import { TOOL_CONTENT_REVEAL_EVENT } from "../../chat/tool/tool-part-find.ts";
import type { SlotContent } from "../../ui/slot-content.ts";
import type { Message, TurnClock, TurnLoad, WorkerTask } from "../../api/types.ts";
import { verboseModePref } from "../../settings/system/debug-prefs.ts";
import { SourceContextProvider } from "../source/annotations/source-context.ts";
import {
  createTranscriptDisplayProjector,
  retainTranscriptItemIdentity,
  transcriptItemsByKey,
} from "../../chat/transcript/projection/transcript-display-projection.ts";
import { reviewTurnsByAssistant } from "../../chat/transcript/presentation/review-turns.ts";
import { sameWireValue } from "../../chat/transcript/projection/wire-equal.ts";
import { type DisplayTranscriptItem, type RawTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import {
  recordTranscriptRowHeight,
  transcriptRowHeightForScope,
} from "../../chat/transcript/layout/transcript-row-heights-persist.ts";
import {
  transcriptRowPresentationEstimate,
  transcriptRowPresentation,
} from "../../chat/transcript/presentation/transcript-row-presentation.ts";
import {
  transcriptRowContentEstimate,
  type PresentedVisual,
  type TranscriptRowMetrics,
} from "../../chat/transcript/layout/transcript-row-content-estimate.ts";
import {
  buildCanonicalArtifactPlacement,
  isCanonicalArtifactEntry,
  visualPresentEntryKey,
} from "../../chat/visual/visual-artifact-canonical.ts";
import { measureSync } from "../../chat/stream/den-main-thread-perf.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import { taskWorkerMatchesForTranscript, type OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { ArtifactDedupProvider } from "../../chat/visual/artifact-dedup-context.tsx";
import type { ChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";
import {
  transcriptItemContainsMessage,
  transcriptItemToolCallIds,
} from "../../chat/transcript/projection/transcript-item-anchors.ts";
import {
  createTranscriptVirtualizer,
  transcriptOffsetForPosition,
  transcriptReadingPosition,
  transcriptRowEstimatedHeight,
  transcriptVirtualRunway,
} from "../../chat/transcript/layout/transcript-virtualizer.ts";
import { isTranscriptTimeItem, withTranscriptTimeRows } from "../../chat/transcript/presentation/transcript-time-rows.ts";
import { unreadSince } from "../../attention/unread-boundary.ts";
import { messageTimesPref } from "../../settings/chat/chat-prefs.ts";
import { viewerCalendarTimeFormat } from "../../time/calendar-time.ts";
import type { VirtualItem } from "@tanstack/solid-virtual";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import {
  ensureTranscriptRevealAnchorLoaded,
  loadTranscriptHistoryUntil,
  loadTranscriptPage,
} from "../../chat/transcript/layout/transcript-load-more.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { isShellLayoutUnstable } from "../../shell/shell-layout-busy.ts";
import { isCatalogSpan, type ChatSpanBlock } from "../../chat/workflow/workflow-spans.ts";
import type { PendingSend } from "../../chat/send/pending-sends.ts";
import { type MessageRecoveryHandlers } from "./UserBubble.tsx";
import { TranscriptItemRow } from "./TranscriptItemRow.tsx";

function resolveExploreLegId(workers: readonly WorkerTask[]): string | undefined {
  for (let i = workers.length - 1; i >= 0; i--) {
    const leg = workers[i]?.leg_id?.trim();
    if (leg) return leg;
  }
  return undefined;
}

type Props = {
  messages: Message[];
  pendingSends?: readonly PendingSend[];
  sessionId?: string | null;
  workers?: WorkerTask[];
  workerTranscripts?: WorkerTranscriptCache;
  /** The worker layout's row list, which scrolls with the drawer. */
  workerListRef?: (element: HTMLElement | undefined) => void;
  onOpenWorker?: (workerId: string, opts?: OpenWorkerOptions) => void;
  transcriptItems?: RawTranscriptItem[];
  transcriptSpans?: ChatSpanBlock[];
  visibleTurnActive?: boolean;
  layout?: TranscriptLayout;
  checkpointClient?: import("../../api/client.ts").LycaonClient | null;
  checkpointAppStore?: import("../../store/app-state-model.ts").AppStore;
  projectDir?: string;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  branchJobId?: string;
  exploreContext?: CitationExploreContext;
  onExploreInSearch?: (query: string) => void;
  blueprint?: ChatBlueprintState;
  onBlueprintRevise?: (blueprintPath: string, text: string) => void;
  activePendingPhaseId?: string | null;
  recovery?: MessageRecoveryHandlers;
  /** User turn clocks are keyed by opening message id. */
  turnClocks?: Readonly<Record<string, TurnClock>>;
  /** Decision-engine receipts keyed by opening message id; their rows join the turn's activity. */
  turnLoads?: Readonly<Record<string, readonly TurnLoad[]>>;
  /** Tail surfaces share the transcript row flow. */
  tail?: readonly TranscriptTailSlot[];
  /** True when earlier messages in history exist before these items. */
  hasMoreBefore?: boolean;
};

export type TranscriptTailSlot = SlotContent & {
  /** Slot identity stays stable when its content changes. */
  id: string;
};

type VirtualTranscriptSpanGroup = {
  key: string;
  rowKeys: string[];
  span?: ChatSpanBlock;
  index?: number;
};

type TranscriptItemsModel = {
  items: DisplayTranscriptItem[];
  spanIndexByItemKey: ReadonlyMap<string, number>;
};

/** Memo equality for per-row lookup tables: readers see a change only when an entry moved. */
function sameMapEntries<K, V>(before: ReadonlyMap<K, V> | undefined, after: ReadonlyMap<K, V>): boolean {
  if (!before || before.size !== after.size) return false;
  for (const [key, value] of after) {
    if (!before.has(key) || before.get(key) !== value) return false;
  }
  return true;
}

export function transcriptScrollHostElement(
  root: HTMLElement | null,
): HTMLElement | null {
  if (!root) return null;
  const host = root.closest(".den-chat-stream");
  if (!(host instanceof HTMLElement)) {
    if (!root.isConnected) return null;
    throw new Error("Chat transcript requires a scroll host.");
  }
  return host;
}

export function SessionTranscript(props: Props) {
  const workerDisclosures = createMemo(() => { void props.sessionId; void props.branchJobId; return createTranscriptDisclosureStore(); });
  const layout = () => props.layout ?? "chat";
  const workers = () => props.workers ?? [];
  const tailSlots = createMemo(() =>
    (props.tail ?? []).filter((slot) => slot.present()),
  );
  // A tail row opens its own reading unit below the last row; further ones sit with it.
  const tailSlotSeam = (id: string): TranscriptSeam | undefined => {
    if (tailSlots()[0]?.id !== id) return "row";
    return items().length > 0 ? "section" : undefined;
  };
  const viewport = useTranscriptViewport();
  const requireChatViewport = () => {
    if (!viewport) {
      throw new Error("Chat transcript requires a viewport controller.");
    }
    return viewport;
  };
  const disclosureOpen = (key: TranscriptDisclosureKey) =>
    layout() === "chat" ? requireChatViewport().disclosures.isOpen(key) : false;
  const [streamRoot, setStreamRoot] = createSignal<HTMLElement | null>(null);

  const projectTranscript = createTranscriptDisplayProjector();
  const transcriptItemsModel = createMemo((): TranscriptItemsModel => {
    const common = {
      layout: layout(),
    };
    return measureSync(
      "transcript.rebuild",
      () => {
        const spanIndexByItemKey = new Map<string, number>();
        const spans = props.transcriptSpans?.map((span) => span.items)
          ?? (props.transcriptItems ? [props.transcriptItems] : undefined);
        const projected = projectTranscript(props.messages, spans, {
          ...common, verboseMode: verboseModePref(), turnLoads: props.turnLoads,
        });
        const items: DisplayTranscriptItem[] = [];
        for (const [spanIndex, spanItems] of projected.entries()) {
          for (const item of spanItems) {
            if (props.transcriptSpans) spanIndexByItemKey.set(item.key, spanIndex);
            items.push(item);
          }
        }
        noteTranscriptRowWithdrawals(
          props.sessionId ?? "chat",
          items,
          new Set(props.messages.map((message) => message.id)),
        );
        return { items, spanIndexByItemKey };
      },
      { msgs: props.messages.length, layout: layout() },
    );
  });
  const messageById = createMemo(
    () => new Map(props.messages.map((message) => [message.id, message])),
  );
  const timedItemsModel = createMemo((previous: TranscriptItemsModel | undefined): TranscriptItemsModel => {
    const base = transcriptItemsModel();
    if (layout() !== "chat") return base;
    const hasMoreBefore = props.hasMoreBefore ?? props.checkpointAppStore?.state.transcript.hasMoreBefore ?? false;
    // Time rows are rebuilt each pass; an unchanged one keeps its identity like every other row.
    const timed = retainTranscriptItemIdentity(
      previous ? transcriptItemsByKey(previous.items) : new Map(),
      withTranscriptTimeRows(base.items, {
        messageById: messageById(),
        pendingSends: props.pendingSends,
        turnClocks: props.turnClocks ?? {},
        unreadSince: unreadSince(props.sessionId),
        hasMoreBefore,
        sessionLive: props.visibleTurnActive ?? false,
        dayKey: viewerCalendarTimeFormat().dayKey,
      }),
    );
    if (!props.transcriptSpans) return { items: timed, spanIndexByItemKey: base.spanIndexByItemKey };
    // Markers join the next row's span; tails join the previous row's.
    const spanIndexByItemKey = new Map(base.spanIndexByItemKey);
    let pendingMarkers: string[] = [];
    let previousSpan: number | undefined;
    for (const item of timed) {
      if (item.kind === "turn_tail") {
        const span = previousSpan ?? base.spanIndexByItemKey.get(item.anchorMessageId);
        if (span !== undefined) spanIndexByItemKey.set(item.key, span);
        continue;
      }
      if (isTranscriptTimeItem(item)) {
        pendingMarkers.push(item.key);
        continue;
      }
      const span = base.spanIndexByItemKey.get(item.key) ??
        (item.kind === "pending_user" && props.transcriptSpans.length > 0
          ? props.transcriptSpans.length - 1 : undefined);
      if (span !== undefined) {
        spanIndexByItemKey.set(item.key, span);
        for (const key of pendingMarkers) spanIndexByItemKey.set(key, span);
        previousSpan = span;
        pendingMarkers = [];
      }
    }
    if (pendingMarkers.length > 0) {
      const fallbackSpan = previousSpan ?? (props.transcriptSpans.length > 0 ? props.transcriptSpans.length - 1 : 0);
      for (const key of pendingMarkers) spanIndexByItemKey.set(key, fallbackSpan);
    }
    return { items: timed, spanIndexByItemKey };
  });
  const items = () => timedItemsModel().items;
  const spanIndexByItemKey = () => timedItemsModel().spanIndexByItemKey;
  // Each terminal assistant row's reviewable turn, resolved once for the whole transcript.
  const reviewTurns = createMemo(
    () => reviewTurnsByAssistant(items(), messageById(), props.visibleTurnActive ?? false),
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
    const spans = props.transcriptSpans;
    if (!spans) return undefined;
    const index = spanIndexByItemKey().get(itemKey);
    if (index === undefined) return undefined;
    const span = spans[index];
    return span && isCatalogSpan(span) ? span.runId || `span-${index}` : undefined;
  };

  const continuationKeys = createMemo(() => {
    const keys = new Set<string>();
    for (const message of props.messages) {
      if (message.kind === "user_continuation") keys.add(message.id);
    }
    return keys;
  }, undefined, {
    equals: (before, after) => before?.size === after.size && [...after].every((key) => before.has(key)),
  });

  const rowSeams = createMemo(() => transcriptRowSeams(items(), {
    spanKeyOf: catalogSpanKeyOf,
    continuation: (key) => continuationKeys().has(key),
  }), undefined, { equals: sameMapEntries });

  const seamForItem = (key: string): TranscriptSeam | undefined => rowSeams().get(key);

  // A seam belongs to the row beneath it, so measured heights include it.
  const rowInsetsByItemKey = createMemo(() => {
    const spacing = transcriptSpacing();
    const remPx = transcriptRootRemPx();
    const insets = new Map<string, number>();
    for (const [key, seam] of rowSeams()) insets.set(key, transcriptSeamPx(seam, spacing, remPx));
    return insets;
  }, undefined, { equals: sameMapEntries });

  const rowInsetPx = (key: string) => rowInsetsByItemKey().get(key) ?? 0;

  const taskMatches = createMemo(() => {
    const sid = props.sessionId?.trim();
    if (!sid) return new Map<string, WorkerTask>();
    return taskWorkerMatchesForTranscript(items(), workers(), sid);
  }, undefined, { equals: sameMapEntries });

  const exploreContext = createMemo((): CitationExploreContext => ({
    ...props.exploreContext,
    sessionId: props.sessionId?.trim() || props.exploreContext?.sessionId,
    legId: props.exploreContext?.legId ?? resolveExploreLegId(workers()),
  }), undefined, { equals: sameWireValue });

  const [scrollEpoch, setScrollEpoch] = createSignal(0);
  const residentPresence = useResidentPresence();

  createEffect(() => {
    const presence = residentPresence();
    viewport?.stream();
    if (layout() !== "chat" || presence === "idle") return;
    const root = streamRoot();
    if (!root) return;
    // Retained transcripts can enter a new host without changing their rows.
    queueMicrotask(() => {
      if (streamRoot() !== root || !root.isConnected) return;
      setScrollEpoch((n) => n + 1);
      // Detached rows may have been swept from the measurement observer.
      for (const row of root.querySelectorAll<HTMLElement>(".transcript-viewport-row[data-index]")) {
        virtualizer.queueMeasurement(row);
      }
    });
  });

  const scrollHostEl = (): HTMLElement | null => {
    scrollEpoch();
    if (layout() !== "chat") return null;
    return transcriptScrollHostElement(streamRoot());
  };

  const rowHeightScope = () => {
    const projectId = props.projectId?.trim();
    const sessionId = props.sessionId?.trim();
    return projectId && sessionId ? { projectId, sessionId } : undefined;
  };

  const geometry = createTranscriptGeometry({ root: streamRoot, viewport: scrollHostEl, enabled: () => layout() === "chat" });
  const rowMetrics = geometry.metrics;
  const rowGeometry = geometry.key;

  const workerMetaMessages = createMemo((): Message[] => {
    const extra = props.workerTranscripts;
    if (!extra) return [];
    const out: Message[] = [];
    for (const entry of Object.values(extra)) {
      if (entry.rows.length) out.push(...entry.rows);
    }
    return out;
  });
  const artifactPlacements = createMemo(() =>
    buildCanonicalArtifactPlacement(props.messages, {
      metaMessages: workerMetaMessages(),
    }),
  );
  /** Artifact dimensions and reference chips used for row estimates. */
  const presentedVisualsFor = (item: DisplayTranscriptItem): PresentedVisual[] => {
    if (item.kind !== "assistant" && item.kind !== "user") return [];
    const fromWire = messageById().get(item.key)?.artifact_ids ?? [];
    const ids =
      item.kind === "user" && fromWire.length === 0 ? (item.artifactIds ?? []) : fromWire;
    const placements = artifactPlacements();
    return ids
      .map((id) => id.trim())
      .filter(Boolean)
      .map((id): PresentedVisual => {
        if (!isCanonicalArtifactEntry(placements, id, visualPresentEntryKey(item.key, id))) {
          return { kind: "reference" };
        }
        const meta = placements.meta(id);
        return meta?.width && meta.height
          ? { kind: "image", width: meta.width, height: meta.height }
          : { kind: "unsized" };
      });
  };

  const contentEstimates = new WeakMap<DisplayTranscriptItem, { metrics: TranscriptRowMetrics; visuals: string; height: number | undefined }>();
  const contentRowEstimate = (item: DisplayTranscriptItem): number | undefined => {
    const metrics = rowMetrics();
    const visuals = presentedVisualsFor(item);
    const visualKey = JSON.stringify(visuals);
    const previous = contentEstimates.get(item);
    let estimate = previous?.metrics === metrics && previous.visuals === visualKey ? previous.height : undefined;
    if (metrics && (!previous || previous.metrics !== metrics || previous.visuals !== visualKey)) {
      estimate = transcriptRowContentEstimate(item, metrics, visuals);
      contentEstimates.set(item, { metrics, visuals: visualKey, height: estimate });
    }
    return estimate !== undefined && (item.kind === "user" || item.kind === "pending_user") && messageTimesPref() === "always"
      ? estimate + 16 + transcriptSpacing().messageTimeGap * transcriptRootRemPx()
      : estimate;
  };

  // A time under the bubble changes the user row's height.
  const rowPresentation = (item: DisplayTranscriptItem): string => {
    const presentation = transcriptRowPresentation(item, disclosureOpen);
    return (item.kind === "user" || item.kind === "pending_user") && messageTimesPref() === "always"
      ? `${presentation}+time-below`
      : presentation;
  };

  const restoredRowEstimate = (item: DisplayTranscriptItem): number | undefined => {
    if (item.kind === "pending_user") return undefined;
    const scope = rowHeightScope();
    if (!scope) return undefined;
    const geometry = rowGeometry();
    if (!geometry) return undefined;
    return transcriptRowHeightForScope(scope, item.key, `${geometry}:${rowPresentation(item)}`);
  };

  const virtualizer = createTranscriptVirtualizer({
    items: () => (layout() === "chat" ? items() : []),
    scrollElement: scrollHostEl,
    revealOffset: (offset, glide) =>
      requireChatViewport().commitReveal(offset, glide),
    shiftContent: (deltaY, fromOffset) => requireChatViewport().shiftVirtualContent(deltaY, fromOffset),
    rowSizeHint: (item) => {
      const insetPx = rowInsetPx(item.key);
      const contentPx =
        restoredRowEstimate(item) ??
        transcriptRowPresentationEstimate(item, disclosureOpen) ??
        contentRowEstimate(item);
      if (contentPx !== undefined) return contentPx + insetPx;
      return insetPx > 0
        ? transcriptRowEstimatedHeight(item) + insetPx
        : undefined;
    },
    // Column measurements refine estimates for unmounted rows.
    estimateBasis: rowGeometry,
    placementBasis: rowInsetsByItemKey,
    originPx: geometry.origin,
    onRowMeasured: ({ element, index, size, width }) => {
      if (!(element instanceof HTMLElement)) return;
      if (document.fonts?.status === "loading") return;
      const metrics = rowMetrics();
      // Root and row observers may deliver the resize in different callbacks.
      if (width && metrics && Math.round(width * 64) !== Math.round(metrics.widthPx * 64)) return;
      if (element.querySelector('[data-animating="true"]')) return;
      // Hidden and animating rows have unstable heights.
      if (isShellLayoutUnstable() || element.closest('[data-resident="idle"]')) return;
      // Cached heights belong to the current disclosure state.
      for (const disclosure of element.querySelectorAll<HTMLElement>("[data-disclosure-key]")) {
        const key = parseTranscriptDisclosureKey(disclosure.dataset.disclosureKey);
        if (!key) continue;
        const isOpen = disclosure instanceof HTMLDetailsElement
          ? disclosure.open
          : disclosure.dataset.expanded === "true";
        if (isOpen !== disclosureOpen(key)) return;
      }
      const item = items()[index];
      if (!item || element.dataset.msgId !== item.key || item.kind === "pending_user") return;
      const scope = rowHeightScope();
      const geometry = rowGeometry();
      if (!scope || !geometry) return;
      recordTranscriptRowHeight(
        scope,
        item.key,
        `${geometry}:${rowPresentation(item)}`,
        Math.max(0, size - rowInsetPx(item.key)),
      );
    },
  });

  geometry.attach(virtualizer);
  const keyboard = createTranscriptKeyboard({
    active: () => layout() === "chat" && residentPresence() === "active",
    rows: items, root: streamRoot, viewport: scrollHostEl,
    reveal: index => virtualizer.scrollToIndex(index, { align: "center" }),
    loadEarlier: async first => {
      const client = getLycaonClient(), store = props.checkpointAppStore, sessionId = props.sessionId;
      if (!client || !store || !sessionId) return;
      if (first) await loadTranscriptHistoryUntil(client, store, sessionId, () => !store.state.transcript.hasMoreBefore);
      else await loadTranscriptPage(client, store, sessionId, "older");
    },
    loadLater: async last => {
      const client = getLycaonClient(), store = props.checkpointAppStore, sessionId = props.sessionId;
      if (!client || !store || !sessionId || !store.state.transcript.hasTailGap) return;
      if (last) store.actions.resetOlderTranscriptPages();
      else await loadTranscriptPage(client, store, sessionId, "newer");
    },
    reportError: error => reportSurfaceFailure({ code: "transcript_navigation_failed", title: "Could not load earlier messages", suggestedAction: "Try navigating to the message again." }, error, props.projectId ?? ""),
  });
  const currentScrollOffset = () => {
    const host = scrollHostEl();
    return (host && scrollportMotionForViewport(host)?.offsetY()) ?? virtualizer.scrollOffset;
  };
  // Inset changes reuse measured interiors, including rows outside the mounted window.
  let previousInsets = new Map<string, number>();
  createEffect(() => {
    const insets = rowInsetsByItemKey();
    const sizes: { index: number; size: number }[] = [];
    untrack(() => {
      items().forEach((item, index) => {
        const measured = virtualizer.itemSizeCache.get(item.key);
        if (measured === undefined) return;
        const delta = (insets.get(item.key) ?? 0) - (previousInsets.get(item.key) ?? 0);
        if (delta !== 0) sizes.push({ index, size: measured + delta });
      });
      previousInsets = insets;
      virtualizer.resizeItems(sizes);
    });
  });

  // Stable keys preserve mounted window rows.
  const virtualRowByKey = createMemo(() => {
    const map = new Map<string, VirtualItem>();
    for (const v of virtualizer.getVirtualItems()) {
      map.set(String(v.key), v);
    }
    return map;
  });
  // Unchanged visible keys retain the array identity.
  const virtualRowKeys = createMemo(() => [...virtualRowByKey().keys()], [], {
    equals: (a, b) => a.length === b.length && a.every((k, i) => k === b[i]),
  });
  const virtualSpanGroups = createMemo<VirtualTranscriptSpanGroup[]>(() => {
    const spans = props.transcriptSpans;
    if (!spans?.length) {
      return [{ key: "span-0", rowKeys: virtualRowKeys() }];
    }
    const groups = spans.map((span, index) => ({
      key: index === 0 ? "span-0" : span.runId || `span-${index}`,
      span,
      index,
      rowKeys: [] as string[],
    }));
    const byKey = spanIndexByItemKey();
    for (const rowKey of virtualRowKeys()) {
      const item = itemByKey().get(rowKey);
      const spanIndex = item ? byKey.get(item.key) : undefined;
      if (spanIndex !== undefined) groups[spanIndex]?.rowKeys.push(rowKey);
    }
    return groups;
  });
  const virtualRunway = createMemo(() => {
    scrollEpoch();
    virtualizer.scrollOffset;
    return transcriptVirtualRunway(
      [...virtualRowByKey().values()],
      virtualizer.getTotalSize(),
      geometry.origin(),
    );
  }, undefined, {
    equals: (a, b) => !!a && a.beforePx === b.beforePx && a.afterPx === b.afterPx,
  });

  // Window swaps preserve extent; resize observations report content growth.
  const virtualExtent = createMemo(() => virtualizer.getTotalSize());
  createEffect(() => {
    if (layout() !== "chat") return;
    virtualExtent();
    const scroller = scrollHostEl();
    if (!scroller) return;
    scrollportMotionForViewport(scroller)?.scheduleLayoutReconcile();
  });


  createEffect(() => {
    if (layout() !== "chat") return;
    const sessionId = props.sessionId?.trim();
    if (!sessionId) return;
    const appStore = props.checkpointAppStore;
    const detach = requireChatViewport().attachVirtualWindow({
      measureOrigin: geometry.measureOrigin,
      items,
      // Only host-backed rows survive session restoration.
      readingPosition: () =>
        transcriptReadingPosition(virtualizer, (rowKey) => {
          const item = itemByKey().get(rowKey);
          return !!item && !isTranscriptTimeItem(item) && item.kind !== "pending_user";
        }, currentScrollOffset()),
      offsetForPosition: (position) =>
        transcriptOffsetForPosition(virtualizer, position),
      scrollToIndex: (index, opts) => {
        virtualizer.scrollToIndex(index, opts);
      },
      // The virtualizer's smooth request becomes the scrollport's glide.
      scrollToOffset: (offset, { glide }) =>
        virtualizer.scrollToOffset(offset, { behavior: glide ? "smooth" : "auto" }),
      ensureAnchorLoaded: async (anchor) => {
        if (!appStore || !sessionId) return;
        const client = getLycaonClient();
        if (!client) return;
        await ensureTranscriptRevealAnchorLoaded(
          client,
          appStore,
          sessionId,
          anchor,
        );
      },
      // Null until a multi-day chat's day label scrolls above the reader.
      readingDay: () => {
        const { days, labels } = untrack(rowDays);
        if (labels < 2) return null;
        const position = transcriptReadingPosition(virtualizer, undefined, currentScrollOffset());
        const day = position ? days.get(position.rowKey) : undefined;
        return day && day.labelKey !== position?.rowKey ? day.at : null;
      },
      ensureRowLoaded: async (rowKey) => {
        if (!appStore || !sessionId) return;
        const client = getLycaonClient();
        if (!client) return;
        await loadTranscriptHistoryUntil(client, appStore, sessionId, () =>
          items().some((item) => item.key === rowKey),
        );
      },
    });
    onCleanup(detach);
  });

  // Arriving rows can complete a pending position restore.
  createEffect(() => {
    if (layout() !== "chat") return;
    items();
    untrack(() => requireChatViewport().rowsChanged());
  });

  // Worker paths open in their overlay until promotion.
  const sourceContext = createMemo(() => ({
    projectId: props.projectId?.trim() ?? "",
    rootRefs: props.rootRefs,
    jobId: props.branchJobId,
  }));

  return (
    <Show when={items().length > 0 || tailSlots().length > 0}>
    <SourceContextProvider value={sourceContext()}>
      <Show
        when={layout() === "chat"}
        fallback={
          <TranscriptDisclosureProvider value={workerDisclosures()}>
          <ArtifactDedupProvider placements={artifactPlacements}>
            <div role="list" class="den-worker-transcript" data-testid="message-stream" ref={(element) => {
              props.workerListRef?.(element);
              onCleanup(() => props.workerListRef?.(undefined));
            }}>
              <VirtualCardList items={items()} keyOf={(item) => item.key} label="Worker activity" estimateSize={120}
                scroll="ancestor" keepReadingPosition
                externalFind={workerItemHasExternalFind}
                search={(item,query,sensitive,signal) => searchWorkerItem(item,props.sessionId ?? undefined,query,sensitive,signal)}
                onFindReveal={(item,match) => {
                  if (!match.toolCallId) return;
                  const row = document.querySelector<HTMLElement>(`[data-worker-item-key="${CSS.escape(item.key)}"]`);
                  const tool = item.kind === "activity_span" ? row?.querySelector<HTMLElement>(".den-activity-span") : row?.querySelector<HTMLElement>(`[data-tool-call-id="${CSS.escape(match.toolCallId)}"]`);
                  if (!tool) return;
                  tool.dispatchEvent(new CustomEvent(TOOL_CONTENT_REVEAL_EVENT,{ detail:match }));
                }}>
                {(item) => (
                  <div
                    role="listitem"
                    class="den-seam"
                    data-worker-item-key={item().key}
                    data-seam={seamForItem(item().key)}
                  >
                  <TranscriptItemRow
                    item={item}
                    taskMatches={taskMatches}
                    workers={workers()}
                    messages={props.messages}
                    messageById={messageById}
                    reviewTurns={reviewTurns}
                    workerTranscripts={props.workerTranscripts}
                    onOpenWorker={props.onOpenWorker}
                    layout="worker"
                    sessionId={props.sessionId}
                    checkpointClient={props.checkpointClient}
                    checkpointAppStore={props.checkpointAppStore}
                    projectId={props.projectId}
                    rootRefs={props.rootRefs}
                    branchJobId={props.branchJobId}
                    projectDir={props.projectDir}
                    exploreContext={exploreContext()}
                    onExploreInSearch={props.onExploreInSearch}
                    activePendingPhaseId={props.activePendingPhaseId}
                    visibleTurnActive={props.visibleTurnActive ?? false}
                    recovery={props.recovery}
                  />
                  </div>
                )}
              </VirtualCardList>
            </div>
          </ArtifactDedupProvider>
          </TranscriptDisclosureProvider>
        }
      >
        <ArtifactDedupProvider
          placements={artifactPlacements}
          revealCanonical={(placement) => {
            const index = items().findIndex((item) =>
              transcriptItemContainsMessage(item, placement.rowKey),
            );
            if (index < 0) return false;
            virtualizer.scrollToIndex(index, { align: "center" });
            return true;
          }}
        >
          <section
            class="den-chat-stream-inner"
            data-testid="message-stream"
            ref={setStreamRoot}
          >
            <div
              class="den-transcript-virtual-runway"
              aria-hidden="true"
              data-transcript-runway="before"
              style={{ height: `${virtualRunway().beforePx}px` }}
            />
            <KeyedIndex
              each={virtualSpanGroups()}
              keyOf={(group) => group.key}
            >
              {(group) => {
                const span = () => group().span;
                const catalogSpan = () => {
                  const block = span();
                  return block ? isCatalogSpan(block) : false;
                };
                // Span groups isolate workflow run identity.
                return (
                  <section
                    class="den-transcript-span-rows"
                    id={
                      span()?.startMessageId
                        ? `msg-${span()?.startMessageId}`
                        : undefined
                    }
                    data-testid={
                      span()
                        ? catalogSpan()
                          ? "den-chat-span-run"
                          : "den-chat-span-implement"
                        : undefined
                    }
                    data-workflow-run-id={span()?.runId}
                  >
                    <For each={group().rowKeys}>
                      {(rowKey) => {
                        const vRow = () => virtualRowByKey().get(rowKey);
                        const virtualStart = () => {
                          const row = vRow();
                          return row ? row.start - geometry.origin() : undefined;
                        };
                        const rowItem = () => itemByKey().get(rowKey);
                        return (
                          <Show when={rowItem()}>
                            {(item) => (
                              <div
                                class="transcript-viewport-row den-seam"
                                data-index={String(vRow()?.index ?? -1)}
                                // Time rows are not messages.
                                data-msg-id={isTranscriptTimeItem(item()) ? undefined : item().key}
                                data-time-row={isTranscriptTimeItem(item()) ? item().key : undefined}
                                data-virtual-start={virtualStart()}
                                data-row-height-key={rowGeometry() ? `${rowGeometry()}:${rowPresentation(item())}` : undefined}
                                data-seam={seamForItem(item().key)}
                                data-tool-call-ids={transcriptItemToolCallIds(item())}
                                ref={(el) => {
                                  geometry.bindRow(el, () => vRow()?.index ?? -1);
                                  if (!isTranscriptTimeItem(item())) keyboard.bindRow(el, rowKey);
                                }}
                              >
                                <TranscriptItemRow
                                  item={item}
                                  taskMatches={taskMatches}
                                  workers={workers()}
                                  messages={props.messages}
                                  messageById={messageById}
                                  reviewTurns={reviewTurns}
                                  workerTranscripts={props.workerTranscripts}
                                  onOpenWorker={props.onOpenWorker}
                                  layout="chat"
                                  visibleTurnActive={props.visibleTurnActive ?? false}
                                  sessionId={props.sessionId}
                                  checkpointClient={props.checkpointClient}
                                  checkpointAppStore={props.checkpointAppStore}
                                  projectId={props.projectId}
                                  rootRefs={props.rootRefs}
                                  branchJobId={props.branchJobId}
                                  projectDir={props.projectDir}
                                  exploreContext={exploreContext()}
                                  onExploreInSearch={props.onExploreInSearch}
                                  blueprint={props.blueprint}
                                  onBlueprintRevise={props.onBlueprintRevise}
                                  activePendingPhaseId={props.activePendingPhaseId}
                                  recovery={props.recovery}
                                />
                              </div>
                            )}
                          </Show>
                        );
                      }}
                    </For>
                  </section>
                );
              }}
            </KeyedIndex>
            <div
              class="den-transcript-virtual-runway"
              aria-hidden="true"
              data-transcript-runway="after"
              style={{ height: `${virtualRunway().afterPx}px` }}
            />
            <KeyedIndex each={tailSlots()} keyOf={(slot) => slot.id}>
              {(slot) => (
                <div
                  class="transcript-viewport-row den-seam"
                  data-transcript-tail={slot().id}
                  data-seam={tailSlotSeam(slot().id)}
                >
                  {slot().children()}
                </div>
              )}
            </KeyedIndex>
            <div
              data-transcript-end=""
              aria-hidden="true"
              style={{ height: "0", "flex-shrink": 0 }}
            />
          </section>
        </ArtifactDedupProvider>
      </Show>
    </SourceContextProvider>
    </Show>
  );
}
