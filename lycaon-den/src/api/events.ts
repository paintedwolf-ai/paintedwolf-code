import { traceSourceViewDelivery } from "./source-view-event-trace.ts";
import {
  clearSessionHostErrorNotices,
  publishSessionHostError,
} from "../notices/notice-store.ts";
import type { BackendConnection } from "../platform/connection/backend.ts";
import type { AppStoreActions } from "../store/app-state-model.ts";
import { createStoreBoardCoalescer } from "../store/board-sync.ts";
import {
  buildEventsUrl,
  parseEventEnvelope,
  readAuthenticatedSSE,
} from "./events-sse.ts";
import { applyMessageEvent } from "../chat/transcript/projection/message-events.ts";
import { sweepPendingOnIdle } from "../chat/send/pending-sends.ts";
import {
  createEventDeliveryQueue,
  type EventDeliveryQueue,
} from "./event-delivery-queue.ts";
import { applyBackgroundProcessEvent } from "../chat/tool/background-process-store.ts";
import { applyPreviewEvent } from "../chat/visual/preview-store.ts";
import { applyChatVault } from "../chat/vault/chat-vault-store.ts";
import { invalidateInvocationRecordings } from "../chat/visual/invocation-recording-store.ts";
import { normalizeWorkerEvent } from "../chat/worker/workers-model.ts";
import type { AppStore, SidecarStatus } from "../store/app-state-model.ts";
import type { EventPayloadMap } from "./event-payloads.generated.ts";
import type { EventEnvelope, EventScope, EventTopic } from "./types.ts";
import { isSidecarEstablished } from "../platform/connection/sidecar-status.ts";
import { applyTurnOutcome } from "../chat/recovery/turn-outcome.ts";
import { invalidateApprovalGrantsCache } from "../settings/security/approval-grants-cache.ts";
import { LycaonApiError } from "./http.ts";
import { resetSessionSnapshotReads, sessionSnapshotReadFence } from "./session-snapshot-refresh.ts";
import { forgetSessionRevisions, sessionRevisionWatermarks } from "./session-event-revisions.ts";

/** Starts a new backend connection generation for this store. */
export function resetSessionEventRevisions(storeActions: AppStoreActions): void {
  forgetSessionRevisions(storeActions);
  resetSessionSnapshotReads(storeActions);
}

/** Idle stream heartbeat. */
const SSE_HEARTBEAT_PREFIX = ": ping";

/** Fallback REST refetch when SSE is disconnected. */
const SSE_FALLBACK_REFETCH_MS = 120_000;

/** AppState slices invalidated when a topic event arrives. */
export type StoreInvalidation =
  | "session"
  | "projects"
  | "blueprints"
  | "workflows"
  | "workers"
  | "board"
  | "cost"
  | "scan"
  | "mcp"
  | "providers"
  | "model_policy"
  | "settings"
  | "findings"
  | "progress"
  | "queue"
  | "readiness";

export const TOPIC_STORE_INVALIDATION: Readonly<
  Record<EventTopic, readonly StoreInvalidation[]>
> = {
  session: ["session"],
  message: [],
  // Board reconciliation controls merge_status.
  worker: [],
  checkpoint: ["session"],
  // The coalescer applies board snapshots.
  board: [],
  // A leg update moves the run's projected topology legs.
  delegation: ["board", "workflows"],
  cost: ["cost"],
  llm: ["session"],
  // The topic handler replaces one chat's presence.
  agent_presence: [],
  activity: [],
  oar: [],
  scan: ["scan"],
  providers: ["providers"],
  model_policy: ["model_policy"],
  // Settings dispatch by facet.
  settings: [],
  // Refetch reconciles workflow catalog changes.
  workflow: ["workflows", "session"],
  grounding: ["session"],
  findings: ["findings"],
  progress: ["progress"],
  queue: ["queue"],
  project: ["projects"],
  process: [],
  preview: [],
  // The payload contains the complete attention view.
  attention: [],
  // The handler controls project opening.
  cli_open: [],
  // The gallery reloads artifact metadata.
  artifact: [],
  // Topic handlers update inventory and buffers.
  source_changed: [],
  // The handler applies the request's state to the file operation store.
  source_operation: [],
  source_view: [],
  file_briefing: [],
  // Topic handlers update document caches.
  editor_document: [],
  // Clients tick locally between clock edges.
  turn_clock: [],
  chat_vault: [],
  // The handler folds the receipt into the transcript's turn loads.
  turn_load: [],
  preflight: ["readiness"],
};

