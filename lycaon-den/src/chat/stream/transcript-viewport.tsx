import { scheduleScrollportFrame } from "../../platform/scrolling/scrollport-frame.ts";
import type { TranscriptVirtualViewport, TranscriptViewportController } from "./transcript-viewport-types.ts";
import { createTranscriptNavigation } from "./transcript-navigation.ts";
import { createTranscriptDisclosureStore } from "../transcript/presentation/disclosure-state.tsx";
import type { TranscriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";
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
import type { TranscriptReadingPosition } from "../transcript/layout/transcript-virtualizer.ts";
import {
  persistTranscriptViewportSnapshot,
  transcriptViewportOpenKeys,
  transcriptViewportSnapshot,
} from "./transcript-viewport-state.ts";
import { applyStreamTabPanelInset, clearStreamTabPanelInset, commitStreamScroll, commitStreamTail, glideStreamToTail, isStreamNearBottom, reconcileStreamLayout, scheduleSyncStreamScrollFade, setStreamTailPin, shiftStreamContent, streamTailOffset, subscribeStreamScroll, suppressStreamScrollFade, syncStreamScrollbarLayout, watchStreamGeometry, watchStreamScrollFade, watchStreamTabPanelInset, watchStreamTabPanelRetract } from "./stream-scroll.ts";
import { suppressStreamScrollEngagement } from "./reader-intent/reader-state.ts";
import { watchStreamReaderIntent } from "./reader-intent/reader-intent.ts";
import {
  canPresentProse,
  createProseDelivery,
} from "./prose-arrival.ts";

const TRANSCRIPT_VIEWPORT_SAVE_IDLE_MS = 1_200;
/** Tab panel motion moves the offset without reader input. */
const TAB_PANEL_ENGAGEMENT_QUIET_MS = TAB_PANEL_TRANSITION_MS + 80;
const PANEL_RETRACT_ENGAGEMENT_QUIET_MS = 360;

const controllerRegistry = new Map<string, Set<TranscriptViewportController>>();

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
  let saveTimer: ReturnType<typeof setTimeout> | undefined;
  let glideGeneration = 0;
  let gliding = false;
  let panelOpenedManually = false;
  let previousTabOpen = false;
  let virtualWindow: TranscriptVirtualViewport | null = null;
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
  const proseDelivery = createProseDelivery({ stream: () => stream, following });

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
    proseDelivery.cancel();
    cancelGlide();
    setFollowing(false);
  };

  const followLatest = () => {
    closeHistoryGap();
    disclosureMotion = undefined;
    openTailDisclosures.clear();
    pendingRestore = null;
    proseDelivery.cancel();
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

  const navigation = createTranscriptNavigation({
    stream: () => stream, view: () => virtualWindow, generation: () => sessionGeneration,
    stopFollowing: holdPlace, acquireDisclosure: (key, lease) => disclosures.acquire(key, lease),
  });
  const controller: TranscriptViewportController = {
    sessionId: () => opts.sessionId().trim(),
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
      navigation.invalidate();
      proseDelivery.cancel();
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
      const next = (sessionId).trim();
      const previous = (previousSessionId ?? activeSessionId).trim();
      if (previous && previous !== next) {
        controller.saveSession(previous);
        proseDelivery.cancel();
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
      const id = (requestedSessionId ?? activeSessionId).trim();
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
        proseDelivery.cancel();
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
        scrollportMotionForViewport(target)?.tail.releaseTailRange();
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
          proseDelivery.cancel();
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
          proseDelivery.cancel();
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
        !proseDelivery.pending(change.rowKey) &&
        canPresentProse(target)
      ) {
        proseDelivery.schedule(change.rowKey);
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
    revealAnchor: navigation.revealAnchor,
    setNavigationDisclosures: navigation.setNavigationDisclosures,
    clearNavigationDisclosures: navigation.clearNavigationDisclosures,
    ensureVisible: navigation.ensureVisible,
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
