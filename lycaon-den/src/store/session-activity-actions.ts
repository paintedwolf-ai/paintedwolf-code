import { produce } from "solid-js/store";
import type { SetStoreFunction } from "solid-js/store";
import type { AppState, AppStoreActions, PromptSubmissionHold, SessionActivity } from "./app-state-model.ts";

function heldSubmissions(state: AppState, sessionId: string): readonly PromptSubmissionHold[] {
  return state.sessionActivity[sessionId]?.promptSubmissions ?? [];
}

function updateSubmissions(
  draft: AppState,
  sessionId: string,
  update: (held: readonly PromptSubmissionHold[]) => PromptSubmissionHold[],
): void {
  mutateSessionActivity(draft, sessionId, (entry) => {
    const next = update(entry.promptSubmissions ?? []);
    entry.promptSubmissions = next.length > 0 ? next : undefined;
  });
}

/** Upsert live session activity and remove empty entries. */
export function mutateSessionActivity(
  draft: AppState,
  sessionId: string,
  apply: (entry: SessionActivity) => void,
): void {
  const entry: SessionActivity = { ...draft.sessionActivity[sessionId] };
  apply(entry);
  if (
    !entry.llmTurn &&
    !entry.promptSubmissions?.length &&
    !entry.stopping &&
    Object.keys(entry.activities ?? {}).length === 0
  ) {
    delete draft.sessionActivity[sessionId];
    return;
  }
  draft.sessionActivity[sessionId] = entry;
}