export type EventHandler<T> = (event: T, scope: EventScope) => void;

export type TopicHandlers = {
  [K in EventTopic]?: EventHandler<EventPayloadMap[K]>;
};

export interface EventSubscriptionOptions {
  /** Full store for transcript message patches (topic message). */
  appStore?: AppStore;
  /** Patch store from topic payloads. */
  storeActions?: AppStoreActions;
  /** Called with invalidated store keys. */
  onInvalidate?: (keys: readonly StoreInvalidation[], scope: EventScope) => void;
  /** Per-facet settings dispatch: receives the event's area. */
  onSettingsEvent?: (event: EventPayloadMap["settings"], scope: EventScope) => void;
  /** Override default SSE_FALLBACK_REFETCH_MS. */
  fallbackRefetchMs?: number;
  onReconnectAttempt?: (attempt: number, delayMs: number) => void;
  onOpen?: (reason: EventOpenReason) => void;
  onError?: (err: unknown) => void;
  /** Rebuild authoritative state after an event-stream gap. */
  onReconcile?: (reason: EventReconcileReason) => void | Promise<void>;
  /** Inject stream reader for tests. */
  connect?: (
    connection: BackendConnection,
    projectId: string,
    signal: AbortSignal,
    after?: string,
  ) => AsyncGenerator<{ data?: string; comment?: string }>;
}

export type EventReconcileReason = "disconnected" | "replay_unavailable";
export type EventOpenReason = "initial" | "resume" | "reconnect";

export type EventSubscription = {
  close: () => Promise<void>;
  url: string;
  /** Resolves when the stream first connects. */
  ready: Promise<void>;
  /** Skip reconnect backoff — e.g. when the window regains focus. */
  wakeReconnect: () => void;
  /** Rebuild the stream from a snapshot boundary. */
  resumeAfter: (cursor: string) => void;
};

function waitReconnectDelay(
  ms: number,
  registerWake: (wake: () => void) => void,
): Promise<void> {
  if (ms <= 0) return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      registerWake(() => undefined);
      resolve();
    }, ms);
    registerWake(() => {
      clearTimeout(timer);
      resolve();
    });
  });
}

function replayBoundary(err: unknown): string | undefined {
  if (
    !(err instanceof LycaonApiError) ||
    err.code !== "event_replay_unavailable"
  ) {
    return undefined;
  }
  const cursor = err.details?.event_cursor;
  return typeof cursor === "string" ? cursor.trim() : "";
}

/** Capped exponential reconnect delay. */
export function reconnectDelayMs(attempt: number): number {
  return Math.min(60_000, 1000 * 2 ** Math.min(attempt, 6));
}

/** Failed reconnects allowed before reporting disconnection. */
export const SSE_DISCONNECTED_AFTER_ATTEMPTS = 4;

/** Status for a reconnect loop with consecutive failures. */
export function reconnectStatus(
  attempt: number,
  established: boolean,
): SidecarStatus {
  if (attempt >= SSE_DISCONNECTED_AFTER_ATTEMPTS) return "disconnected";
  if (attempt === 0 && !established) return "connecting";
  return "reconnecting";
}

/** Returns true for SSE comment heartbeats (`: ping\\n\\n`). */
export function isHeartbeatComment(data: string): boolean {
  return data.startsWith(SSE_HEARTBEAT_PREFIX);
}

