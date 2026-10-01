import type {
  Message,
  BlueprintSummary,
  Session,
  TurnClock,
  TurnLoad,
  WorkerTask,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import type { PendingCheckpoint } from "../checkpoint/checkpoint-model.ts";
import type { TranscriptWindow } from "../transcript/layout/transcript-window.ts";
import type { WorkerTranscript } from "../worker/worker-transcript.ts";

export type SessionChatScope = {
  projectId: string;
  sessionId: string;
};

export type SessionChatSnapshot = {
  scope: SessionChatScope;
  touchedAt: number;
  session: Session;
  /** Sparse transcript window used to restore resident rows. */
  transcript: TranscriptWindow;
  /** Resident rows materialized from transcript. */
  messages: Message[];
  transcriptWatermark: number;
  /** Visible user turn clocks for the resident rows. */
  turnClocks: TurnClock[];
  /** Decision engine receipts for the resident rows. */
  turnLoads: TurnLoad[];
  workers: WorkerTask[];
  workerTranscripts: Record<string, WorkerTranscript>;
  activeWorkflowRun?: WorkflowRun;
  workflowRuns: WorkflowRun[];
  workflowCatalog: WorkflowSummary[];
  blueprints: BlueprintSummary[];
  pendingCheckpoints: PendingCheckpoint[];
};
