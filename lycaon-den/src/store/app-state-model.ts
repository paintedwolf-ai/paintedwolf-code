import type { ActivityEvent, BoardView, CodeScanEvent, CoordinatorLoopProgress, CoordinatorRunContext, LLMCallEvent, LLMRetryReason, Message, MessageEvent, CheckpointEvent, BlueprintSummary, WorkflowSummary, FindingsDigest, GitRepoEntry, ProgressDigest, TurnClock, TurnLoad, QueueDraft, Session, SessionBootstrap, SessionEvent, SessionTranscriptPage, WorkerEvent, WorkerTask, WorkflowEvent, WorkflowRun } from "../api/types.ts";
import type { GitWorkspaceStatus } from "../chat/actions/git-workspace-status.ts";
import type { PendingCheckpoint } from "../chat/checkpoint/checkpoint-model.ts";
import type { LoadState } from "./load-state.ts";
import { type PendingSend } from "../chat/send/pending-sends.ts";
import type { GitScope } from "../components/git-repo-scope.ts";
import { type TranscriptPageRequest, type TranscriptWindow } from "../chat/transcript/layout/transcript-window.ts";
import type { WorkerTranscript } from "../chat/worker/worker-transcript.ts";
import type { SessionChatSnapshot } from "../chat/session/session-chat-snapshot.ts";

// Reactive transport cache for HTTP and SSE state.

export type SessionStatePatch = Pick<SessionEvent, "id" | "status"> &
  Partial<
    Pick<
      SessionEvent,
      | "title"
      | "last_message"
      | "host_error"
      | "untrusted_content"
      | "current_turn"
      | "ui"
      | "prompt_pending"
    >
  >;

export type SidecarStatus =
  | "connecting"
  | "connected"
  | "disconnected"
  | "reconnecting";

/** Coordinator LLM call chrome — stop/thinking activity, not transcript content. */
export interface LlmTurnActivity {
  callId?: string;
  status: "active" | "done" | "error";
  /** Provider instance id used by the waiting label. */
  provider?: string;
  /** Present only while the call is being reissued; see LLMCallEvent.attempt. */
  retry?: {
    attempt: number;
    maxAttempts: number;
    reason?: LLMRetryReason;
    /** Host-measured dead air on the previous attempt; only with reason silent. */
    silenceMs?: number;
  };
  /** Active turn uses commit-after-guard on coordinator closeout surfaces. */
  guarded?: boolean;
  /** Host-declared: this turn's provisional prose stays hidden until commit. */
  provisionalHidden?: boolean;
}

/** A prompt this client sent; `revision` arrives with admission. */

export interface PromptSubmissionHold {
  id: string;
  revision?: number;
}

/** Live work keyed by session. An absent entry means idle. */

export interface SessionActivity {
  llmTurn?: LlmTurnActivity;
  /** Prompts this client sent that applied session state does not yet reflect. */
  promptSubmissions?: readonly PromptSubmissionHold[];
  /** Session stop is converging. */
  stopping?: boolean;
  /** Host-observed work leases, keyed by activity id until their terminal edge. */
  activities?: Record<string, ActivityEvent>;
}

/** Latest coordinator context occupancy, from the most recent `ok` llm call. */

export interface ContextUsage {
  /** Prompt tokens sent on the last call — current window occupancy. */
  prompt: number;
  /** Model's full context window in tokens; 0 when the model is unknown. */
  window: number;
  /** Prompt-token level at which compaction fires; 0 when unknown. */
  compactionThreshold: number;
}

/** Session id, or "*" while create-session hydrate has no id yet. */

export type ChatHydrationLock = string;