function dispatchTopic(
  envelope: EventEnvelope,
  handlers: TopicHandlers,
  storeActions?: AppStoreActions,
  appStore?: AppStore,
): void {
  const handler = handlers[envelope.topic];
  if (handler) {
    (handler as EventHandler<unknown>)(envelope.data, envelope.scope);
  }
  if (!storeActions) return;
  switch (envelope.topic) {
    case "session": {
      const session = envelope.data;
      if (session.action === "deleted") break;
      storeActions.mergeSession(session);
      if (!session.host_error && session.idle_disposition === "completed") {
        // Completed turns clear their session-scoped host errors.
        clearSessionHostErrorNotices(session.id);
      }
      // Idle reaps unmatched pending sends after the transcript flushes.
      if (appStore && session.status === "idle") {
        sweepPendingOnIdle(appStore, session.id);
      }
      applyTurnOutcome(session);
      break;
    }
    case "worker":
      storeActions.updateWorker(
        normalizeWorkerEvent(
          envelope.data,
          appStore?.state.currentSession?.id,
        ),
      );
      break;
    case "scan":
      storeActions.addCodeScan(envelope.data);
      break;
    case "llm":
      storeActions.setLLMCallStatus(envelope.data);
      break;
    case "activity":
      storeActions.setActivity(envelope.data);
      break;
    // Runs apply before coalesced message references.
    case "workflow":
      storeActions.applyWorkflowRunEvent(envelope.data);
      break;
    case "chat_vault":
      applyChatVault(envelope.data);
      break;
    case "turn_clock":
      storeActions.setTurnClock(envelope.data);
      break;
    case "turn_load":
      storeActions.setTurnLoad(envelope.data);
      break;
    case "checkpoint":
      if (envelope.data.kind === "tool_approval" && envelope.data.status === "approved") {
        invalidateApprovalGrantsCache();
        storeActions.bumpApprovalsRevision();
      }
      storeActions.mergeCheckpoint(envelope.data);
      break;
    case "message":
      if (appStore) applyMessageEvent(appStore, envelope.data);
      break;
    default:
      break;
  }
}

