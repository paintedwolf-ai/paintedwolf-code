import { createTranscriptKeyboard } from "../../chat/transcript/presentation/transcript-keyboard.ts";
import { reportSurfaceFailure } from "../../notices/surface-failure.ts";
import {
  parseTranscriptDisclosureKey,
  type TranscriptDisclosureKey,
} from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { createTranscriptGeometry } from "../../chat/transcript/layout/transcript-geometry.ts";
import type { TranscriptSeam } from "../../chat/transcript/layout/transcript-spacing.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { For, Show, createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { KeyedIndex } from "../keyed-index.tsx";
import type { WorkerTranscriptCache } from "../../chat/worker/worker-transcript.ts";
import type { SlotContent } from "../../ui/slot-content.ts";
import type { Message, TurnClock, TurnLoad, WorkerTask } from "../../api/types.ts";
import { SourceContextProvider } from "../source/annotations/source-context.ts";
import type { RawTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import {
  recordTranscriptRowHeight,
  transcriptRowContentRevision,
} from "../../chat/transcript/layout/transcript-row-heights-persist.ts";
import type { TranscriptLayout } from "../../chat/transcript/layout/transcript-layout.ts";
import type { OpenWorkerOptions } from "../../chat/worker/workers-model.ts";
import { ArtifactDedupProvider } from "../../chat/visual/artifact-dedup-context.tsx";
import type { ChatBlueprintState } from "../blueprint/chat-blueprint-state.ts";
import type { CitationExploreContext } from "../citation/CitationGroundingPanel.tsx";
import {
  bindTranscriptSpanAnchors,
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
import { isTranscriptTimeItem } from "../../chat/transcript/presentation/transcript-time-rows.ts";
import type { VirtualItem } from "@tanstack/solid-virtual";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import {
  createTranscriptHistoryView,
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
import { WorkerTranscriptList } from "./WorkerTranscriptList.tsx";
import { useTranscriptProjectionModel } from "./useTranscriptProjectionModel.ts";

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
  const layout = () => props.layout ?? "chat";
  const workers = () => props.workers ?? [];
  const tailSlots = createMemo(() =>
    (props.tail ?? []).filter((slot) => slot.present()),
  );
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
  const [scrollEpoch, setScrollEpoch] = createSignal(0);
  const residentPresence = useResidentPresence();

  const scrollHostEl = (): HTMLElement | null => {
    scrollEpoch();
    if (layout() !== "chat") return null;
    return transcriptScrollHostElement(streamRoot());
  };

  const geometry = createTranscriptGeometry({
    root: streamRoot,
    viewport: scrollHostEl,
    enabled: () => layout() === "chat",
  });
  const rowMetrics = geometry.metrics;
  const rowGeometry = geometry.key;

  const {
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
  } = useTranscriptProjectionModel({
    layout,
    messages: () => props.messages,
    pendingSends: () => props.pendingSends,
    sessionId: () => props.sessionId,
    projectId: () => props.projectId,
    workers,
    workerTranscripts: () => props.workerTranscripts,
    transcriptItems: () => props.transcriptItems,
    transcriptSpans: () => props.transcriptSpans,
    visibleTurnActive: () => props.visibleTurnActive ?? false,
    turnClocks: () => props.turnClocks,
    turnLoads: () => props.turnLoads,
    hasMoreBefore: () => props.hasMoreBefore,
    checkpointAppStore: () => props.checkpointAppStore,
    exploreContext: () => props.exploreContext,
    disclosureOpen,
    rowMetrics,
    rowGeometry,
  });

  // A tail row opens its own reading unit below the last row; further ones sit with it.
  const tailSlotSeam = (id: string): TranscriptSeam | undefined => {
    if (tailSlots()[0]?.id !== id) return "row";
    return items().length > 0 ? "section" : undefined;
  };

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
      for (const row of root.querySelectorAll<HTMLElement>(
        ".transcript-viewport-row[data-index]",
      )) {
        virtualizer.queueMeasurement(row);
      }
    });
  });

  const resolveLostAnchor = bindTranscriptSpanAnchors({
    items,
    isOpen: disclosureOpen,
    rename: (from, to) => viewport?.disclosures.rename(from, to),
    root: scrollHostEl,
  });
  const historyView = createTranscriptHistoryView({
    enabled: () => layout() === "chat",
    messages: () => props.messages,
    items,
    appStore: () => props.checkpointAppStore,
  });

  const virtualizer = createTranscriptVirtualizer({
    items: () => (layout() === "chat" ? items() : []),
    scrollElement: scrollHostEl,
    revealOffset: (offset, glide) =>
      requireChatViewport().commitReveal(offset, glide),
    shiftContent: (deltaY, fromOffset) =>
      requireChatViewport().shiftVirtualContent(deltaY, fromOffset),
    rowSizeHint: (item) => {
      const insetPx = rowInsetPx(item.key);
      const contentPx = estimatedContentHeight(item);
      if (contentPx !== undefined) return contentPx + insetPx;
      return insetPx > 0
        ? transcriptRowEstimatedHeight(item) + insetPx
        : undefined;
    },
    // Column measurements refine estimates for unmounted rows.
    estimateBasis: rowGeometry,
    placementBasis: rowInsetsByItemKey,
    contentRevision: transcriptRowContentRevision,
    resolveLostAnchor,
    originPx: geometry.origin,
    unloadedOrds: historyView.unloadedOrds,
    residentRowSpan: historyView.residentRowSpan,
    onRowMeasured: ({ element, index, size, width }) => {
      if (!(element instanceof HTMLElement)) return;
      if (document.fonts?.status === "loading") return;
      const metrics = rowMetrics();
      // Root and row observers may deliver the resize in different callbacks.
      if (
        width &&
        metrics &&
        Math.round(width * 64) !== Math.round(metrics.widthPx * 64)
      ) {
        return;
      }
      if (element.querySelector('[data-animating="true"]')) return;
      // Hidden and animating rows have unstable heights.
      if (isShellLayoutUnstable() || element.closest('[data-resident="idle"]')) {
        return;
      }
      // Cached heights belong to the current disclosure state.
      for (const disclosure of element.querySelectorAll<HTMLElement>(
        "[data-disclosure-key]",
      )) {
        const key = parseTranscriptDisclosureKey(
          disclosure.dataset.disclosureKey,
        );
        if (!key) continue;
        const isOpen = disclosure.dataset.expanded === "true";
        if (isOpen !== disclosureOpen(key)) return;
      }
      const item = items()[index];
      if (
        !item ||
        element.dataset.msgId !== item.key ||
        item.kind === "pending_user"
      ) {
        return;
      }
      const scope = rowHeightScope();
      const key = rowHeightKey(item);
      if (!scope || !key) return;
      recordTranscriptRowHeight(
        scope,
        key,
        Math.max(0, size - rowInsetPx(item.key)),
      );
    },
  });

  geometry.attach(virtualizer);
  const keyboard = createTranscriptKeyboard({
    active: () => layout() === "chat" && residentPresence() === "active",
    rows: items,
    root: streamRoot,
    viewport: scrollHostEl,
    reveal: (index) => virtualizer.scrollToIndex(index, { align: "center" }),
    loadEarlier: async (first) => {
      const client = getLycaonClient(),
        store = props.checkpointAppStore,
        sessionId = props.sessionId;
      if (!client || !store || !sessionId) return;
      if (first) {
        await loadTranscriptHistoryUntil(
          client,
          store,
          sessionId,
          () => !store.state.transcript.hasMoreBefore,
        );
      } else {
        await loadTranscriptPage(client, store, sessionId, "older");
      }
    },
    loadLater: async (last) => {
      const client = getLycaonClient(),
        store = props.checkpointAppStore,
        sessionId = props.sessionId;
      if (!client || !store || !sessionId || !store.state.transcript.hasTailGap) {
        return;
      }
      if (last) store.actions.resetOlderTranscriptPages();
      else await loadTranscriptPage(client, store, sessionId, "newer");
    },
    reportError: (error) =>
      reportSurfaceFailure(
        {
          code: "transcript_navigation_failed",
          title: "Could not load earlier messages",
          suggestedAction: "Try navigating to the message again.",
        },
        error,
        props.projectId ?? "",
      ),
  });
  const currentScrollOffset = () => {
    const host = scrollHostEl();
    return (
      (host && scrollportMotionForViewport(host)?.offsetY()) ??
      virtualizer.scrollOffset
    );
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
        const delta =
          (insets.get(item.key) ?? 0) - (previousInsets.get(item.key) ?? 0);
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
  const virtualRunway = createMemo(
    () => {
      scrollEpoch();
      virtualizer.scrollOffset;
      return transcriptVirtualRunway(
        [...virtualRowByKey().values()],
        virtualizer.getTotalSize(),
        geometry.origin(),
      );
    },
    undefined,
    {
      equals: (a, b) =>
        !!a && a.beforePx === b.beforePx && a.afterPx === b.afterPx,
    },
  );

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
      historyGeometry: historyView.geometry.bind(null, virtualizer),
      // Only host-backed rows survive session restoration.
      readingPosition: () =>
        transcriptReadingPosition(
          virtualizer,
          (rowKey) => {
            const item = itemByKey().get(rowKey);
            return (
              !!item &&
              !isTranscriptTimeItem(item) &&
              item.kind !== "pending_user"
            );
          },
          currentScrollOffset(),
        ),
      offsetForPosition: (position) =>
        transcriptOffsetForPosition(virtualizer, position, resolveLostAnchor),
      scrollToIndex: (index, opts) => {
        virtualizer.scrollToIndex(index, opts);
      },
      // The virtualizer's smooth request becomes the scrollport's glide.
      scrollToOffset: (offset, { glide }) =>
        virtualizer.scrollToOffset(offset, {
          behavior: glide ? "smooth" : "auto",
        }),
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
        const position = transcriptReadingPosition(
          virtualizer,
          undefined,
          currentScrollOffset(),
        );
        const day = position ? days.get(position.rowKey) : undefined;
        return day && day.labelKey !== position?.rowKey ? day.at : null;
      },
      ensureRowLoaded: async (rowKey) => {
        if (!appStore || !sessionId) return;
        const client = getLycaonClient();
        if (!client) return;
        await loadTranscriptHistoryUntil(client, appStore, sessionId, () =>
          items().some((item) => item.key === rowKey) ||
          Boolean(resolveLostAnchor(rowKey)),
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
            <WorkerTranscriptList
              items={items()}
              artifactPlacements={artifactPlacements}
              workerListRef={props.workerListRef}
              sessionId={props.sessionId}
              branchJobId={props.branchJobId}
              seamForItem={seamForItem}
              taskMatches={taskMatches}
              workers={workers()}
              messages={props.messages}
              messageById={messageById}
              reviewTurns={reviewTurns}
              workerTranscripts={props.workerTranscripts}
              onOpenWorker={props.onOpenWorker}
              checkpointClient={props.checkpointClient}
              checkpointAppStore={props.checkpointAppStore}
              projectId={props.projectId}
              rootRefs={props.rootRefs}
              projectDir={props.projectDir}
              exploreContext={exploreContext()}
              onExploreInSearch={props.onExploreInSearch}
              activePendingPhaseId={props.activePendingPhaseId}
              visibleTurnActive={props.visibleTurnActive ?? false}
              recovery={props.recovery}
            />
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
                            return row
                              ? row.start - geometry.origin()
                              : undefined;
                          };
                          const rowItem = () => itemByKey().get(rowKey);
                          return (
                            <Show when={rowItem()}>
                              {(item) => (
                                <div
                                  class="transcript-viewport-row den-seam"
                                  data-index={String(vRow()?.index ?? -1)}
                                  // Time rows are not messages.
                                  data-msg-id={
                                    isTranscriptTimeItem(item())
                                      ? undefined
                                      : item().key
                                  }
                                  data-time-row={
                                    isTranscriptTimeItem(item())
                                      ? item().key
                                      : undefined
                                  }
                                  data-virtual-start={virtualStart()}
                                  data-row-height-key={
                                    rowHeightKey(item())?.variant
                                  }
                                  data-row-revision={transcriptRowContentRevision(
                                    item(),
                                  )}
                                  data-seam={seamForItem(item().key)}
                                  data-tool-call-ids={transcriptItemToolCallIds(
                                    item(),
                                  )}
                                  ref={(el) => {
                                    geometry.bindRow(
                                      el,
                                      () => vRow()?.index ?? -1,
                                    );
                                    if (!isTranscriptTimeItem(item())) {
                                      keyboard.bindRow(el, rowKey);
                                    }
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
                                    visibleTurnActive={
                                      props.visibleTurnActive ?? false
                                    }
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
                                    activePendingPhaseId={
                                      props.activePendingPhaseId
                                    }
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
