import { createEffect, createSignal } from "solid-js";
import type { RecentSession } from "../../../shared/app-state-types.ts";
import { pendingCheckpointsForSession, sessionWorkers } from "../../chat/actions/chat-actions.ts";
import { reviewPendingApproval } from "../../chat/checkpoint/approval-review.ts";
import { revealChicklet } from "../../chat/transcript/presentation/transcript-reveal.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { flushWorkerTranscriptCoalesce } from "../../chat/worker/worker-transcript-coalesce.ts";
import { latestWorkerForChildSession } from "../../chat/worker/worker-transcript.ts";
import type { OpenWorkerOptions, WorkerDrawerFocus } from "../../chat/worker/workers-model.ts";
import { sessionScope } from "../../notices/notice-scope.ts";
import { noticeReporterFor } from "../../platform/connection/app-connection.ts";
import type { SessionNavigation } from "./session-navigation.ts";
import type { ShellPaneVisibility } from "./shell-pane-visibility.ts";
import type { ShellScope } from "./shell-scope.ts";
import type { ShellSearchNavigation } from "./shell-search-navigation.ts";

type WorkerDrawerDependencies = Pick<ShellScope, "appStore" | "recents" | "activeChat">
  & Pick<ShellPaneVisibility, "revealConversation"> & Pick<SessionNavigation, "resumeSession">
  & Pick<ShellSearchNavigation, "navigateFromSearch" | "revealFromSearch">;

export type ShellWorkerDrawer = ReturnType<typeof createShellWorkerDrawer>;

export function createShellWorkerDrawer({ appStore, recents, activeChat,
  revealConversation, resumeSession, navigateFromSearch, revealFromSearch }: WorkerDrawerDependencies) {
  const [workersOpen, setWorkersOpen] = createSignal(false);
  const [selectedWorkerId, setSelectedWorkerId] = createSignal<string | null>(
    null,
  );
  const [workerDrawerFocus, setWorkerDrawerFocus] =
    createSignal<WorkerDrawerFocus>(null);
  const observeActiveChat = () => createEffect(() => {
    activeChat()?.sessionId;
    flushWorkerTranscriptCoalesce(appStore);
    setWorkersOpen(false);
    setSelectedWorkerId(null);
    setWorkerDrawerFocus(null);
  });

  const openWorkers = (workerId?: string, opts?: OpenWorkerOptions) => {
    const chat = activeChat();
    if (!chat) return;
    setWorkersOpen(true);
    setWorkerDrawerFocus(opts?.scrollTo === "evidence" ? "evidence" : null);
    if (workerId) {
      setSelectedWorkerId(workerId);
      return;
    }
    if (!selectedWorkerId()) {
      const first = sessionWorkers(appStore, chat.sessionId)[0];
      if (first) setSelectedWorkerId(first.id);
    }
  };

  const openNotificationSession = (sessionId: string) => {
    const sid = sessionId.trim();
    if (!sid) return;
    void revealConversation();

    const direct = recents.state.recents.find((r) => r.sessionId === sid);
    if (direct) {
      void resumeSession(direct);
      return;
    }

    const worker = latestWorkerForChildSession(
      appStore.state.workers,
      sid,
    );
    const parentSessionId = worker?.parent_session_id?.trim();
    if (!worker || !parentSessionId) return;

    const projectId =
      worker.project_id?.trim() ||
      recents.state.recents.find((r) => r.sessionId === parentSessionId)
        ?.projectId;
    if (!projectId) return;

    const parentRecent = recents.state.recents.find(
      (r) => r.projectId === projectId && r.sessionId === parentSessionId,
    );
    const row: RecentSession = parentRecent ?? {
      projectId,
      sessionId: parentSessionId,
      title: "",
    };
    const workerId = worker.id;
    void resumeSession(row).then(() => {
      queueMicrotask(() => openWorkers(workerId));
    });
  };

  const closeWorkers = () => {
    setWorkersOpen(false);
  };

  const revealInTranscript = (projectId: string | null, target: TranscriptRevealTarget) => {
    if (!projectId) return;
    void (async () => {
      await revealConversation();
      await navigateFromSearch({
        projectId: projectId, sessionId: target.sessionId,
        reveal: target.workerId || target.checkpointId ? undefined : target.anchor
      });
      if (activeChat()?.sessionId !== target.sessionId) return;
      if (target.checkpointId && pendingCheckpointsForSession(appStore, target.sessionId)
        .some(checkpoint => checkpoint.checkpointId === target.checkpointId)) {
        reviewPendingApproval({ sessionId: target.sessionId, checkpointId: target.checkpointId });
        return;
      }
      if (target.checkpointId && !target.workerId && target.anchor) revealFromSearch(target.anchor, projectId, target.sessionId);
      if (!target.workerId || activeChat()?.sessionId !== target.sessionId) return;
      openWorkers(target.workerId);
      const worker = sessionWorkers(appStore, target.sessionId).find(row => row.id === target.workerId);
      if (target.anchor && worker?.child_session_id) {
        await revealChicklet(() => document.querySelector<HTMLElement>(".den-worker-transcript-pane"),
          { sessionId: worker.child_session_id, anchor: target.anchor }, {
            waitForMount: true,
          isCurrent: () => workersOpen() && selectedWorkerId() === target.workerId && activeChat()?.sessionId === target.sessionId
        });
      }
    })().catch((error) => {
      noticeReporterFor(sessionScope(projectId, target.sessionId)).reportError(error);
    });
  };

  return {
    workersOpen, selectedWorkerId, workerDrawerFocus, observeActiveChat,
    openWorkers, closeWorkers, openNotificationSession, revealInTranscript,
    acknowledgeDrawerFocus: () => setWorkerDrawerFocus(null)
  };
}
