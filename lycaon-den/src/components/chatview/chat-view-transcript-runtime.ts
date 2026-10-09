import { watchTranscriptRuntime } from "../../chat/stream/transcript-runtime.ts";
import { createEffect, createSignal, on, onCleanup, untrack } from "solid-js";
import { focusRegion, isRegionEntry, registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { scrollTargetForRun } from "../../chat/workflow/workflow-spans.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { bindPaintInterestLiveStreams } from "../../chat/stream/paint-interest-live.ts";
import { transcriptDisplayTail, transcriptTailDelivery, isFirstProseDelivery } from "../../chat/transcript/layout/transcript-tail.ts";
import { transcriptViewportForSession } from "../../chat/stream/transcript-viewport.tsx";
import { alignRevealTarget } from "../../chat/transcript/presentation/transcript-reveal.ts";
import { findSearchesChat } from "../../find/find-controller.ts";
import { reconcileStreamLayout } from "../../chat/stream/stream-scroll.ts";
import { lastTranscriptRow } from "../../chat/transcript/presentation/transcript-keyboard.ts";
import type { Accessor } from "solid-js";
import type { ChatViewProps } from "./chat-view-props.ts";
import type { createChatViewActivity } from "./chat-view-activity.ts";
import type { ChatTabChromeValue } from "../../chat/composer/chat-tab-chrome.tsx";
import type { createTranscriptViewportController } from "../../chat/stream/transcript-viewport.tsx";
type Viewport = ReturnType<typeof createTranscriptViewportController>;
type Activity = ReturnType<typeof createChatViewActivity>;

/** Poll budget for a run-jump target that mounts on a later virtualizer pass. */
const RUN_JUMP_RETRY_MS = 60;
const RUN_JUMP_RETRIES = 8;

export function createChatViewTranscript(props: ChatViewProps, viewport: Viewport, chrome: ChatTabChromeValue, activity: Activity, surfaceActive: Accessor<boolean>, resetComposer: () => void) {
  const { workers, spanBlocks } = activity;
  const [streamEl, setStreamEl] = createSignal<HTMLDivElement>();
  const chatFocusClaim = {};

  createEffect(() => {
    const el = streamEl();
    if (el && surfaceActive()) {
      el.tabIndex = -1;
      registerFocusRegion("chat", el, chatFocusClaim);
      // Pointer and scripted focus stay on the stream so paging keys scroll it.
      const onFocus = () => {
        if (!isRegionEntry(el)) return;
        if (!focusRegion("composer")) lastTranscriptRow(el)?.focus({ preventScroll: true });
      };
      el.addEventListener("focus", onFocus);
      onCleanup(() => el.removeEventListener("focus", onFocus));
    } else {
      releaseFocusRegion("chat", chatFocusClaim);
    }
  });
  onCleanup(() => releaseFocusRegion("chat", chatFocusClaim));
  const scrollToRun = (run: import("../../api/types.ts").WorkflowRun) => {
    const target = scrollTargetForRun(run);
    if (!target || !streamEl()) return;
    const viewport = transcriptViewportForSession(props.sessionId);
    if (!viewport) return;
    void viewport.revealAnchor({
      chicklet: "message",
      anchorId: target,
    }, { align: "start" }).then(() => {
      // Alignment waits for the virtual row to mount.
      const attempt = (left: number): void => {
        const host = streamEl();
        if (!host) return;
        const el = host.querySelector<HTMLElement>(`#msg-${target}`);
        if (el) {
          alignRevealTarget(el);
          return;
        }
        if (left > 0) setTimeout(() => attempt(left - 1), RUN_JUMP_RETRY_MS);
      };
      attempt(RUN_JUMP_RETRIES);
    });
  };

  let prevDisplayTail: ReturnType<typeof transcriptDisplayTail> = null;
  let prevSessionId: string | undefined;

  watchTranscriptRuntime({
    active: surfaceActive,
    sessionId: () => props.sessionId,
    currentSessionId: () => props.appStore.state.currentSession?.id,
    hydrating: () => props.appStore.state.chatHydrationLock != null,
    stream: streamEl,
    bind: () => viewport.bindRuntime({
      client: () => getLycaonClient() ?? undefined,
      appStore: props.appStore,
      sessionId: () => props.sessionId,
      tabOpen: () => chrome.openTab() != null,
      panelRetracted: chrome.panelRetracted,
      panelHeightPx: chrome.panelHeightPx,
      onPanelRetractedChange: chrome.setPanelRetracted,
    }),
  });

  // Find holds chat matches still; closing it hands the tail back to following.
  createEffect(
    on(findSearchesChat, (searching) => {
      const host = streamEl();
      if (!searching && host) reconcileStreamLayout(host);
    }, { defer: true }),
  );

  bindPaintInterestLiveStreams({
    appStore: props.appStore,
    active: surfaceActive,
    client: () => getLycaonClient(),
    sessionId: () => props.sessionId,
    hydrationLock: () => props.appStore.state.chatHydrationLock,
    paintedWorker: () => {
      if (props.workersDrawerOpen !== true) return null;
      const workerId = props.selectedWorkerId?.trim();
      if (!workerId) return null;
      const row = workers().find((w) => w.id === workerId);
      const childSessionId = row?.child_session_id?.trim();
      if (!childSessionId) return null;
      return { workerId, childSessionId };
    },
  });


  createEffect(() => {
    viewport.chromeChanged({
      tabOpen: chrome.openTab() != null,
      panelRetracted: chrome.panelRetracted(),
      panelHeightPx: chrome.panelHeightPx(),
    });
  });

  createEffect(() => {
    props.sessionId;
    chrome.setPanelRetracted(false);
    resetComposer();
  });

  createEffect(() => {
    const sessionId = props.sessionId;
    if (props.appStore.state.currentSession?.id !== sessionId) return;
    const tail = transcriptDisplayTail(spanBlocks(), prevDisplayTail);
    const hydrating = props.appStore.state.chatHydrationLock != null;

    if (sessionId !== prevSessionId) {
      // A pending session id can resolve without replacing its transcript.
      const inPlaceIdSwap =
        prevSessionId !== undefined &&
        tail?.key === prevDisplayTail?.key &&
        tail?.itemCount === prevDisplayTail?.itemCount;
      const previousSessionId = prevSessionId;
      prevSessionId = sessionId;
      prevDisplayTail = tail;
      const restored = viewport.activateSession(
        sessionId,
        previousSessionId,
        inPlaceIdSwap,
      );
      if (!inPlaceIdSwap && !restored) {
        viewport.primeTail(600);
      }
      return;
    }

    // Resume hydration lands at the bottom without a growth animation.
    if (hydrating) {
      prevDisplayTail = tail;
      if (untrack(viewport.following)) viewport.primeTail(400);
      return;
    }

    const delivery = transcriptTailDelivery(prevDisplayTail, tail);
    const firstContent = isFirstProseDelivery(prevDisplayTail, tail);
    prevDisplayTail = tail;

    if (!delivery) return;

    const tabOpen = chrome.openTab() != null && !chrome.panelRetracted();
    if (tabOpen && delivery === "prose") {
      return;
    }

    if (delivery === "prose") {
      if (tail) viewport.contentChanged({ delivery, rowKey: tail.key, firstContent });
    } else {
      viewport.contentChanged({ delivery });
    }
  });

  return { streamEl, setStreamEl, scrollToRun };
}