export interface AppState {
  currentSession?: Session;
  /** Generation for async session-view commits. */
  sessionViewEpoch: number;
  /** Materialized projection of the sparse transcript window. */
  messages: Message[];
  /** Sparse live tail plus loaded older pages. Authority for resident rows. */
  transcript: TranscriptWindow;
  /** Session represented by the materialized transcript. */
  transcriptSessionId?: string;
  /** Highest transcript mutation seq seen for transcriptSessionId. */
  transcriptWatermark: number;
  /** Visible user turn clocks for transcriptSessionId, keyed by opening message id. */
  turnClocks: Record<string, TurnClock>;
  /** Decision engine receipts for transcriptSessionId, keyed by opening message id. */
  turnLoads: Record<string, TurnLoad[]>;
  /** Parent events buffered during hydration. */
  chatHydrationBuffer: MessageEvent[];
  blueprints: BlueprintSummary[];
  workers: WorkerTask[];
  /** Authoritative worker mutations observed per coordinator session. */
  workerEventEpochs: Record<string, number>;
  /** Cached child-session transcripts keyed by worker job id. */
  workerTranscripts: Record<string, WorkerTranscript>;
  pendingCheckpoints: PendingCheckpoint[];
  checkpointEventEpoch: number;
  board?: BoardView;
  boardLoad: LoadState<true>;
  /** Authoritative board mutations observed for the foreground view. */
  boardEventEpoch: number;
  workflowEventEpoch: number;
  /** Foreground findings; loaded null means the host has no digest. */
  findings: LoadState<FindingsDigest | null>;
  progress?: ProgressDigest;
  /** Foreground session-tree clock, ticked locally between host edges. */
  turnClock?: TurnClock;
  /** Foreground repository status; only a loaded result can offer initialization. */
  gitStatus: LoadState<GitWorkspaceStatus>;
  /** Project repository set for the Git tab (session state; not persisted). */
  gitRepos?: GitRepoEntry[];
  /** Active repository id from the most recent repos fetch. */
  gitActiveRepoId?: string;
  /** Session-only Git scope pin. */
  gitScopePin?: GitScope | null;
  queueDraft?: QueueDraft;
  latestCodeScan?: CodeScanEvent;
  activeWorkflowRun?: WorkflowRun;
  workflowRuns: WorkflowRun[];
  workflowCatalog: WorkflowSummary[];
  /** Per-session activity, keyed by session id. Absent entry = idle. */
  sessionActivity: Record<string, SessionActivity>;
  /** Ephemeral prompt sends awaiting a host echo. */
  pendingSends: Record<string, PendingSend[]>;
  contextUsage?: ContextUsage;
  coordinatorLoopProgress?: CoordinatorLoopProgress;
  coordinatorRunContext?: CoordinatorRunContext;
  sidecarStatus: SidecarStatus;
  isLoading: boolean;
  /** Blocks parent-session message SSE until session-switch hydrate completes. */
  chatHydrationLock?: ChatHydrationLock;
  /** Bumped on settings SSE so the verify editor re-fetches its async detection proposal. */
  verifyDetectRevision: number;
  /** Bumped on extensions-facet settings SSE so extension surfaces refetch. */
  extensionsRevision: number;
  /** Bumped on approvals settings SSE so project approval readers refetch. */
  approvalsRevision: number;
  /** Bumped on provider and model-policy invalidations so effective policy readers refetch. */
  modelPolicyRevision: number;
  /** Bumped on project-trust settings SSE so trust readers refetch. */
  projectTrustRevision: number;
}

