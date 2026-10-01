import { batch } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { Project, SessionTranscriptPage } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { reconcilePendingOnBaseline } from "../send/pending-sends.ts";
import { syncWorkersFromSessionTranscript } from "../worker/workers-model.ts";
import {
  fetchWorkflowState,
  mergeWorkflowRunsFromMessages,
} from "../workflow/workflow-actions.ts";

/** Install a transcript and its workflow state atomically. */
export async function applySessionTranscriptSnapshot(
  appStore: AppStore,
  client: LycaonClient,
  sessionId: string,
  projectDir: string,
  transcript: SessionTranscriptPage,
  projects: readonly Project[],
): Promise<void> {
  const epoch = appStore.state.sessionViewEpoch;
  const workflowEpoch = appStore.state.workflowEventEpoch;
  const workflow = await fetchWorkflowState(
    appStore,
    client,
    sessionId,
    projectDir,
    projects,
  );
  // The fresh workflow snapshot wins revision conflicts.
  const authoritativeRuns = new Map(
    [
      ...workflow.workflowRuns,
      ...(workflow.activeWorkflowRun ? [workflow.activeWorkflowRun] : []),
    ].map((run) => [run.id, run]),
  );
  const mergedRuns = await mergeWorkflowRunsFromMessages(
    client,
    transcript.messages,
    [...authoritativeRuns.values()],
  );
  if (
    appStore.state.currentSession?.id?.trim() !== sessionId.trim() ||
    appStore.state.sessionViewEpoch !== epoch
  ) {
    return;
  }
  batch(() => {
    if (appStore.state.workflowEventEpoch === workflowEpoch) {
      appStore.actions.setWorkflowState(sessionId, epoch, {
        activeWorkflowRun: workflow.activeWorkflowRun,
        workflowRuns: mergedRuns,
        workflowCatalog: workflow.workflowCatalog,
        blueprints: workflow.blueprints,
      });
    }
    appStore.actions.installTranscriptBaseline(
      sessionId,
      transcript.messages,
      transcript.watermark,
      {
        hasMoreBefore: Boolean(transcript.before_cursor),
        hasMoreAfter: Boolean(transcript.after_cursor),
        turnClocks: Object.values(transcript.turn_clocks),
        turnLoads: Object.values(transcript.turn_loads).flat(),
      },
    );
    syncWorkersFromSessionTranscript(appStore, sessionId, transcript.messages);
    appStore.actions.clearLlmTurn(sessionId);
    // Baseline rows reconcile echoes missed while disconnected.
    reconcilePendingOnBaseline(appStore, sessionId);
  });
}
