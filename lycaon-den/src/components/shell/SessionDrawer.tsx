import { createMemo } from "solid-js";
import type { Project } from "../../api/types.ts";
import { sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { bindPaintInterestLiveStreams } from "../../chat/stream/paint-interest-live.ts";
import { boardWithWorkerApprovalState } from "../../chat/worker/worker-approval-model.ts";
import { findSessionWorker, type OpenWorkerOptions, type WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { isBackendReachable } from "../../platform/connection/sidecar-status.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { SessionWorkersDrawer } from "../worker/SessionWorkersDrawer.tsx";
import { WorklogPanel } from "../worklog/WorklogPanel.tsx";
import { bindWorklogRefresh } from "../worklog/worklog-panel-state.ts";
import { ContextDrawerHost } from "./ContextDrawer.tsx";
import type { SessionDrawerContent } from "./shell-session-drawer.ts";

export type SessionDrawerProps = {
  appStore: AppStore;
  projects: readonly Project[];
  projectDir: string;
  sessionId: string;
  content: SessionDrawerContent | null;
  selectedWorkerId: string | null;
  workerFocus: WorkerDrawerFocus;
  onWorkerFocusHandled: () => void;
  /** Worker transcripts hydrate while the drawer is closed. */
  backgroundHydrate: boolean;
  onOpenWorker: (workerId?: string, opts?: OpenWorkerOptions) => void;
  onClose: () => void;
};

/** The session's worklog or one worker, in a drawer over the column on screen. */
export function SessionDrawer(props: SessionDrawerProps) {
  const offline = () => !isBackendReachable(props.appStore.state.sidecarStatus);
  const workers = createMemo(() => sessionWorkers(props.appStore, props.sessionId));
  const board = () =>
    boardWithWorkerApprovalState(
      props.appStore.state.board,
      workers(),
      props.appStore.state.pendingCheckpoints,
    );
  const workerOpen = () => props.content === "worker";

  bindWorklogRefresh({
    appStore: props.appStore,
    sessionId: () => props.sessionId,
    offline,
    open: () => props.content === "worklog",
  });

  // The open worker streams live whether or not its conversation is on screen.
  bindPaintInterestLiveStreams({
    appStore: props.appStore,
    client: () => getLycaonClient(),
    hydrationLock: () => props.appStore.state.chatHydrationLock,
    paintedWorker: () => {
      if (!workerOpen()) return null;
      const workerId = props.selectedWorkerId?.trim();
      if (!workerId) return null;
      const row = findSessionWorker(workers(), workerId, props.appStore.state.workers);
      const childSessionId = row?.child_session_id?.trim();
      return row && childSessionId ? { workerId: row.id, childSessionId } : null;
    },
  });

  return (
    <ContextDrawerHost>
      <WorklogPanel
        open={props.content === "worklog"}
        findings={props.appStore.state.findings}
        board={board()}
        workers={workers()}
        messages={props.appStore.state.messages}
        pendingCheckpoints={props.appStore.state.pendingCheckpoints}
        onOpenWorkerCheckpoint={(workerId) => props.onOpenWorker(workerId)}
        onClose={() => props.onClose()}
      />
      <SessionWorkersDrawer
        appStore={props.appStore}
        projects={props.projects}
        projectDir={props.projectDir}
        sessionId={props.sessionId}
        open={workerOpen()}
        selectedId={props.selectedWorkerId}
        scrollToFocus={props.workerFocus}
        onScrollToFocusHandled={props.onWorkerFocusHandled}
        backgroundHydrate={props.backgroundHydrate}
        onClose={() => props.onClose()}
      />
    </ContextDrawerHost>
  );
}