export type AppStoreActions = {
  setSidecarStatus: (status: SidecarStatus) => void;
  setLoading: (loading: boolean) => void;
  setWorkers: (workers: WorkerTask[]) => void;
  /** Replace workers for one coordinator session; other sessions' rows are kept. */
  mergeSessionWorkers: (
    sessionId: string,
    workers: WorkerTask[],
    expectedEventEpoch: number,
  ) => boolean;
  /** Merge SSE rows into one worker's transcript in place, row by row. */
  applyWorkerTranscriptRows: (workerId: string, rows: Message[]) => void;
  /** Refresh a worker's live tail from its newest durable page. */
  installWorkerTranscriptTail: (workerId: string, page: SessionTranscriptPage) => void;
  /** Extend a worker's resident range; false when the request is stale. */
  loadWorkerTranscriptPage: (
    workerId: string,
    request: TranscriptPageRequest,
    page: SessionTranscriptPage,
  ) => boolean;
  /** Keep a worker transcript resident while a reader shows it. */
  retainWorkerTranscript: (workerId: string) => () => void;
  setCurrentSession: (session?: Session) => void;
  invalidateSessionViewRequests: () => void;
  /** Install one authoritative session and all bootstrap-supplied chrome in one commit. */
  installSessionBootstrap: (bootstrap: SessionBootstrap, session?: Session) => void;
  /** Install a live-tail baseline while retaining newer local rows. */
  installTranscriptBaseline: (
    sessionId: string,
    messages: Message[],
    watermark: number,
    page?: {
      hasMoreBefore?: boolean;
      hasMoreAfter?: boolean;
      turnClocks?: readonly TurnClock[];
      turnLoads?: readonly TurnLoad[];
    },
  ) => void;
  /** Extend the resident range with a fetched page; false when the request is stale. */
  loadTranscriptPage: (request: TranscriptPageRequest, page: SessionTranscriptPage) => boolean;
  /** Records the seen stamp the host returned for the current session. */
  noteSessionSeen: (session: Session) => void;
  /** Drops older pages so the resident range reads contiguously from the live tail. */
  resetOlderTranscriptPages: () => void;
  /** Seq-guarded patch/append of one row; returns false when stale or unchanged. */
  upsertMessage: (message: Message) => boolean;
  /** Queue a parent message event while hydrate is in flight. */
  bufferHydrationMessageEvent: (event: MessageEvent) => void;
  /** Drain the hydrate buffer for replay after baseline install. */
  takeHydrationBuffer: () => MessageEvent[];
  /** Drop one session's coordinator turn chrome, leaving any in-flight prompt intact. */
  clearLlmTurn: (sessionId: string) => void;
  /** Holds a prompt as in flight from the moment this client submits it. */
  holdPromptSubmission: (sessionId: string, submissionId: string) => void;
  /** Newer events and revision 0 release the admission hold. */
  admitPromptSubmission: (
    sessionId: string,
    submissionId: string,
    revision: number,
    appliedRevision: number | undefined,
  ) => void;
  /** Ends holds admitted below an applied session event's `revision`. */
  releasePromptSubmissionsThrough: (sessionId: string, revision: number) => void;
  /** Releases a prompt whose outcome this client learned directly. */
  releasePromptSubmission: (sessionId: string, submissionId: string) => void;
  addPendingSend: (sessionId: string, entry: Omit<PendingSend, "createdAt">) => void;
  patchPendingSend: (
    sessionId: string,
    operationId: string,
    patch: Pick<PendingSend, "state">,
  ) => void;
  removePendingSends: (
    sessionId: string,
    operationIds: readonly string[],
  ) => void;
  setSessionStopping: (sessionId: string, stopping: boolean) => void;
  /** Drop one session's activity — the local stop path; other sessions keep running. */
  clearSessionActivity: (sessionId: string) => void;
  /** Clear chat state and optionally lock hydration events. */
  clearChatForSessionSwitch: (hydrationLock?: ChatHydrationLock) => void;
  /** Lock hydration for an in-place resume. Companion slices stay. */
  beginSessionResumeSwitch: (hydrationLock?: ChatHydrationLock) => void;
  completeChatSessionHydration: () => void;
  resetChatForSessionSwitch: () => void;
  setPendingCheckpoints: (
    sessionId: string,
    sessionViewEpoch: number,
    expectedEventEpoch: number,
    checkpoints: PendingCheckpoint[],
  ) => void;
  mergeCheckpoint: (event: CheckpointEvent) => void;
  setBoard: (board?: BoardView) => void;
  setBoardLoad: (sessionId: string, epoch: number, eventEpoch: number, load: LoadState<true>) => void;
  setBoardSnapshot: (
    sessionId: string,
    sessionViewEpoch: number,
    expectedBoardEventEpoch: number,
    expectedWorkerEventEpoch: number,
    board?: BoardView,
  ) => void;
  setFindings: (sessionId: string, epoch: number, digest?: FindingsDigest) => void;
  /** Records a failed findings read without discarding the last shown digest. */
  setFindingsLoadFailed: (sessionId: string, epoch: number, err: unknown) => void;
  setProgress: (sessionId: string, epoch: number, digest?: ProgressDigest) => void;
  setTurnClock: (event: TurnClock) => void;
  setTurnLoad: (event: TurnLoad) => void;
  setGitStatus: (status: GitWorkspaceStatus) => void;
  /** Records a failed status read without discarding the last shown status. */
  setGitStatusLoadFailed: (err: unknown) => void;
  setGitRepos: (repos: GitRepoEntry[], activeRepoId: string) => void;
  setGitScopePin: (pin: GitScope | null) => void;
  clearGitScopePin: () => void;
  setQueueDraft: (sessionId: string, epoch: number, draft: QueueDraft) => boolean;
  bumpVerifyDetectRevision: () => void;
  bumpExtensionsRevision: () => void;
  bumpApprovalsRevision: () => void;
  bumpModelPolicyRevision: () => void;
  bumpProjectTrustRevision: () => void;
  setCoordinatorRunContext: (sessionId: string, epoch: number, ctx?: CoordinatorRunContext) => void;
  setSessionTitle: (sessionId: string, title: string) => void;
  mergeSession: (event: SessionStatePatch) => void;
  updateWorker: (event: WorkerEvent) => void;
  addCodeScan: (event: CodeScanEvent) => void;
  clearLatestCodeScan: () => void;
  setLLMCallStatus: (event: LLMCallEvent) => void;
  setActivity: (event: ActivityEvent) => void;
  setWorkflowState: (sessionId: string, epoch: number, input: {
    activeWorkflowRun?: WorkflowRun;
    workflowRuns: WorkflowRun[];
    workflowCatalog: WorkflowSummary[];
    blueprints: BlueprintSummary[];
  }) => void;
  /** Apply the authoritative run a workflow lifecycle event carries. */
  applyWorkflowRunEvent: (event: WorkflowEvent) => void;
  /** Exact restore from a session-chat LRU snapshot — no transcript merge. */
  restoreSessionChatSnapshot: (snapshot: SessionChatSnapshot) => void;
};

export type AppStore = {
  state: AppState;
  actions: AppStoreActions;
};
