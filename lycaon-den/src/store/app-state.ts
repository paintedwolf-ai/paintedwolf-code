import { createStore, produce } from "solid-js/store";
import { projectMatchesDir } from "../api/project-path.ts";
import { isEnterableSessionStatus } from "../api/session-status.generated.ts";
import { replayBufferedChildMessageEvents } from "../chat/transcript/projection/worker-child-message-buffer.ts";
import { replayForegroundBufferedMessages } from "../chat/transcript/projection/message-events.ts";
import type { Project, QueueDraft, WorkerTask } from "../api/types.ts";
import { WorkerTranscriptRetention } from "../chat/worker/worker-transcript.ts";
import { loadFailed, loaded, unloaded, valueOf } from "./load-state.ts";
import { reconcilePendingOnQueueDraft } from "../chat/send/pending-sends.ts";
import { pendingFromEvent } from "../chat/checkpoint/checkpoint-model.ts";
import { clearTranscriptEntryMemory, noteTranscriptEntryBaseline, transcriptEntryKeysFromMessages } from "../chat/transcript/presentation/transcript-entry.ts";
import { clearVisualArtifactSessionMemory, noteVisualArtifactIntroBaseline } from "../chat/visual/visual-artifact-reveal.ts";
import { mergeTranscriptBaseline } from "../chat/transcript/projection/transcript-baseline-merge.ts";
import { appendToTail, applyTranscriptPage, copyTranscriptWindow, emptyTranscriptWindow, installTailWindow, materializeTranscriptWindow, mergeIntoTail, patchRowInWindow, precedesLiveTail, refreshTailWindow, resetOlderPages, transcriptWindowPage } from "../chat/transcript/layout/transcript-window.ts";
import { clearDraftVersionCache } from "../chat/draft/draft-version-cache.ts";
import { clearChatContentCache } from "../chat/transcript/content/chat-content-reader.ts";
import { messagesEqual } from "../chat/transcript/projection/messages-equal.ts";
import { mergeTurnClocks } from "../chat/session/turn-clock.ts";
import { mergeTurnLoads } from "../chat/session/turn-load.ts";
import { latestMessageSnapshot, messageLiveFieldsEqual } from "../chat/transcript/projection/messages-equal.ts";
import { mergedWorkerProgressFromEvent, reconcileSessionWorkersFromList, workerStoreFingerprint, workerTasksSliceEqual } from "../chat/worker/workers-model.ts";
import { createSessionActivityActions, mutateSessionActivity } from "./session-activity-actions.ts";
import { createSessionCoordinationActions } from "./session-coordination-actions.ts";
import { createGitCacheActions } from "./git-cache-actions.ts";
import { type AppState, type AppStoreActions, type AppStore } from "./app-state-model.ts";

export const INITIAL_APP_STATE: Readonly<AppState> = {
  messages: [],
  transcript: emptyTranscriptWindow(),
  transcriptWatermark: 0,
  turnClocks: {},
  turnLoads: {},
  chatHydrationBuffer: [],
  blueprints: [],
  workers: [],
  workerEventEpochs: {},
  workerTranscripts: {},
  pendingCheckpoints: [],
  checkpointEventEpoch: 0,
  boardEventEpoch: 0,
  workflowEventEpoch: 0,
  boardLoad: unloaded(),
  workflowRuns: [],
  workflowCatalog: [],
  sessionActivity: {},
  pendingSends: {},
  sessionViewEpoch: 0,
  findings: unloaded(),
  gitStatus: unloaded(),
  sidecarStatus: "connecting",
  isLoading: false,
  verifyDetectRevision: 0,
  extensionsRevision: 0,
  approvalsRevision: 0,
  modelPolicyRevision: 0,
  projectTrustRevision: 0,
};