export function createSessionActivityActions(state: AppState, setState: SetStoreFunction<AppState>): Pick<AppStoreActions,
  "clearLlmTurn" | "holdPromptSubmission" | "admitPromptSubmission" | "releasePromptSubmissionsThrough" | "releasePromptSubmission" | "setSessionStopping" | "clearSessionActivity" | "addPendingSend" | "patchPendingSend" | "removePendingSends" | "setLLMCallStatus" | "setActivity"> {
  return {
    clearLlmTurn(sessionId) {
      const id = sessionId.trim();
      if (!id) return;
      setState(
        produce((draft) => {
          if (!draft.sessionActivity[id]) return;
          mutateSessionActivity(draft, id, (entry) => {
            entry.llmTurn = undefined;
          });
        }),
      );
    },
    holdPromptSubmission(sessionId, submissionId) {
      const id = sessionId.trim();
      const submission = submissionId.trim();
      if (!id || !submission || heldSubmissions(state, id).some((held) => held.id === submission)) return;
      setState(
        produce((draft) => {
          updateSubmissions(draft, id, (held) => [...held, { id: submission }]);
        }),
      );
    },
    admitPromptSubmission(sessionId, submissionId, revision, appliedRevision) {
      const id = sessionId.trim();
      if (!id || !heldSubmissions(state, id).some((held) => held.id === submissionId)) return;
      const reflected = revision <= 0 || (appliedRevision !== undefined && appliedRevision > revision);
      setState(
        produce((draft) => {
          updateSubmissions(draft, id, (held) =>
            reflected
              ? held.filter((hold) => hold.id !== submissionId)
              : held.map((hold) => (hold.id === submissionId ? { id: hold.id, revision } : hold)),
          );
        }),
      );
    },
    releasePromptSubmissionsThrough(sessionId, revision) {
      const id = sessionId.trim();
      const reflects = (hold: PromptSubmissionHold) => hold.revision !== undefined && hold.revision < revision;
      if (!id || !heldSubmissions(state, id).some(reflects)) return;
      setState(
        produce((draft) => {
          updateSubmissions(draft, id, (held) => held.filter((hold) => !reflects(hold)));
        }),
      );
    },
    releasePromptSubmission(sessionId, submissionId) {
      const id = sessionId.trim();
      if (!id || !heldSubmissions(state, id).some((held) => held.id === submissionId)) return;
      setState(
        produce((draft) => {
          updateSubmissions(draft, id, (held) => held.filter((hold) => hold.id !== submissionId));
        }),
      );
    },
    setSessionStopping(sessionId, stopping) {
      const id = sessionId.trim();
      if (!id) return;
      setState(
        produce((draft) => {
          mutateSessionActivity(draft, id, (entry) => {
            entry.stopping = stopping || undefined;
          });
        }),
      );
    },
    addPendingSend(sessionId, entry) {
      const id = sessionId.trim();
      if (!id || !entry.operationId.trim()) return;
      setState(
        produce((draft) => {
          draft.pendingSends[id] = [...(draft.pendingSends[id] ?? []), { ...entry, createdAt: Date.now() }];
        }),
      );
    },
    patchPendingSend(sessionId, operationId, patch) {
      const id = sessionId.trim();
      if (!id) return;
      setState(
        produce((draft) => {
          const list = draft.pendingSends[id];
          if (!list) return;
          const idx = list.findIndex((e) => e.operationId === operationId);
          if (idx < 0) return;
          list[idx] = { ...list[idx]!, ...patch };
        }),
      );
    },
    removePendingSends(sessionId, operationIds) {
      const id = sessionId.trim();
      if (!id || operationIds.length === 0) return;
      setState(
        produce((draft) => {
          const list = draft.pendingSends[id];
          if (!list) return;
          const drop = new Set(operationIds);
          const kept = list.filter((e) => !drop.has(e.operationId));
          if (kept.length === list.length) return;
          if (kept.length === 0) delete draft.pendingSends[id];
          else draft.pendingSends[id] = kept;
        }),
      );
    },
    clearSessionActivity(sessionId) {
      const id = sessionId.trim();
      if (!id) return;
      setState(
        produce((draft) => {
          delete draft.sessionActivity[id];
        }),
      );
    },
    setLLMCallStatus(event) {
      const sessionId = event.session_id?.trim();
      if (!sessionId) return;
      setState(
        produce((draft) => {
          // Background calls do not repaint foreground summaries.
          const foreground = draft.currentSession?.id === sessionId;
          if (event.coordinator_loop && foreground) {
            draft.coordinatorLoopProgress = event.coordinator_loop;
          }
          if (event.status === "active") {
            mutateSessionActivity(draft, sessionId, (entry) => {
              entry.llmTurn = {
                callId: event.call_id,
                status: "active",
                provider: event.provider?.trim() || undefined,
                // Missing attempt metadata clears retry state.
                retry: event.attempt
                  ? {
                      attempt: event.attempt,
                      maxAttempts: event.max_attempts ?? event.attempt,
                      reason: event.retry_reason,
                      silenceMs: event.silence_ms,
                    }
                  : undefined,
                guarded: event.coordinator_loop?.guarded ?? false,
                provisionalHidden:
                  event.coordinator_loop?.provisional_hidden ?? false,
              };
            });
          } else if (event.status === "ok") {
            mutateSessionActivity(draft, sessionId, (entry) => {
              if (entry.llmTurn) {
                entry.llmTurn = { ...entry.llmTurn, status: "done" };
              }
            });
            // The latest prompt updates foreground context occupancy.
            if (event.tokens?.prompt && foreground) {
              draft.contextUsage = {
                prompt: event.tokens.prompt,
                window: event.tokens.context_window ?? 0,
                compactionThreshold: event.tokens.compaction_threshold ?? 0,
              };
            }
          } else if (event.status === "error") {
            if (event.coordinator_loop && foreground) {
              draft.coordinatorLoopProgress = undefined;
            }
            mutateSessionActivity(draft, sessionId, (entry) => {
              if (entry.llmTurn) {
                entry.llmTurn = { ...entry.llmTurn, status: "error" };
              }
            });
          }
        }),
      );
    },
    setActivity(event) {
      const sessionId = event.session_id?.trim();
      const activityId = event.activity_id?.trim();
      if (!sessionId || !activityId) return;
      setState(
        produce((draft) => {
          mutateSessionActivity(draft, sessionId, (entry) => {
            const activities = { ...(entry.activities ?? {}) };
            if (event.status === "active") {
              activities[activityId] = event;
            } else {
              delete activities[activityId];
            }
            entry.activities =
              Object.keys(activities).length > 0 ? activities : undefined;
          });
        }),
      );
    },
  };
}