/** Subscribe to host events with topic dispatch and invalidation. */
export function subscribeEvents(
  connection: BackendConnection,
  projectId: string,
  handlers: TopicHandlers = {},
  options: EventSubscriptionOptions = {},
): EventSubscription {
  const url = buildEventsUrl(connection, projectId);
  let closed = false;
  let attempt = 0;
  let afterCursor = "";
  let runVersion = 0;
  let wakeDelay: (() => void) | undefined;
  const boardCoalescer = options.storeActions
    ? createStoreBoardCoalescer(options.storeActions)
    : undefined;
  const revisionContext = options.storeActions ?? options.appStore?.actions ?? handlers;
  const sessionRevisions = sessionRevisionWatermarks(revisionContext);
  const snapshotReads = sessionSnapshotReadFence(revisionContext);
  const seenEventIds = new Set<string>();
  const seenEventOrder: string[] = [];
  let controller = new AbortController();
  const streams = new Set<Promise<void>>();
  let fallbackTimer: ReturnType<typeof setTimeout> | undefined;
  let reconciliation: Promise<void> | undefined;
  let streamConnected = false;
  let hasOpened = false;
  let resolveReady!: () => void;
  const ready = new Promise<void>((resolve) => {
    resolveReady = resolve;
  });
  let readyResolved = false;
  const fallbackDelay = Math.max(1, options.fallbackRefetchMs ?? SSE_FALLBACK_REFETCH_MS);

  const settleReady = () => {
    if (readyResolved) return;
    readyResolved = true;
    resolveReady();
  };

  const delivered = (eventId: string): boolean =>
    eventId !== "" && seenEventIds.has(eventId);

  const markDelivered = (eventId: string) => {
    if (!eventId || seenEventIds.has(eventId)) return;
    seenEventIds.add(eventId);
    seenEventOrder.push(eventId);
    if (seenEventOrder.length > 4096) {
      const expired = seenEventOrder.shift();
      if (expired) seenEventIds.delete(expired);
    }
  };

  const reconcile = (reason: EventReconcileReason): Promise<void> => {
    if (!options.onReconcile) return Promise.resolve();
    if (reconciliation) return reconciliation;
    reconciliation = Promise.resolve(options.onReconcile(reason)).finally(() => {
      reconciliation = undefined;
    });
    return reconciliation;
  };

  const stopFallback = () => {
    if (fallbackTimer !== undefined) clearTimeout(fallbackTimer);
    fallbackTimer = undefined;
  };

  const armFallback = () => {
    if (
      closed ||
      streamConnected ||
      !options.onReconcile ||
      fallbackTimer !== undefined
    ) return;
    const runFallback = async () => {
      fallbackTimer = undefined;
      if (closed) return;
      try {
        await reconcile("disconnected");
      } catch (err) {
        options.onError?.(err);
      }
      armFallback();
    };
    // runFallback handles its own rejection.
    fallbackTimer = setTimeout(() => void runFallback(), fallbackDelay);
  };

  const applyEnvelope = (envelope: EventEnvelope) => {
    traceSourceViewDelivery(envelope, "applying");
    if (envelope.topic === "session") {
      const session = envelope.data;
      // An error is independently useful even when its snapshot is superseded.
      if (session.host_error) {
        publishSessionHostError(session.host_error, session.project_id, session.id);
      }
      const revision = envelope.entity_revision;
      if (revision !== undefined) {
        const previous = sessionRevisions.get(session.id);
        if (previous !== undefined && revision < previous) return;
        sessionRevisions.set(session.id, revision);
      }
      snapshotReads.invalidate(session.id);
    }
    if (envelope.topic === "settings") {
      options.onSettingsEvent?.(envelope.data, envelope.scope);
    }
    if (envelope.topic === "process") {
      applyBackgroundProcessEvent(envelope.data);
    }
    if (envelope.topic === "preview") {
      applyPreviewEvent(envelope.data);
    }
    if (envelope.topic === "artifact" && envelope.data.session_id) {
      invalidateInvocationRecordings(envelope.data.session_id);
    }
    if (envelope.topic === "board") {
      const boardEvent = envelope.data;
      boardCoalescer?.schedule(boardEvent);
      const handler = handlers.board;
      if (handler) handler(boardEvent, envelope.scope);
    } else {
      dispatchTopic(envelope, handlers, options.storeActions, options.appStore);
    }
    if (envelope.topic === "session" && envelope.entity_revision !== undefined) {
      // Applied state now reflects every prompt admitted below this revision.
      options.storeActions?.releasePromptSubmissionsThrough(envelope.data.id, envelope.entity_revision);
    }
    options.onInvalidate?.(TOPIC_STORE_INVALIDATION[envelope.topic], envelope.scope);
    traceSourceViewDelivery(envelope, "applied");
  };

  // Rendering may coalesce and sort; replay follows the original arrival prefix.
  const deliveryQueue: EventDeliveryQueue = createEventDeliveryQueue(applyEnvelope, {
    onApplied: (envelopes) => {
      for (const envelope of envelopes) markDelivered(envelope.event_id);
    },
    onCheckpoint: (cursor) => { afterCursor = cursor; },
    onApplyError: (err) => reopenAfterFailedApply(err),
  });

  const connectFn =
    options.connect ??
    ((conn, pid, signal, after) =>
      readAuthenticatedSSE(conn, pid, signal, after));

  const established = () =>
    isSidecarEstablished(options.appStore?.state.sidecarStatus ?? "disconnected");

  /** Count one failure, then wait out its backoff before reopening. */
  const backoffBeforeReconnect = async () => {
    attempt += 1;
    options.storeActions?.setSidecarStatus(
      reconnectStatus(attempt, established()),
    );
    const delay = reconnectDelayMs(attempt);
    options.onReconnectAttempt?.(attempt, delay);
    await waitReconnectDelay(delay, (wake) => {
      wakeDelay = wake;
    });
    wakeDelay = undefined;
  };

  const run = async (
    version: number,
    signal: AbortSignal,
    initialOpenReason: EventOpenReason,
  ) => {
    let openReason = initialOpenReason;
    while (!closed && version === runVersion) {
      try {
        if (openReason === "initial") {
          if (!established()) options.storeActions?.setSidecarStatus("connecting");
        } else if (openReason === "reconnect") {
          options.storeActions?.setSidecarStatus(
            reconnectStatus(attempt, established()),
          );
        }
        for await (const msg of connectFn(
          connection,
          projectId,
          signal,
          afterCursor,
        )) {
          if (closed || version !== runVersion) return;
          if (msg.comment !== undefined) {
            if (isHeartbeatComment(`: ${msg.comment}`)) continue;
            if (msg.comment === "connected") {
              if (reconciliation) await reconciliation.catch(() => undefined);
              if (closed || version !== runVersion || signal.aborted) return;
              streamConnected = true;
              hasOpened = true;
              settleReady();
              stopFallback();
              attempt = 0;
              options.storeActions?.setSidecarStatus("connected");
              options.onOpen?.(openReason);
            }
            continue;
          }
          if (!msg.data) continue;
          const envelope = parseEventEnvelope(msg.data);
          if (!envelope) continue;
          traceSourceViewDelivery(envelope, "received");
          // Replay advances only through the fully applied arrival prefix.
          deliveryQueue.enqueue(envelope, delivered(envelope.event_id));
        }
        if (closed || version !== runVersion) return;
        throw new Error("SSE stream ended");
      } catch (caught) {
        if (closed || version !== runVersion || signal.aborted) return;
        streamConnected = false;
        armFallback();
        let err = caught;
        const boundary = replayBoundary(err);
        if (boundary !== undefined && options.onReconcile) {
          try {
            await reconcile("replay_unavailable");
            if (closed || version !== runVersion || signal.aborted) return;
            deliveryQueue.cancel();
            sessionRevisions.clear();
            snapshotReads.clear();
            seenEventIds.clear();
            seenEventOrder.length = 0;
            afterCursor = boundary;
            attempt = 0;
            openReason = "reconnect";
            continue;
          } catch (recoveryErr) {
            err = recoveryErr;
          }
        }
        options.onError?.(err);
        openReason = "reconnect";
        await backoffBeforeReconnect();
      }
    }
  };

  const startRun = (...args: Parameters<typeof run>): Promise<void> => {
    const stream = run(...args);
    streams.add(stream);
    void stream.then(() => streams.delete(stream), () => streams.delete(stream));
    return stream;
  };

  /** Reconnect from the last applied envelope to replay the failed update. */
  function reopenAfterFailedApply(
    err: unknown,
  ): void {
    if (closed) return;
    deliveryQueue.cancel();
    streamConnected = false;
    armFallback();
    options.onError?.(err);
    const version = (runVersion += 1);
    controller.abort();
    controller = new AbortController();
    const { signal } = controller;
    void (async () => {
      await backoffBeforeReconnect();
      if (closed || version !== runVersion) return;
      await startRun(version, signal, "reconnect");
    })();
  }

  const restart = (cursor: string) => {
    if (closed || !cursor) return;
    // Apply buffered events before replacing the stream.
    const beforeFlush = runVersion;
    deliveryQueue.flush();
    if (beforeFlush !== runVersion) return;
    afterCursor = cursor;
    attempt = 0;
    runVersion += 1;
    controller.abort();
    controller = new AbortController();
    streamConnected = false;
    armFallback();
    // Bootstrap can replace the stream before its first connected comment.
    void startRun(runVersion, controller.signal, hasOpened ? "resume" : "initial");
  };

  armFallback();
  void startRun(runVersion, controller.signal, "initial");

  return {
    url,
    ready,
    wakeReconnect: () => {
      attempt = 0;
      wakeDelay?.();
    },
    resumeAfter: restart,
    close: () => {
      closed = true;
      settleReady();
      stopFallback();
      wakeDelay?.();
      boardCoalescer?.cancel();
      deliveryQueue.flush();
      controller.abort();
      return Promise.allSettled([...streams]).then(() => undefined);
    },
  };
}
