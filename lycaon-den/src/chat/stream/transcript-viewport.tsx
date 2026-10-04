import { createTranscriptDisclosureStore, type TranscriptDisclosureStore } from "../transcript/presentation/disclosure-state.tsx";
import type { TranscriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
import { cancelScrollportFrame, scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import { cancelStreamSpringScroll } from "./stream-scroll-spring.ts";
import {
  createContext,
  createEffect,
  createRoot,
  createSignal,
  untrack,
  useContext,
  type Accessor,
  type ParentProps,
} from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { scrollportMotionForViewport } from "../../platform/scrolling/scrollport-motion.ts";
import { denScrollDebugLog } from "./den-scroll-debug.ts";
import {
  isShellLayoutUnstable,
  onShellLayoutSettled,
} from "../../shell/shell-layout-busy.ts";
import { findSearchesChat } from "../../find/find-controller.ts";
import { prefersReducedMotion } from "../../platform/interaction/reduced-motion.ts";
import { TAB_PANEL_TRANSITION_MS } from "../../components/chatview/tabs.ts";
import { watchTranscriptLoadMore } from "../transcript/layout/transcript-load-more.ts";
import type { TranscriptRevealAnchor } from "../transcript/presentation/transcript-reveal-target.ts";
import type { TranscriptItem } from "../transcript/projection/transcript-item-model.ts";
import type { TranscriptReadingPosition } from "../transcript/layout/transcript-virtualizer.ts";
import {
  transcriptItemContainsMessage,
  transcriptItemToolCallIds,
} from "../transcript/projection/transcript-item-anchors.ts";
import type { TranscriptDeliveryKind } from "../transcript/layout/transcript-tail.ts";
import {
  persistTranscriptViewportSnapshot,
  transcriptViewportOpenKeys,
  transcriptViewportSnapshot,
} from "./transcript-viewport-state.ts";
import {
  applyStreamTabPanelInset,
  clearStreamTabPanelInset,
  commitStreamScroll,
  commitStreamTail,
  glideStreamToTail,
  isStreamNearBottom,
  reconcileStreamLayout,
  scheduleSyncStreamScrollFade,
  setStreamTailPin,
  shiftStreamContent,
  streamTailOffset,
  subscribeStreamScroll,
  suppressStreamScrollEngagement,
  suppressStreamScrollFade,
  syncStreamScrollbarLayout,
  watchStreamGeometry,
  watchStreamReaderIntent,
  watchStreamScrollFade,
  watchStreamTabPanelInset,
  watchStreamTabPanelRetract,
} from "./stream-scroll.ts";
import {
  canPresentProse,
  fadeProseArrival,
  proseDeliveryRow,
} from "./prose-arrival.ts";

const TRANSCRIPT_VIEWPORT_SAVE_IDLE_MS = 1_200;
/** Tab panel motion moves the offset without reader input. */
const TAB_PANEL_ENGAGEMENT_QUIET_MS = TAB_PANEL_TRANSITION_MS + 80;
const PANEL_RETRACT_ENGAGEMENT_QUIET_MS = 360;

type TranscriptVirtualViewport = {
  /** Samples the transcript origin after outer chrome layout changes. */
  measureOrigin: () => void;
  items: () => readonly TranscriptItem[];
  readingPosition: () => TranscriptReadingPosition | null;
  /** Offset that shows a position again; null while its row is not resident. */
  offsetForPosition: (position: TranscriptReadingPosition) => number | null;
  scrollToIndex: (
    index: number,
    opts?: { align?: "start" | "center" | "end" | "auto" },
  ) => void;
  scrollToOffset: (offset: number, opts: { glide: boolean }) => void;
  ensureAnchorLoaded: (anchor: TranscriptRevealAnchor) => Promise<void>;
  /** Loads older history until the row is resident or history ends. */
  ensureRowLoaded: (rowKey: string) => Promise<void>;
  /** Epoch ms dating the reader's day once its day label is above them; null otherwise. */
  readingDay: () => number | null;
};

type TranscriptViewportRuntime = {
  client: () => LycaonClient | undefined;
  appStore: AppStore;
  sessionId: () => string | undefined;
  tabOpen: () => boolean;
  panelRetracted: () => boolean;
  panelHeightPx: () => number;
  onPanelRetractedChange: (retracted: boolean) => void;
};

type TranscriptContentChange =
  | {
    delivery: Extract<TranscriptDeliveryKind, "prose">;
    rowKey: string;
    firstContent: boolean;
  }
  | { delivery: Extract<TranscriptDeliveryKind, "structural"> };

export type TranscriptViewportController = {
  sessionId: () => string;
  /** The viewport keeps the latest message in view. */
  following: Accessor<boolean>;
  /** False while presented history ends at a gap before the live tail. */
  presentsLiveTail: Accessor<boolean>;
  stream: () => HTMLElement | null;
  disclosures: TranscriptDisclosureStore;
  attachStream: (stream: HTMLElement | null) => void;
  /** Returns whether a saved reading position was restored. */
  activateSession: (
    sessionId: string,
    previousSessionId?: string,
    preserveCurrent?: boolean,
  ) => boolean;
  saveSession: (sessionId?: string) => void;
  bindRuntime: (runtime: TranscriptViewportRuntime) => () => void;
  attachVirtualWindow: (view: TranscriptVirtualViewport) => () => void;
  /** The day the reader is in, once its label has scrolled above them. */
  readingDay: () => number | null;
  /** Virtual rows changed; a saved position waiting for its row retries. */
  rowsChanged: () => void;
  commitReveal: (offset: number, glide: boolean) => void;
  /** Carries the offset with content that moved above the reading row; returns the applied shift. */
  shiftVirtualContent: (deltaY: number, fromOffset: number) => number;
  /** Reader input preserves the current reading position. */
  stopFollowing: () => void;
  /**
   * Holds reader-driven disclosure expansion or collapse until final geometry is available.
   * A collapse also lets the native range contract without changing follow.
   */
  beginDisclosureMotion: (
    key: TranscriptDisclosureKey | undefined,
    direction: "open" | "close",
  ) => () => void;
  resumeFollowing: () => void;
  jumpToTail: (smooth?: boolean) => void;
  primeTail: (fadeMs?: number) => void;
  contentChanged: (change: TranscriptContentChange) => void;
  chromeChanged: (opts: {
    tabOpen: boolean;
    panelRetracted: boolean;
    panelHeightPx: number;
  }) => void;
  revealAnchor: (
    anchor: TranscriptRevealAnchor,
    opts?: {
      align?: "start" | "center" | "end" | "auto";
      signal?: AbortSignal;
      element?: () => HTMLElement | null;
    },
  ) => Promise<boolean>;
  setNavigationDisclosures: (keys: readonly TranscriptDisclosureKey[]) => void;
  clearNavigationDisclosures: () => void;
  ensureVisible: (
    element: HTMLElement,
    opts?: { align?: "start" | "center" | "end" | "nearest"; smooth?: boolean },
  ) => void;
};

const controllerRegistry = new Map<string, Set<TranscriptViewportController>>();

/** How long a reveal waits for its row to arrive and then to mount and settle. */
const REVEAL_SETTLE_TIMEOUT_MS = 6_000;

function normalizedSessionId(value: string): string {
  return value.trim();
}

export function registerTranscriptViewport(
  controller: TranscriptViewportController,
): () => void {
  const id = normalizedSessionId(controller.sessionId());
  if (!id) return () => {};
  let set = controllerRegistry.get(id);
  if (!set) {
    set = new Set();
    controllerRegistry.set(id, set);
  }
  set.add(controller);
  return () => {
    set?.delete(controller);
    if (set?.size === 0) controllerRegistry.delete(id);
  };
}

export function transcriptViewportForSession(
  sessionId: string,
): TranscriptViewportController | null {
  const set = controllerRegistry.get(normalizedSessionId(sessionId));
  if (!set) return null;
  for (const controller of set) {
    const stream = controller.stream();
    if (stream?.isConnected && !stream.closest('[data-resident="idle"]')) {
      return controller;
    }
  }
  return set.values().next().value ?? null;
}

function resolveRevealIndex(
  items: readonly TranscriptItem[],
  anchor: TranscriptRevealAnchor,
): number {
  const id = anchor.anchorId.trim();
  if (!id) return -1;
  if (anchor.chicklet === "tool") {
    const actionIndex = items.findIndex((item) =>
      item.kind !== "file_edit" && item.kind !== "worker_file_edit" &&
      transcriptItemToolCallIds(item)?.split(" ").includes(id),
    );
    if (actionIndex >= 0) return actionIndex;
    return items.findIndex((item) => transcriptItemToolCallIds(item)?.split(" ").includes(id));
  }
  return items.findIndex((item) => transcriptItemContainsMessage(item, id));
}

type StreamVisibleBounds = {
  top: number;
  bottom: number;
};

/** The header and its expanded tab panel may paint over the scrollport. */
function streamVisibleBounds(stream: HTMLElement): StreamVisibleBounds {
  const viewport = stream.getBoundingClientRect();
  const style = getComputedStyle(stream);
  const insetBounds = (top: number): StreamVisibleBounds => ({
    top: Math.min(viewport.bottom, top + (Number.parseFloat(style.scrollPaddingTop) || 0)),
    bottom: Math.max(top, viewport.bottom - (Number.parseFloat(style.scrollPaddingBottom) || 0)),
  });
  const stage = stream.closest<HTMLElement>(".den-shell-stage--chat");
  const header = stage?.querySelector<HTMLElement>(".den-shell-header-chat");
  if (!header) return insetBounds(viewport.top);

  const blockers = [
    header,
    ...header.querySelectorAll<HTMLElement>(".tabs__panel-clip"),
  ]
    .map((element) => element.getBoundingClientRect())
    .filter(
      (rect) =>
        rect.bottom > viewport.top &&
        rect.top < viewport.bottom &&
        rect.right > viewport.left &&
        rect.left < viewport.right,
    )
    .sort((a, b) => a.top - b.top);

  let top = viewport.top;
  for (const blocker of blockers) {
    // Only a contiguous overlay at the top narrows the usable viewport.
    if (blocker.top > top + 0.5 || blocker.bottom <= top) continue;
    top = Math.min(viewport.bottom, Math.max(top, blocker.bottom));
  }
  return insetBounds(top);
}

/** Following pins the live tail; reading preserves a row and its virtual offset. */
export function createTranscriptViewportController(opts: {
  sessionId: Accessor<string>;
}): TranscriptViewportController {
  let stream: HTMLElement | null = null;
  const [following, setFollowingSignal] = createSignal(true);
  /** Where the reader is while not following. */
  let position: TranscriptReadingPosition | null = null;
  /** A saved position waiting for its row to become resident. */
  let pendingRestore: TranscriptReadingPosition | null = null;
  let restoreLoading = false;
  let runtimeBound = false;
  let activeSessionId = "";
  let sessionGeneration = 0;
  let revealSequence = 0;
  let cancelElementReveal: (() => void) | undefined;
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  let proseFrame: number | undefined;
  /** Pending opacity animation for newly arrived prose. */
  let proseRowKey: string | null = null;
  let glideGeneration = 0;
  let gliding = false;
  let panelOpenedManually = false;
  let previousTabOpen = false;
  let virtualWindow: TranscriptVirtualViewport | null = null;
  let navigationDisclosureReleases: Array<() => void> = [];
  let disclosureMotion: {
    pending: Set<symbol>;
    wasFollowing: boolean;
    userScrolledAway: boolean;
    direction: "open" | "close";
    key: TranscriptDisclosureKey | undefined;
  } | undefined;
  const openTailDisclosures = new Set<TranscriptDisclosureKey | undefined>();
  const [streamVersion, setStreamVersion] = createSignal(0);
  const [historyGap, setHistoryGap] = createSignal<() => boolean>(() => false);
  const presentsLiveTail = () => !historyGap()();
  /** Drops older pages cut off by a gap so the live tail is presented again. */
  let closeHistoryGap = () => {};
  const scheduleSave = () => {
    clearTimeout(saveTimer);
    saveTimer = setTimeout(
      () => controller.saveSession(),
      TRANSCRIPT_VIEWPORT_SAVE_IDLE_MS,
    );
  };
  // Jump motion and active search suspend the tail pin.
  const tailPinned = () =>
    following() && !gliding && !findSearchesChat() &&
    stream !== null && canPresentProse(stream);

  const disclosures = createTranscriptDisclosureStore(scheduleSave);

  const readPosition = (): TranscriptReadingPosition | null => {
    if (
      !stream ||
      !canPresentProse(stream) ||
      stream.clientHeight <= 0 ||
      isShellLayoutUnstable()
    ) {
      return null;
    }
    return virtualWindow?.readingPosition() ?? null;
  };

  /** Following stopped while the layout could not be read; the next settle samples it. */
  let positionUnread = false;
  const samplePosition = (): boolean => {
    const current = readPosition();
    if (!current) return false;
    position = current;
    positionUnread = false;
    return true;
  };

  const setFollowing = (next: boolean) => {
    if (following() === next) return;
    setFollowingSignal(next);
    if (next) {
      position = null;
      positionUnread = false;
    } else {
      positionUnread = !samplePosition();
    }
    scheduleSave();
  };

  const cancelProseDelivery = () => {
    if (proseFrame !== undefined) cancelAnimationFrame(proseFrame);
    proseFrame = undefined;
    proseRowKey = null;
  };

  const cancelGlide = () => {
    glideGeneration += 1;
    if (!gliding) return;
    gliding = false;
    if (stream) cancelStreamSpringScroll(stream);
  };

  /** The reader's intent applies at once; an unreadable layout only defers sampling the place. */
  const holdPlace = () => {
    if (disclosureMotion) disclosureMotion.userScrolledAway = true;
    disclosureMotion = undefined;
    openTailDisclosures.clear();
    cancelProseDelivery();
    cancelGlide();
    setFollowing(false);
  };

  const followLatest = () => {
    closeHistoryGap();
    disclosureMotion = undefined;
    openTailDisclosures.clear();
    pendingRestore = null;
    cancelProseDelivery();
    setFollowing(true);
  };

  const applyPendingRestore = () => {
    const target = stream;
    const view = virtualWindow;
    const wanted = pendingRestore;
    if (!target || !view || !wanted) return;
    const offset = view.offsetForPosition(wanted);
    if (offset !== null) {
      commitStreamScroll(target, offset, "restore_anchor");
      // Rows can exist before their scroll extent reaches the DOM.
      const maxScroll = streamTailOffset(target);
      if (maxScroll >= offset - 2 || Math.abs(target.scrollTop - offset) <= 2) {
        pendingRestore = null;
      }
      scheduleSyncStreamScrollFade(target);
      return;
    }
    // History loads once the session has hydrated into a live viewport.
    if (!runtimeBound || restoreLoading) return;
    restoreLoading = true;
    const generation = sessionGeneration;
    void view
      .ensureRowLoaded(wanted.rowKey)
      .catch(() => undefined)
      .then(() => {
        // Session changes invalidate pending loads.
        if (generation !== sessionGeneration) return;
        restoreLoading = false;
        if (pendingRestore !== wanted) return;
        if (view.offsetForPosition(wanted) !== null) {
          applyPendingRestore();
          return;
        }
        // The saved row left the transcript: open at the latest message.
        controller.jumpToTail(false);
      });
  };

  const scheduleProseArrival = (
    change: Extract<TranscriptContentChange, { delivery: "prose" }>,
  ) => {
    cancelProseDelivery();
    const target = stream;
    if (!target) return;
    proseRowKey = change.rowKey;
    proseFrame = requestAnimationFrame(() => {
      proseFrame = undefined;
      const current = proseRowKey;
      proseRowKey = null;
      if (current !== change.rowKey || stream !== target || !following()) return;
      if (!canPresentProse(target)) return;
      const row = proseDeliveryRow(target, change.rowKey);
      if (row) fadeProseArrival(row);
    });
  };

  const scheduleElementReveal = (element: HTMLElement, ensureOpts: Parameters<TranscriptViewportController["ensureVisible"]>[1] = {}, isCurrent: () => boolean = () => true): Promise<boolean> => {
    cancelElementReveal?.();
    const target = stream;
    if (!target || !element.isConnected) return Promise.resolve(false);
    const generation = sessionGeneration;
    return new Promise((resolve) => {
      let destination: number | undefined;
      const cancel = () => {
        cancelScrollportFrame(target, read);
        cancelScrollportFrame(target, commit);
        if (cancelElementReveal === cancel) cancelElementReveal = undefined;
        resolve(false);
      };
      const valid = () => isCurrent() && target === stream && generation === sessionGeneration && element.isConnected;
      const read = () => {
        if (!valid()) { cancel(); return; }
        const viewport = streamVisibleBounds(target);
        const rect = element.getBoundingClientRect();
        const align = ensureOpts.align ?? "nearest";
        let delta = 0;
        if (align === "center") {
          delta =
            rect.top -
            viewport.top -
            (viewport.bottom - viewport.top - rect.height) / 2;
        } else if (align === "end") {
          delta = rect.bottom - viewport.bottom;
        } else if (align === "start") {
          delta = rect.top - viewport.top;
        } else if (rect.top < viewport.top) {
          delta = rect.top - viewport.top;
        } else if (rect.bottom > viewport.bottom) {
          delta = rect.bottom - viewport.bottom;
        }
        // The virtualizer takes the target even at zero delta, retiring an index scroll.
        destination = Math.min(
          streamTailOffset(target),
          Math.max(0, target.scrollTop + delta),
        );
      };
      const commit = () => {
        if (!valid() || destination === undefined) { cancel(); return; }
        const glide = ensureOpts.smooth !== false;
        if (virtualWindow) {
          virtualWindow.scrollToOffset(destination, { glide });
        } else {
          commitStreamScroll(target, destination, "reveal", { glide });
        }
        if (cancelElementReveal === cancel) cancelElementReveal = undefined;
        resolve(true);
      };
      cancelElementReveal = cancel;
      scheduleScrollportFrame(target, "measure", read);
      scheduleScrollportFrame(target, "render", commit);
    });
  };

  const controller: TranscriptViewportController = {
    sessionId: () => normalizedSessionId(opts.sessionId()),
    following,
    presentsLiveTail,
    stream: () => {
      streamVersion();
      return stream;
    },
    disclosures,
    attachStream: (next) => {
      if (stream === next) return;
      disclosureMotion = undefined;
      openTailDisclosures.clear();
      revealSequence++;
      cancelElementReveal?.();
      cancelProseDelivery();
      cancelGlide();
      if (stream) setStreamTailPin(stream, null);
      stream = next;
      setStreamVersion((version) => version + 1);
      if (!next) return;
      setStreamTailPin(next, tailPinned);
      applyPendingRestore();
    },
    activateSession: (sessionId, previousSessionId, preserveCurrent = false) => {
      disclosureMotion = undefined;
      openTailDisclosures.clear();
      const next = normalizedSessionId(sessionId);
      const previous = normalizedSessionId(previousSessionId ?? activeSessionId);
      if (previous && previous !== next) {
        controller.saveSession(previous);
        cancelProseDelivery();
        cancelGlide();
        sessionGeneration += 1;
        pendingRestore = null;
        restoreLoading = false;
      }
      activeSessionId = next;
      controller.clearNavigationDisclosures();
      if (preserveCurrent) {
        controller.saveSession(next);
        return !following();
      }
      const snapshot = transcriptViewportSnapshot(next);
      disclosures.restoreUserOpenKeys(transcriptViewportOpenKeys(snapshot));
      if (!snapshot?.position) {
        position = null;
        setFollowing(true);
        return false;
      }
      setFollowingSignal(false);
      position = snapshot.position;
      positionUnread = false;
      pendingRestore = snapshot.position;
      applyPendingRestore();
      return true;
    },
    saveSession: (requestedSessionId) => {
      const id = normalizedSessionId(requestedSessionId ?? activeSessionId);
      if (!id) return;
      clearTimeout(saveTimer);
      saveTimer = undefined;
      const saved = following() ? null : (pendingRestore ?? position);
      persistTranscriptViewportSnapshot(id, {
        ...(saved ? { position: saved } : {}),
        openKeys: disclosures.userOpenKeys(),
      });
    },
    bindRuntime: (runtime) => {
      const target = stream;
      if (!target) return () => {};
      runtimeBound = true;
      const transcript = () => runtime.appStore.state.transcript;
      setHistoryGap(() => () => transcript().hasTailGap);
      closeHistoryGap = () => {
        if (transcript().hasTailGap) runtime.appStore.actions.resetOlderTranscriptPages();
      };
      // A following reader is at the live tail, so history cut off from it is dropped.
      const stopGapRule = createRoot((dispose) => {
        createEffect(() => {
          if (following() && !presentsLiveTail()) untrack(closeHistoryGap);
        });
        return dispose;
      });
      // Hidden resident surfaces can lose their native scroll offset.
      if (!following() && position) pendingRestore ??= position;
      const onScroll = () => {
        if (following() || pendingRestore) return;
        if (!target.isConnected || !samplePosition()) return;
        scheduleSave();
      };
      const settleLayout = () => {
        if (!target.isConnected || !canPresentProse(target) || target.clientHeight <= 0) return;
        if (following()) {
          commitStreamTail(target, "repin_tail");
          reconcileStreamLayout(target);
        } else if (pendingRestore) {
          applyPendingRestore();
        } else if (positionUnread && samplePosition()) {
          scheduleSave();
        }
      };
      // Presentation can return with no layout change, as when a modal leaves the stage inert.
      let presentationWatch: MutationObserver | undefined;
      const settleWhenPresented = () => {
        if (presentationWatch || !target.isConnected || canPresentProse(target)) return;
        presentationWatch = new MutationObserver(() => {
          if (!canPresentProse(target)) return;
          stopPresentationWatch();
          if (!isShellLayoutUnstable()) settleLayout();
        });
        presentationWatch.observe(target.ownerDocument.documentElement, {
          subtree: true,
          attributeFilter: ["hidden", "inert", "aria-hidden", "data-resident"],
        });
      };
      const stopPresentationWatch = () => {
        presentationWatch?.disconnect();
        presentationWatch = undefined;
      };
      const stops = [
        watchStreamScrollFade(target),
        watchStreamReaderIntent(target, {
          following,
          stopFollowing: holdPlace,
          // The end of history before a gap is not the live tail; newer pages load instead.
          resumeFollowing: () => {
            if (presentsLiveTail()) followLatest();
          },
          onReaderInput: () => {
            if (disclosureMotion) disclosureMotion.userScrolledAway = true;
            disclosureMotion = undefined;
            openTailDisclosures.clear();
            pendingRestore = null;
            panelOpenedManually = false;
          },
        }),
        watchTranscriptLoadMore(target, {
          client: runtime.client,
          appStore: runtime.appStore,
          sessionId: runtime.sessionId,
        }),
        watchStreamTabPanelRetract(target, {
          tabOpen: runtime.tabOpen,
          retracted: runtime.panelRetracted,
          manualOpen: () => panelOpenedManually,
          onRetractedChange: (retracted) => {
            runtime.onPanelRetractedChange(retracted);
            if (retracted) clearStreamTabPanelInset(target);
            suppressStreamScrollEngagement(target, PANEL_RETRACT_ENGAGEMENT_QUIET_MS);
            syncStreamScrollbarLayout(target);
          },
        }),
        watchStreamTabPanelInset(target, {
          tabOpen: runtime.tabOpen,
          retracted: runtime.panelRetracted,
          panelHeightPx: runtime.panelHeightPx,
        }),
        watchStreamGeometry(target, {
          tabOpen: runtime.tabOpen,
          retracted: runtime.panelRetracted,
          panelHeightPx: runtime.panelHeightPx,
          onGeometryChanged: (originChanged) => {
            if (originChanged) virtualWindow?.measureOrigin();
            reconcileStreamLayout(target);
            applyPendingRestore();
            settleWhenPresented();
            // Range changes require an explicit scrollbar measurement.
            syncStreamScrollbarLayout(target);
          },
        }),
        subscribeStreamScroll(target, onScroll),
        onShellLayoutSettled(settleLayout),
        stopPresentationWatch,
      ];
      applyPendingRestore();
      return () => {
        runtimeBound = false;
        stopGapRule();
        setHistoryGap(() => () => false);
        closeHistoryGap = () => {};
        cancelProseDelivery();
        controller.saveSession();
        for (const stop of stops) stop();
      };
    },
    attachVirtualWindow: (view) => {
      if (virtualWindow && virtualWindow !== view) {
        throw new Error("A transcript view can attach only one virtual window.");
      }
      virtualWindow = view;
      view.measureOrigin();
      applyPendingRestore();
      return () => {
        if (virtualWindow === view) virtualWindow = null;
      };
    },
    rowsChanged: () => {
      if (pendingRestore) applyPendingRestore();
    },
    readingDay: () => virtualWindow?.readingDay() ?? null,
    commitReveal: (offset, glide) => {
      const target = stream;
      if (!target) return;
      commitStreamScroll(target, offset, "reveal", { glide });
    },
    shiftVirtualContent: (deltaY, fromOffset) => {
      const target = stream;
      if (!target) return 0;
      return shiftStreamContent(target, deltaY, fromOffset);
    },
    stopFollowing: () => {
      pendingRestore = null;
      holdPlace();
    },
    beginDisclosureMotion: (key, direction) => {
      const target = stream;
      if (!target) return () => {};
      if (direction === "close") {
        scrollportMotionForViewport(target)?.releaseTailRange();
      }
      if (!disclosureMotion) {
        const wasFollowing =
          direction === "close"
            ? following() || isStreamNearBottom(target) || openTailDisclosures.has(key)
            : following() || isStreamNearBottom(target) || openTailDisclosures.size > 0;
        if (direction === "open" && wasFollowing) {
          openTailDisclosures.add(key);
        } else if (direction === "close") {
          openTailDisclosures.delete(key);
        }
        pendingRestore = null;
        if (wasFollowing && direction === "open") {
          cancelProseDelivery();
          cancelGlide();
          setFollowing(false);
        }
        disclosureMotion = {
          pending: new Set(),
          wasFollowing,
          userScrolledAway: false,
          direction,
          key,
        };
      } else if (direction === "open" && disclosureMotion.direction === "close") {
        // An accordion closes siblings before opening the pressed row, so the motion is an expansion.
        disclosureMotion.direction = "open";
        if (disclosureMotion.wasFollowing) {
          openTailDisclosures.add(key);
          cancelProseDelivery();
          cancelGlide();
          setFollowing(false);
        }
      }
      const motion = disclosureMotion;
      const token = Symbol();
      motion.pending.add(token);
      denScrollDebugLog("scroll", "disclosure-motion", {
        key,
        direction,
        phase: "begin",
        offset: target.scrollTop,
        wasFollowing: motion.wasFollowing,
        following: following(),
      });
      return () => {
        if (disclosureMotion !== motion || !motion.pending.delete(token)) return;
        if (motion.pending.size > 0) return;
        // Row measurements publish the final tail before following can resume.
        scheduleScrollportFrame(target, "observe", () => {
          if (disclosureMotion !== motion || motion.pending.size > 0) return;
          disclosureMotion = undefined;
          denScrollDebugLog("scroll", "disclosure-motion", {
            key,
            direction,
            phase: "settled",
            offset: target.scrollTop,
            wasFollowing: motion.wasFollowing,
            userScrolledAway: motion.userScrolledAway,
          });
          if (motion.wasFollowing && !motion.userScrolledAway) {
            const tail = streamTailOffset(target);
            if (motion.direction === "close") {
              followLatest();
              commitStreamTail(target, "repin_tail");
              reconcileStreamLayout(target);
            } else if (tail - target.scrollTop <= 1) {
              followLatest();
            }
          }
        });
      };
    },
    resumeFollowing: () => {
      followLatest();
      if (stream && !gliding) commitStreamTail(stream, "jump");
    },
    jumpToTail: (smooth = true) => {
      // The active tail pin settles layout changes before paint.
      const pinned = following() && !gliding;
      // Closing a gap discards the history a glide would travel through.
      const acrossGap = !presentsLiveTail();
      followLatest();
      const target = stream;
      if (!target) return;
      cancelGlide();
      if (!smooth || pinned || acrossGap || prefersReducedMotion()) {
        commitStreamTail(target, "jump");
        return;
      }
      const generation = glideGeneration;
      gliding = true;
      void glideStreamToTail(target).then((arrived) => {
        if (generation !== glideGeneration) return;
        gliding = false;
        if (stream !== target || !following()) return;
        // The tail pin takes over after the glide.
        if (arrived) reconcileStreamLayout(target);
        // Interrupted glides preserve the reached position unless unstable layout hides it.
        else if (!isShellLayoutUnstable() && !isStreamNearBottom(target)) holdPlace();
      });
    },
    primeTail: (fadeMs = 400) => {
      followLatest();
      const target = stream;
      if (!target) return;
      cancelGlide();
      suppressStreamScrollFade(target, fadeMs);
      commitStreamTail(target, "repin_tail");
      scheduleSyncStreamScrollFade(target);
    },
    contentChanged: (change) => {
      const target = stream;
      if (!target) return;
      if (
        change.delivery === "prose" &&
        change.firstContent &&
        following() &&
        !gliding &&
        proseRowKey !== change.rowKey &&
        canPresentProse(target)
      ) {
        scheduleProseArrival(change);
      }
      scheduleSyncStreamScrollFade(target);
    },
    chromeChanged: ({ tabOpen, panelRetracted, panelHeightPx }) => {
      if (tabOpen && !previousTabOpen) panelOpenedManually = true;
      if (!tabOpen || panelRetracted) panelOpenedManually = false;
      previousTabOpen = tabOpen;
      const target = stream;
      if (!target) return;
      suppressStreamScrollEngagement(target, TAB_PANEL_ENGAGEMENT_QUIET_MS);
      if (!tabOpen || panelRetracted || panelHeightPx <= 0) {
        clearStreamTabPanelInset(target);
      } else {
        applyStreamTabPanelInset(target, panelHeightPx);
      }
      syncStreamScrollbarLayout(target);
      reconcileStreamLayout(target);
      scheduleSyncStreamScrollFade(target);
    },
    revealAnchor: async (anchor, revealOpts = {}) => {
      const id = anchor.anchorId.trim();
      if (!id || revealOpts.signal?.aborted) return false;
      controller.stopFollowing();
      const view = virtualWindow;
      if (!view) return false;
      const revealGeneration = sessionGeneration;
      const sequence = ++revealSequence;
      cancelElementReveal?.();
      await view.ensureAnchorLoaded(anchor);
      if (
        sequence !== revealSequence ||
        sessionGeneration !== revealGeneration ||
        virtualWindow !== view ||
        revealOpts.signal?.aborted
      ) {
        return false;
      }
      let index = resolveRevealIndex(view.items(), anchor);
      // A walk can select an action whose row is still streaming into the transcript.
      const arrivalDeadline = performance.now() + REVEAL_SETTLE_TIMEOUT_MS;
      while (index < 0 && performance.now() < arrivalDeadline) {
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
        if (sequence !== revealSequence || revealOpts.signal?.aborted || sessionGeneration !== revealGeneration || virtualWindow !== view) return false;
        index = resolveRevealIndex(view.items(), anchor);
      }
      if (index < 0) return false;
      const align = revealOpts.align ?? "start";
      const needsMount = !revealOpts.element?.()?.isConnected;
      if (needsMount) {
        view.scrollToIndex(index, { align });
      }
      if (revealOpts.element) {
        const deadline = performance.now() + REVEAL_SETTLE_TIMEOUT_MS;
        let element = revealOpts.element();
        let quietFrames = needsMount ? 0 : 3;
        let previousGeometry = "";
        while ((!element?.isConnected || quietFrames < 3) && performance.now() < deadline) {
          await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
          if (sequence !== revealSequence || revealOpts.signal?.aborted || sessionGeneration !== revealGeneration || virtualWindow !== view) return false;
          element = revealOpts.element();
          const rect = element?.isConnected ? element.getBoundingClientRect() : null;
          const geometry = rect ? `${rect.top}:${rect.height}:${stream ? stream.scrollTop : 0}` : "";
          quietFrames = geometry && geometry === previousGeometry ? quietFrames + 1 : 0;
          previousGeometry = geometry;
        }
        if (!element?.isConnected) return false;
        if (sequence !== revealSequence) return false;
        return scheduleElementReveal(element, { align: align === "auto" ? "nearest" : align, smooth: false }, () => sequence === revealSequence && !revealOpts.signal?.aborted);
      }
      return true;
    },
    setNavigationDisclosures: (keys) => {
      for (const release of navigationDisclosureReleases) release();
      navigationDisclosureReleases = [];
      const lease = Symbol("transcript-navigation");
      for (const key of new Set(keys)) {
        navigationDisclosureReleases.push(disclosures.acquire(key, lease));
      }
    },
    clearNavigationDisclosures: () => {
      revealSequence++;
      cancelElementReveal?.();
      for (const release of navigationDisclosureReleases) release();
      navigationDisclosureReleases = [];
    },
    ensureVisible: (element, ensureOpts = {}) => {
      revealSequence++;
      controller.stopFollowing();
      void scheduleElementReveal(element, ensureOpts);
    },
  };

  return controller;
}

const TranscriptViewportContext = createContext<TranscriptViewportController>();

export function TranscriptViewportProvider(
  props: ParentProps<{ value: TranscriptViewportController }>,
) {
  return (
    <TranscriptViewportContext.Provider value={props.value}>
      {props.children}
    </TranscriptViewportContext.Provider>
  );
}

export function useTranscriptViewport(): TranscriptViewportController | undefined {
  return useContext(TranscriptViewportContext);
}