export function createAppStore(
  initial: Readonly<AppState> = INITIAL_APP_STATE,
): AppStore {
  const [state, setState] = createStore<AppState>(
    structuredClone(initial) as AppState,
  );
  let progressRevisionEpoch = initial.sessionViewEpoch;
  let findingsRevisionEpoch = initial.sessionViewEpoch;
  const queueDrafts = new Map<string, QueueDraft>();
  if (initial.currentSession && initial.queueDraft) {
    queueDrafts.set(initial.currentSession.id.trim(), initial.queueDraft);
  }

  const rememberQueueDraft = (sessionId: string, draft: QueueDraft): QueueDraft => {
    const current = queueDrafts.get(sessionId);
    if (current && current.revision >= draft.revision) return current;
    queueDrafts.set(sessionId, draft);
    return draft;
  };

  const workerRetention = new WorkerTranscriptRetention();
  const trimWorkerTranscripts = (workerId: string) => {
    const evicted = workerRetention.evictions(state.workerTranscripts, workerId);
    if (evicted.length) setState("workerTranscripts", produce(cache => { for (const id of evicted) delete cache[id]; }));
  };
  const actions: AppStoreActions = {
    ...createSessionActivityActions(state, setState),
    ...createSessionCoordinationActions(state, setState),
    ...createGitCacheActions(setState),
    setSidecarStatus(status) {
      setState("sidecarStatus", status);
    },
    setLoading(isLoading) {
      setState("isLoading", isLoading);
    },
    installSessionBootstrap(bootstrap, sessionOverride) {
      const session = sessionOverride ?? bootstrap.session;
      const sid = session.id.trim();
      const previousQueue = queueDrafts.get(sid);
      const queue = rememberQueueDraft(sid, bootstrap.queue);
      setState(
        produce((draft) => {
          draft.sessionViewEpoch++;
          draft.currentSession = session;
          progressRevisionEpoch = draft.sessionViewEpoch;
          findingsRevisionEpoch = draft.sessionViewEpoch;
          draft.progress = bootstrap.progress;
          draft.turnClock = bootstrap.turn_clock;
          draft.findings = loaded(bootstrap.findings ?? null);
          draft.queueDraft = queue;
          draft.coordinatorRunContext = bootstrap.coordinator;
          mutateSessionActivity(draft, sid, (entry) => {
            entry.activities = bootstrap.activities?.length
              ? Object.fromEntries(bootstrap.activities.map((activity) => [activity.activity_id, activity]))
              : undefined;
            if (session.status !== "busy") entry.llmTurn = undefined;
          });
          const replacedWorkerIds = new Set(
            draft.workers
              .filter((worker) => worker.parent_session_id?.trim() === sid)
              .map((worker) => worker.id),
          );
          const otherWorkers = draft.workers.filter(
            (worker) => worker.parent_session_id?.trim() !== sid,
          );
          draft.workers = [...otherWorkers, ...bootstrap.workers];
          const keptWorkerIds = new Set(
            bootstrap.workers.map((worker) => worker.id),
          );
          for (const workerId of replacedWorkerIds) {
            if (!keptWorkerIds.has(workerId)) {
              delete draft.workerTranscripts[workerId];
            }
          }
          draft.pendingCheckpoints = bootstrap.checkpoints.map(pendingFromEvent);
        }),
      );
      // A session switch drops pending sends, so a bootstrap at the remembered
      // revision still has to seat the reserved head.
      const unseated = queue.sending &&
        !state.pendingSends[sid]?.some((entry) => entry.kind === "queue_send");
      if (!previousQueue || queue.revision > previousQueue.revision || unseated) {
        reconcilePendingOnQueueDraft({ state, actions }, sid, queue);
      }
      replayForegroundBufferedMessages({ state, actions }, session.id);
    },
    setWorkers(workers) {
      setState("workers", workers);
    },
    mergeSessionWorkers(sessionId, workers, expectedEventEpoch) {
      const sid = sessionId.trim();
      if (!sid) return false;
      let applied = false;
      setState(
        produce((draft) => {
          if ((draft.workerEventEpochs[sid] ?? 0) !== expectedEventEpoch) {
            return;
          }
          draft.workerEventEpochs[sid] = expectedEventEpoch + 1;
          applied = true;
          const existingSlice = draft.workers.filter(
            (w) => w.parent_session_id?.trim() === sid,
          );
          const merged = reconcileSessionWorkersFromList(existingSlice, workers);
          if (workerTasksSliceEqual(existingSlice, merged)) return;
          const dropped = existingSlice.map((w) => w.id);
          const rest = draft.workers.filter(
            (w) => w.parent_session_id?.trim() !== sid,
          );
          draft.workers = [...rest, ...merged];
          const kept = new Set(workers.map((w) => w.id));
          for (const id of dropped) {
            if (!kept.has(id)) {
              delete draft.workerTranscripts[id];
            }
          }
        }),
      );
      return applied;
    },
    applyWorkerTranscriptRows(workerId, rows) {
      if (!workerId || rows.length === 0) return;
      setState(
        "workerTranscripts",
        produce((cache) => {
          const entry = cache[workerId];
          if (!entry) {
            let window = emptyTranscriptWindow();
            for (const row of rows) window = appendToTail(window, row);
            cache[workerId] = { window, rows: materializeTranscriptWindow(window), hydrated: false };
            return;
          }
          const positions = new Map(entry.rows.map((row, index) => [row.id, index]));
          let window = entry.window;
          let appended = false;
          for (const row of rows) {
            const id = row.id?.trim();
            if (!id) continue;
            const index = positions.get(id);
            if (index !== undefined) {
              const existing = entry.rows[index]!;
              if (latestMessageSnapshot(existing, row) === existing) continue;
              if (messageLiveFieldsEqual(existing, row)) continue;
              patchRowInWindow(window, row);
              entry.rows[index] = row;
              continue;
            }
            // Nonresident history stays durable until a page brings it back.
            if (window.hasMoreAfter || precedesLiveTail(window, row)) continue;
            window = appendToTail(window, row);
            appended = true;
          }
          if (!appended) return;
          entry.window = window;
          entry.rows = materializeTranscriptWindow(window);
        }),
      );
      trimWorkerTranscripts(workerId);
    },
    installWorkerTranscriptTail(workerId, page) {
      // An empty page leaves the run eligible for another hydration.
      if (!workerId || (page.messages?.length ?? 0) === 0) return;
      setState(
        "workerTranscripts",
        produce((cache) => {
          const window = refreshTailWindow(cache[workerId]?.window ?? emptyTranscriptWindow(), transcriptWindowPage(page));
          cache[workerId] = { window, rows: materializeTranscriptWindow(window), hydrated: true };
        }),
      );
      trimWorkerTranscripts(workerId);
    },
    loadWorkerTranscriptPage(workerId, request, page) {
      let applied = false;
      setState(
        "workerTranscripts",
        produce((cache) => {
          const entry = cache[workerId];
          const window = entry && applyTranscriptPage(entry.window, request, transcriptWindowPage(page));
          if (!entry || !window) return;
          entry.window = window;
          entry.rows = materializeTranscriptWindow(window);
          applied = true;
        }),
      );
      if (applied) trimWorkerTranscripts(workerId);
      return applied;
    },
    retainWorkerTranscript(workerId) {
      return workerRetention.retain(workerId);
    },
    invalidateSessionViewRequests() {
      setState("sessionViewEpoch", state.sessionViewEpoch + 1);
      queueDrafts.clear();
    },
    noteSessionSeen(session) {
      setState(
        produce((draft) => {
          const current = draft.currentSession;
          if (!current || current.id !== session.id) return;
          current.seen_at = session.seen_at;
        }),
      );
    },
    setCurrentSession(session) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id !== session?.id) draft.sessionViewEpoch++;
          draft.currentSession = session;
          draft.queueDraft = queueDrafts.get(session?.id.trim() ?? "");
        }),
      );
      const sid = session?.id?.trim();
      // Replay events buffered while this session was in the background.
      if (sid) {
        replayForegroundBufferedMessages({ state, actions }, sid);
      }
    },
    installTranscriptBaseline(sessionId, messages, watermark, baseline) {
      // Only initial hydration establishes the reveal baseline.
      const isFreshHydration =
        state.transcriptSessionId !== sessionId || state.messages.length === 0;
      const page = {
        messages,
        hasMoreBefore: baseline?.hasMoreBefore ?? false,
        hasMoreAfter: baseline?.hasMoreAfter ?? false,
      };
      const merged = mergeTranscriptBaseline(sessionId, messages, watermark, {
        transcriptSessionId: state.transcriptSessionId,
        messages: state.messages,
        transcriptWatermark: state.transcriptWatermark,
      });
      const nextWatermark = merged.sessionChanged
        ? watermark
        : Math.max(state.transcriptWatermark, watermark);
      const nextTurnClocks = mergeTurnClocks(
        merged.sessionChanged ? {} : state.turnClocks,
        baseline?.turnClocks ?? [],
      );
      const nextTurnLoads = mergeTurnLoads(
        merged.sessionChanged ? {} : state.turnLoads,
        baseline?.turnLoads ?? [],
      );
      if (
        !merged.sessionChanged &&
        state.transcriptSessionId === sessionId &&
        messagesEqual(state.messages, merged.messages) &&
        nextWatermark === state.transcriptWatermark &&
        nextTurnClocks === state.turnClocks &&
        nextTurnLoads === state.turnLoads
      ) {
        return;
      }
      setState(
        produce((draft) => {
          if (merged.sessionChanged) {
            draft.transcriptWatermark = 0;
            draft.transcript = installTailWindow(emptyTranscriptWindow(), page, {
              resetPages: true,
            });
          } else {
            draft.transcript = mergeIntoTail(
              draft.transcript,
              page,
              watermark,
              draft.messages,
            );
          }
          draft.messages = materializeTranscriptWindow(draft.transcript);
          draft.transcriptSessionId = sessionId;
          draft.transcriptWatermark = nextWatermark;
          draft.turnClocks = nextTurnClocks;
          draft.turnLoads = nextTurnLoads;
        }),
      );
      // Reconciliation preserves the live reveal baseline.
      if (isFreshHydration) {
        noteTranscriptEntryBaseline(
          sessionId,
          transcriptEntryKeysFromMessages(merged.messages, nextTurnLoads),
        );
        noteVisualArtifactIntroBaseline(merged.messages);
      }
    },
    loadTranscriptPage(request, page) {
      const sessionId = state.transcriptSessionId?.trim();
      let applied = false;
      setState(
        produce((draft) => {
          const next = applyTranscriptPage(draft.transcript, request, transcriptWindowPage(page));
          if (!next) return;
          draft.transcript = next;
          draft.messages = materializeTranscriptWindow(next);
          draft.turnClocks = mergeTurnClocks(draft.turnClocks, Object.values(page.turn_clocks));
          draft.turnLoads = mergeTurnLoads(draft.turnLoads, Object.values(page.turn_loads).flat());
          applied = true;
        }),
      );
      // Paged history enters the reveal baseline immediately.
      if (applied && sessionId && page.messages.length > 0) {
        noteTranscriptEntryBaseline(
          sessionId,
          transcriptEntryKeysFromMessages(page.messages, state.turnLoads),
        );
      }
      return applied;
    },
    resetOlderTranscriptPages() {
      setState(
        produce((draft) => {
          draft.transcript = resetOlderPages(draft.transcript);
          draft.messages = materializeTranscriptWindow(draft.transcript);
        }),
      );
    },
    upsertMessage(message) {
      const id = message.id?.trim();
      if (!id) return false;
      const seq = message.seq ?? 0;
      let applied = false;
      setState(
        produce((draft) => {
          // New rows compare against the preceding watermark.
          const watermarkBeforeUpsert = draft.transcriptWatermark;
          if (seq > draft.transcriptWatermark) {
            draft.transcriptWatermark = seq;
          }
          const idx = draft.messages.findIndex((m) => m.id === id);
          if (idx >= 0) {
            const existing = draft.messages[idx]!;
            // Events are full-row snapshots; the newest seq is the row.
            if (latestMessageSnapshot(existing, message) === existing) return;
            if (messageLiveFieldsEqual(existing, message)) {
              // Sequence and ordinal changes do not invalidate rendered content.
              patchRowInWindow(draft.transcript, message);
              draft.messages[idx] = message;
              return;
            }
            if (!patchRowInWindow(draft.transcript, message)) {
              // Missing sparse rows cannot be appended at the live tip.
              return;
            }
            // Keep the existing array slot.
            draft.messages[idx] = message;
            applied = true;
            return;
          }
          // New ids at or below the baseline watermark are stale.
          if (seq > 0 && seq <= watermarkBeforeUpsert) return;
          // Unknown older rows return with their paged transcript window.
          if (precedesLiveTail(draft.transcript, message)) return;
          draft.transcript = appendToTail(draft.transcript, message);
          draft.messages = materializeTranscriptWindow(draft.transcript);
          applied = true;
        }),
      );
      return applied;
    },
    bufferHydrationMessageEvent(event) {
      setState("chatHydrationBuffer", (prev) => [...prev, event]);
    },
    takeHydrationBuffer() {
      const buffered = state.chatHydrationBuffer;
      if (buffered.length > 0) {
        setState("chatHydrationBuffer", []);
      }
      return buffered;
    },
    clearChatForSessionSwitch(hydrationLock) {
      clearTranscriptEntryMemory();
      clearVisualArtifactSessionMemory();
      clearDraftVersionCache();
      clearChatContentCache();
      setState(
        produce((draft) => {
          draft.sessionViewEpoch++;
          draft.messages = [];
          draft.transcript = emptyTranscriptWindow();
          draft.transcriptSessionId = undefined;
          draft.transcriptWatermark = 0;
          draft.turnClocks = {};
          draft.turnLoads = {};
          draft.chatHydrationBuffer = [];
          draft.workerTranscripts = {};
          // Project exit ends every session event stream.
          draft.sessionActivity = {};
          draft.pendingSends = {};
          draft.contextUsage = undefined;
          draft.progress = undefined;
          draft.turnClock = undefined;
          draft.gitStatus = unloaded();
          draft.gitRepos = undefined;
          draft.gitActiveRepoId = undefined;
          draft.gitScopePin = undefined;
          draft.queueDraft = undefined;
          draft.findings = unloaded();
          draft.board = undefined;
          draft.boardLoad = unloaded();
          draft.boardEventEpoch = 0;
          draft.pendingCheckpoints = [];
          draft.checkpointEventEpoch = 0;
          draft.activeWorkflowRun = undefined;
          draft.workflowRuns = [];
          draft.coordinatorRunContext = undefined;
          // Clear the outgoing session before the next foreground arrives.
          draft.currentSession = undefined;
          draft.chatHydrationLock = hydrationLock?.trim() || undefined;
        }),
      );
    },
    beginSessionResumeSwitch(hydrationLock) {
      setState(
        produce((draft) => {
          draft.sessionViewEpoch++;
          // Hydration replaces chat slices; companion slices stay until that lands.
          draft.chatHydrationBuffer = [];
          draft.pendingSends = {};
          draft.chatHydrationLock = hydrationLock?.trim() || undefined;
        }),
      );
    },
    completeChatSessionHydration() {
      setState("chatHydrationLock", undefined);
    },
    resetChatForSessionSwitch() {
      clearTranscriptEntryMemory();
      clearVisualArtifactSessionMemory();
      setState("sessionViewEpoch", state.sessionViewEpoch + 1);
      setState("messages", []);
      setState("transcript", emptyTranscriptWindow());
      setState("transcriptSessionId", undefined);
      setState("transcriptWatermark", 0);
      setState("turnClocks", {});
      setState("turnLoads", {});
      setState("chatHydrationBuffer", []);
      // Replace keyed record slices atomically.
      setState(
        produce((draft) => {
          draft.workerTranscripts = {};
          draft.sessionActivity = {};
          draft.pendingSends = {};
        }),
      );
      setState("contextUsage", undefined);
      setState("turnClock", undefined);
      setState("currentSession", undefined);
      setState("pendingCheckpoints", []);
      setState("checkpointEventEpoch", 0);
      setState("activeWorkflowRun", undefined);
      setState("workflowRuns", []);
      setState("workflowCatalog", []);
      setState("blueprints", []);
      setState("coordinatorRunContext", undefined);
      setState("latestCodeScan", undefined);
      setState("board", undefined);
      setState("boardEventEpoch", 0);
      setState("chatHydrationLock", undefined);
    },
    setFindings(sessionId, epoch, digest) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id?.trim() !== sessionId.trim() || draft.sessionViewEpoch !== epoch) return;
          const previous = valueOf(draft.findings);
          if (findingsRevisionEpoch === epoch && digest && previous && digest.revision < previous.revision) return;
          findingsRevisionEpoch = epoch;
          draft.findings = loaded(digest ?? null);
        }),
      );
    },
    setFindingsLoadFailed(sessionId, epoch, err) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id?.trim() !== sessionId.trim() || draft.sessionViewEpoch !== epoch) return;
          // A failed refresh preserves the displayed digest.
          draft.findings = loadFailed(err, draft.findings);
        }),
      );
    },
    setTurnLoad(event) {
      setState(
        produce((draft) => {
          if (draft.transcriptSessionId !== event.session_id.trim()) return;
          draft.turnLoads = mergeTurnLoads(draft.turnLoads, [event]);
        }),
      );
    },
    setTurnClock(event) {
      setState(
        produce((draft) => {
          const sessionId = event.session_id.trim();
          if (draft.transcriptSessionId === sessionId) {
            draft.turnClocks = mergeTurnClocks(draft.turnClocks, [event]);
          }
          // Background clocks do not repaint the foreground.
          if (draft.currentSession?.id !== sessionId) return;
          draft.turnClock = event;
        }),
      );
    },
    setProgress(sessionId, epoch, digest) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id?.trim() !== sessionId.trim() || draft.sessionViewEpoch !== epoch) return;
          if (progressRevisionEpoch === epoch && digest && draft.progress && digest.revision < draft.progress.revision) return;
          progressRevisionEpoch = epoch;
          draft.progress = digest;
        }),
      );
    },
    setQueueDraft(sessionId, epoch, queueDraft) {
      const sid = sessionId.trim();
      if (state.currentSession?.id.trim() !== sid || state.sessionViewEpoch !== epoch) return false;
      const current = queueDrafts.get(sid);
      if (current && current.revision >= queueDraft.revision) return false;
      queueDrafts.set(sid, queueDraft);
      setState(
        produce((s) => {
          s.queueDraft = queueDraft;
        }),
      );
      return true;
    },
    bumpVerifyDetectRevision() {
      setState("verifyDetectRevision", state.verifyDetectRevision + 1);
    },
    bumpExtensionsRevision() {
      setState("extensionsRevision", state.extensionsRevision + 1);
    },
    bumpApprovalsRevision() {
      setState("approvalsRevision", state.approvalsRevision + 1);
    },
    bumpModelPolicyRevision() {
      setState("modelPolicyRevision", state.modelPolicyRevision + 1);
    },
    bumpProjectTrustRevision() {
      setState("projectTrustRevision", state.projectTrustRevision + 1);
    },
    setCoordinatorRunContext(sessionId, epoch, ctx) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id?.trim() !== sessionId.trim() || draft.sessionViewEpoch !== epoch) return;
          draft.coordinatorRunContext = ctx;
        }),
      );
    },
    setSessionTitle(sessionId, title) {
      const nextTitle = title.trim();
      if (!nextTitle || state.currentSession?.id !== sessionId) return;
      setState("currentSession", "title", nextTitle);
    },
    mergeSession(event) {
      setState(
        produce((draft) => {
          if (draft.currentSession?.id !== event.id) return;
          // A snapshot can land after the update that superseded it. A status
          // the machine cannot re-enter proves that, so only the status is dropped.
          const status = isEnterableSessionStatus(event.status)
            ? event.status
            : draft.currentSession.status;
          draft.currentSession = {
            ...draft.currentSession,
            status,
            ...(event.title?.trim() ? { title: event.title.trim() } : {}),
            ui: event.ui ?? draft.currentSession.ui,
            ...(typeof event.untrusted_content === "boolean"
              ? { untrusted_content: event.untrusted_content }
              : {}),
            ...(typeof event.current_turn === "number"
              ? { current_turn: event.current_turn }
              : {}),
            // The host omits a false prompt_pending.
            prompt_pending: event.prompt_pending === true,
            updated_at: new Date().toISOString(),
          };
          // host_error is published by SSE dispatch (any session), not here.
          if (status === "idle") {
            draft.coordinatorLoopProgress = undefined;
            // Host leases span idle gaps and close on their own done events.
            mutateSessionActivity(draft, event.id, (entry) => {
              entry.llmTurn = undefined;
            });
          }
        }),
      );
    },
    updateWorker(event) {
      const taskId = (
        event.worker_id ??
        (event as unknown as { task_id?: string }).task_id
      )?.trim();
      if (!taskId) return;
      let boundChildSessionId: string | undefined;
      setState(
        produce((draft) => {
          const idx = draft.workers.findIndex((w) => w.id === taskId);
          const parentSessionId =
            event.parent_session_id?.trim() ||
            (idx >= 0 ? draft.workers[idx]?.parent_session_id?.trim() : "");
          if (parentSessionId) {
            draft.workerEventEpochs[parentSessionId] =
              (draft.workerEventEpochs[parentSessionId] ?? 0) + 1;
          }
          if (idx >= 0) {
            const row = draft.workers[idx]!;
            const mergeStatus = event.merge_status ?? row.merge_status;
            const nextChild =
              event.child_session_id?.trim() || row.child_session_id?.trim();
            if (
              nextChild &&
              nextChild !== row.child_session_id?.trim()
            ) {
              boundChildSessionId = nextChild;
            }
            const next: WorkerTask = {
              ...row,
              status: event.status,
              agent_type: event.agent_type?.trim() || row.agent_type,
              child_session_id: nextChild || undefined,
              parent_session_id:
                event.parent_session_id?.trim() || row.parent_session_id,
              brief: event.brief?.trim() || row.brief,
              merge_status: mergeStatus,
              dependencies: event.dependencies ?? row.dependencies,
              result: event.result ?? row.result,
              failure: event.failure ?? row.failure,
              error: event.error ?? row.error,
              ...mergedWorkerProgressFromEvent(row, event),
              // Job events project the whole row, so absence means answered.
              budget_request: event.budget_request,
              context_usage: event.context_usage ?? row.context_usage,
              workspace_preparation:
                event.workspace_preparation ??
                (event.status === "running"
                  ? row.workspace_preparation
                  : undefined),
            };
            if (workerStoreFingerprint(row) === workerStoreFingerprint(next)) {
              return;
            }
            draft.workers[idx] = next;
            return;
          }
          const child = event.child_session_id?.trim();
          if (child) boundChildSessionId = child;
          draft.workers.push({
            id: taskId,
            agent_type: event.agent_type?.trim() || "worker",
            status: event.status,
            child_session_id: child || undefined,
            parent_session_id: event.parent_session_id?.trim() || undefined,
            brief: event.brief?.trim() || undefined,
            merge_status: event.merge_status,
            dependencies: event.dependencies,
            result: event.result,
            failure: event.failure,
            error: event.error,
            max_tool_loops: event.max_tool_loops,
            budget_request: event.budget_request,
            tool_loops_used: event.tool_loops_used,
            tool_calls_used: event.tool_calls_used,
            turn_tool_calls: event.turn_tool_calls,
            turn_tools_done: event.turn_tools_done,
            context_usage: event.context_usage,
            workspace_preparation: event.workspace_preparation,
            created_at: new Date().toISOString(),
          });
        }),
      );
      if (boundChildSessionId) {
        replayBufferedChildMessageEvents(
          { state, actions },
          boundChildSessionId,
        );
      }
    },
    addCodeScan(event) {
      // Identical summaries can announce newly committed finding history.
      setState(produce((draft) => { draft.latestCodeScan = { ...event }; }));
    },
    clearLatestCodeScan() {
      setState("latestCodeScan", undefined);
    },
    restoreSessionChatSnapshot(snapshot) {
      const sessionId = snapshot.scope.sessionId.trim();
      const transcript = copyTranscriptWindow(snapshot.transcript);
      const messages = materializeTranscriptWindow(transcript);
      noteTranscriptEntryBaseline(
        sessionId,
        transcriptEntryKeysFromMessages(messages, mergeTurnLoads({}, snapshot.turnLoads)),
      );
      noteVisualArtifactIntroBaseline(messages);
      setState(
        produce((draft) => {
          draft.currentSession = snapshot.session;
          draft.transcript = transcript;
          draft.messages = messages;
          draft.transcriptSessionId = sessionId;
          draft.transcriptWatermark = snapshot.transcriptWatermark;
          draft.turnClocks = mergeTurnClocks({}, snapshot.turnClocks);
          draft.turnLoads = mergeTurnLoads({}, snapshot.turnLoads);
          draft.activeWorkflowRun = snapshot.activeWorkflowRun;
          draft.workflowRuns = snapshot.workflowRuns.slice();
          draft.workflowCatalog = snapshot.workflowCatalog.slice();
          draft.blueprints = snapshot.blueprints.slice();
          draft.pendingCheckpoints = snapshot.pendingCheckpoints.slice();
          // Cached snapshots do not replace live activity.
          draft.contextUsage = undefined;
          const rest = draft.workers.filter(
            (w) => w.parent_session_id?.trim() !== sessionId,
          );
          draft.workers = [...rest, ...snapshot.workers];
          for (const id of Object.keys(draft.workerTranscripts)) {
            const worker = draft.workers.find((w) => w.id === id);
            if (worker?.parent_session_id?.trim() === sessionId) {
              delete draft.workerTranscripts[id];
            }
          }
          // Restored runs hydrate again; the snapshot may predate their newest rows.
          for (const [id, entry] of Object.entries(snapshot.workerTranscripts)) {
            const window = copyTranscriptWindow(entry.window);
            draft.workerTranscripts[id] = { window, rows: materializeTranscriptWindow(window), hydrated: false };
          }
        }),
      );
    },
  };

  return { state, actions };
}

/** Map local project path to registry UUID for SSE + scoped GETs. */

export function projectIdForPath(
  projects: readonly Project[],
  projectDir: string,
): string | undefined {
  return projects.find((p) => projectMatchesDir(p, projectDir))?.id;
}
