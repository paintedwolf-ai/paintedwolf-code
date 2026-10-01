import { produce } from "solid-js/store";
import { loadFailed, loaded } from "./load-state.ts";
import { upsertPendingCheckpoint } from "../chat/checkpoint/checkpoint-model.ts";
import { checkpointAppliesToSessionView } from "../chat/checkpoint/checkpoint-session-scope.ts";
import { reconcileWorkersFromBoard } from "./workers-board-sync.ts";
import type { SetStoreFunction } from "solid-js/store";
import type { AppState, AppStoreActions } from "./app-state-model.ts";

export function createSessionCoordinationActions(state: AppState, setState: SetStoreFunction<AppState>): Pick<AppStoreActions,
  "setPendingCheckpoints" | "mergeCheckpoint" | "setBoard" | "setBoardLoad" | "setBoardSnapshot" | "setWorkflowState" | "applyWorkflowRunEvent"> {
  return {
    setPendingCheckpoints(sessionId, sessionViewEpoch, expectedEventEpoch, checkpoints) {
      setState(
        produce((draft) => {
          if (
            draft.currentSession?.id?.trim() !== sessionId.trim() ||
            draft.sessionViewEpoch !== sessionViewEpoch ||
            draft.checkpointEventEpoch !== expectedEventEpoch
          ) {
            return;
          }
          draft.pendingCheckpoints = checkpoints;
        }),
      );
    },
    mergeCheckpoint(event) {
      setState(
        produce((draft) => {
          const currentId = draft.currentSession?.id?.trim();
          if (
            currentId &&
            !checkpointAppliesToSessionView(
              event.session_id,
              currentId,
              draft.workers,
            )
          ) {
            return;
          }
          draft.checkpointEventEpoch++;
          draft.pendingCheckpoints = upsertPendingCheckpoint(
            draft.pendingCheckpoints,
            event,
          );
        }),
      );
    },
    setBoard(board) {
      setState(
        produce((draft) => {
          if (board) {
            const currentSessionId = draft.currentSession?.id?.trim();
            const boardSessionId = board.session_id?.trim();
            if (
              currentSessionId &&
              boardSessionId &&
              boardSessionId !== currentSessionId
            ) {
              return;
            }
          }
          draft.boardEventEpoch++;
          const workerSessionId =
            board?.session_id?.trim() || draft.currentSession?.id?.trim();
          if (workerSessionId) {
            draft.workerEventEpochs[workerSessionId] =
              (draft.workerEventEpochs[workerSessionId] ?? 0) + 1;
          }
          draft.board = board;
          // Board snapshots carry the enriched active run (ui.pending_feedback).
          draft.boardLoad = loaded(true);
          if (board?.active_workflow_run) {
            draft.workflowEventEpoch++;
            draft.activeWorkflowRun = board.active_workflow_run;
          }
          const nextWorkers = reconcileWorkersFromBoard(draft.workers, board);
          if (nextWorkers !== draft.workers) {
            draft.workers = nextWorkers;
          }
        }),
      );
    },
    setBoardLoad(sessionId, epoch, eventEpoch, load) {
      if (state.currentSession?.id !== sessionId || state.sessionViewEpoch !== epoch || state.boardEventEpoch !== eventEpoch) return;
      setState("boardLoad", load);
    },
    setBoardSnapshot(
      sessionId,
      sessionViewEpoch,
      expectedBoardEventEpoch,
      expectedWorkerEventEpoch,
      board,
    ) {
      setState(
        produce((draft) => {
          const sid = sessionId.trim();
          if (
            draft.currentSession?.id?.trim() !== sid ||
            draft.sessionViewEpoch !== sessionViewEpoch ||
            draft.boardEventEpoch !== expectedBoardEventEpoch ||
            (board?.session_id?.trim() &&
              board.session_id.trim() !== sid)
          ) {
            return;
          }
          if ((draft.workerEventEpochs[sid] ?? 0) !== expectedWorkerEventEpoch) {
            draft.boardLoad = loadFailed("Project activity changed during loading.", draft.boardLoad);
            return;
          }
          draft.boardEventEpoch = expectedBoardEventEpoch + 1;
          draft.boardLoad = loaded(true);
          draft.workerEventEpochs[sid] = expectedWorkerEventEpoch + 1;
          draft.board = board;
          if (board?.active_workflow_run) {
            draft.workflowEventEpoch++;
            draft.activeWorkflowRun = board.active_workflow_run;
          }
          const nextWorkers = reconcileWorkersFromBoard(draft.workers, board);
          if (nextWorkers !== draft.workers) {
            draft.workers = nextWorkers;
          }
        }),
      );
    },
    setWorkflowState(sessionId, epoch, input) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id?.trim() !== sessionId.trim() || draft.sessionViewEpoch !== epoch) return;
          draft.workflowEventEpoch++;
          draft.activeWorkflowRun = input.activeWorkflowRun;
          draft.workflowRuns = input.workflowRuns;
          draft.workflowCatalog = input.workflowCatalog;
          draft.blueprints = input.blueprints;
        }),
      );
    },
    applyWorkflowRunEvent(event) {
      const run = event.run;
      const runId = run?.id?.trim();
      if (!run || !runId) return;
      setState(
        produce((draft) => {
          // Runs are session-scoped; a background session's run is not this view's.
          if (draft.currentSession?.id?.trim() !== run.session_id?.trim()) return;
          const index = draft.workflowRuns.findIndex((r) => r.id === runId);
          if (index < 0) {
            draft.workflowRuns = [...draft.workflowRuns, run];
          } else {
            // Revision orders streamed state against an in-flight refetch.
            if ((draft.workflowRuns[index]?.revision ?? 0) > run.revision) return;
            const existing = draft.workflowRuns[index];
            draft.workflowRuns[index] =
              !run.ui && existing?.ui ? { ...run, ui: existing.ui } : run;
          }
          draft.workflowEventEpoch++;
          // Run updates preserve the host-selected active run.
          if (draft.activeWorkflowRun?.id === runId) {
            draft.activeWorkflowRun =
              !run.ui && draft.activeWorkflowRun.ui
                ? { ...run, ui: draft.activeWorkflowRun.ui }
                : run;
          }
        }),
      );
    },
  };
}
