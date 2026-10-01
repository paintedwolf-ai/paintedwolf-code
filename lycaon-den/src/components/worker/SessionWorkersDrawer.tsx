import { createEffect, createMemo, onCleanup, untrack } from "solid-js";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { refreshSessionWorkers } from "../../chat/actions/board-actions.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import { flushWorkerTranscriptCoalesce } from "../../chat/worker/worker-transcript-coalesce.ts";
import {
  hydrateWorkerTranscripts,
  workersColdCacheRevision,
  workersNeedingTranscriptHydrate,
} from "../../chat/worker/worker-transcript.ts";
import { type WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { createCoalescedAsyncScheduler } from "../../store/coalesced-async.ts";
import type { Project } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { WorkersDrawer } from "./WorkersDrawer.tsx";

// Debounce background hydration across status updates.
const CLOSED_HYDRATE_COALESCE_MS = 200;

// Retry selected runs whose transcript is not durable yet.
const EMPTY_HYDRATE_RETRY_MS = [300, 1000, 2500] as const;

type Props = {
  appStore: AppStore;
  projects: readonly Project[];
  projectDir: string;
  sessionId: string;
  open: boolean;
  selectedId: string | null;
  scrollToFocus?: WorkerDrawerFocus;
  onScrollToFocusHandled?: () => void;
  /** Enables background transcript hydration. */
  backgroundHydrate?: boolean;
  onClose: () => void;
};

/** Conversation-scoped worker panel. */
export function SessionWorkersDrawer(props: Props) {
  // Read open state through a Solid owner before asynchronous hydration.
  const open = createMemo(() => props.open);
  let disposed = false;
  const offline = () => !isBackendReachable(props.appStore.state.sidecarStatus);

  const workers = createMemo(() => sessionWorkers(props.appStore, props.sessionId));

  const coldHydrateRevision = createMemo(() =>
    workersColdCacheRevision(
      props.appStore.state.messages,
      workers(),
      props.appStore.state.workerTranscripts,
      open() ? props.selectedId : null,
    ),
  );

  let hydrateGate: Promise<void> = Promise.resolve();
  let emptyRetryAttempt = 0;
  let emptyRetryTimer: ReturnType<typeof setTimeout> | undefined;

  const clearEmptyRetry = () => {
    if (emptyRetryTimer !== undefined) clearTimeout(emptyRetryTimer);
    emptyRetryTimer = undefined;
  };

  const resetEmptyRetry = () => {
    clearEmptyRetry();
    emptyRetryAttempt = 0;
  };

  const selectedWorkerStillCold = (): boolean => {
    const id = props.selectedId?.trim();
    if (!id || !open()) return false;
    return workersNeedingTranscriptHydrate(
      props.appStore.state.messages,
      workers(),
      props.appStore.state.workerTranscripts,
      { selectedWorkerId: id },
    ).some((row) => row.id === id);
  };

  const scheduleEmptyRetryIfNeeded = () => {
    clearEmptyRetry();
    if (!selectedWorkerStillCold()) {
      emptyRetryAttempt = 0;
      return;
    }
    if (emptyRetryAttempt >= EMPTY_HYDRATE_RETRY_MS.length) return;
    const delay = EMPTY_HYDRATE_RETRY_MS[emptyRetryAttempt];
    emptyRetryAttempt += 1;
    emptyRetryTimer = setTimeout(() => {
      emptyRetryTimer = undefined;
      void runTranscriptHydrate();
    }, delay);
  };

  const runTranscriptHydrate = () => {
    hydrateGate = hydrateGate
      .then(async () => {
        const client = getLycaonClient();
        if (disposed || !client || offline()) return;
        const list = workers();
        if (list.length === 0) return;
        await hydrateWorkerTranscripts(
          client,
          props.appStore.state.messages,
          list,
          props.appStore.state.workerTranscripts,
          (workerId, page) => {
            if (!disposed) props.appStore.actions.installWorkerTranscriptTail(workerId, page);
          },
          {
            selectedWorkerId: open() ? props.selectedId : null,
            limit: open() ? 4 : 8,
          },
        );
        if (!disposed) scheduleEmptyRetryIfNeeded();
      })
      .catch(() => undefined);
    return hydrateGate;
  };

  const debouncedClosedHydrate = createCoalescedAsyncScheduler(
    () => runTranscriptHydrate(),
    CLOSED_HYDRATE_COALESCE_MS,
  );

  onCleanup(() => {
    disposed = true;
    debouncedClosedHydrate.cancel();
    resetEmptyRetry();
  });

  createEffect((wasOpen) => {
    props.sessionId;
    const isOpen = open();
    if (isOpen && !wasOpen) {
      debouncedClosedHydrate.cancel();
      flushWorkerTranscriptCoalesce(props.appStore);
      resetEmptyRetry();
      const client = getLycaonClient();
      if (client && !offline()) {
        void (async () => {
          // Refresh the run association before hydration.
          await refreshSessionWorkers(
            props.appStore,
            client,
            props.projectDir,
            props.projects,
            props.sessionId,
          ).catch(() => undefined);
          await runTranscriptHydrate();
        })();
      }
    } else if (!isOpen && wasOpen) {
      flushWorkerTranscriptCoalesce(props.appStore);
      resetEmptyRetry();
    }
    return isOpen;
  }, false);

  createEffect(
    (prev?: { open: boolean; selected: string | null; rev: string }) => {
      props.sessionId;
      const isOpen = open();
      const selected = props.selectedId ?? null;
      const backgroundHydrate = props.backgroundHydrate;
      const rev = coldHydrateRevision();
      // Keep cache reads outside the effect dependency set.
      untrack(() => {
        if (isOpen) {
          const opened = prev != null && !prev.open;
          const selectedChanged = prev != null && prev.selected !== selected;
          const revChanged = prev != null && prev.rev !== rev;
          if (selectedChanged) resetEmptyRetry();
          // The open effect refreshes the roster first.
          if (!opened && (selectedChanged || revChanged)) {
            void runTranscriptHydrate();
          }
          return;
        }
        flushWorkerTranscriptCoalesce(props.appStore);
        if (backgroundHydrate === true) {
          debouncedClosedHydrate.schedule();
        } else {
          debouncedClosedHydrate.cancel();
        }
      });
      return { open: isOpen, selected, rev };
    },
  );

  return (
    <WorkersDrawer
      open={open()}
      appStore={props.appStore}
      projectDir={props.projectDir}
      workers={workers()}
      selectedId={props.selectedId}
      scrollToFocus={props.scrollToFocus}
      onScrollToFocusHandled={props.onScrollToFocusHandled}
      onClose={() => props.onClose()}
    />
  );
}
