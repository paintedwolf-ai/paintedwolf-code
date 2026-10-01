import type { CheckpointDecisionMeta, IndexWarmingMeta, CitationGrounding, NavigationReference, BlueprintMeta, ProgressChange, ProgressStep, TurnLoad } from "../../../api/types.ts";
import type { PendingSend } from "../../send/pending-sends.ts";
import type { FileEditFold } from "../../file-edit/file-edit-fold.ts";
import type { TranscriptLayout } from "../layout/transcript-layout.ts";
import { type ToolPartView } from "../../tool/tool-part-model.ts";
import type { TurnLoadRow } from "../../turnload/turn-load-rows.ts";

export type ActivitySpanEntry =
  | { kind: "tool"; part: ToolPartView; batchOrder?: number }
  | {
      kind: "index_warming";
      key: string;
      meta: IndexWarmingMeta;
      workflowRunId?: string;
    }
  | {
      /** A decision the engine made for the span's turn, drawn like the call it replaced. */
      kind: "turn_load";
      key: string;
      row: TurnLoadRow;
    };

export type TranscriptItem =
  | {
      kind: "user";
      key: string;
      text: string;
      artifactIds?: string[];
    }
  | {
      /** Local presentation, never a host message or turn boundary. */
      kind: "pending_user";
      key: string;
      text: string;
      pending: PendingSend;
    }
  | {
      kind: "assistant";
      key: string;
      text: string;
      grounding?: CitationGrounding;
      navigationRefs?: NavigationReference[];
    }
  | {
      kind: "draft";
      key: string;
      text: string;
      /** True while the host streams into the slot before commit. */
      live?: boolean;
      /** Live completion-token estimate while streaming (0 once settled). */
      generatingTokens?: number;
      draftVersionCount?: number;
      draftStatus?: import("../../../api/types.ts").DraftStatus;
    }
  | {
      kind: "workflow_feedback";
      key: string;
      meta: import("../../../api/types.ts").WorkflowFeedbackMeta;
      runId?: string;
    }
  | {
      /** The host's note for a phase it holds; its progress is read from the record the note names. */
      kind: "workflow_explain";
      key: string;
      meta: import("../../../api/types.ts").WorkflowExplainMeta;
      runId?: string;
    }
  | {
      kind: "index_warming";
      key: string;
      meta: IndexWarmingMeta;
      workflowRunId?: string;
    }
  | { kind: "progress_complete"; key: string; steps: ProgressStep[] }
  | {
      kind: "progress_update";
      key: string;
      initial: boolean;
      steps: ProgressStep[];
      changes: ProgressChange[];
      summary?: import("../../../api/types.ts").ProgressUpdateSummary;
    }
  | { kind: "tool"; key: string; part: ToolPartView; batchOrder?: number }
  | {
      /** Renders only inside an activity span, beside the calls it decided for. */
      kind: "turn_load";
      key: string;
      row: TurnLoadRow;
    }
  | {
      kind: "activity_span";
      key: string;
      /** Weighted, catalog-authored summary of the work in this span. */
      label: string;
      entries: ActivitySpanEntry[];
    }
  | {
      kind: "worker_group";
      key: string;
      parts: ToolPartView[];
    }
  | {
      kind: "worker_file_edit";
      key: string;
      anchorMessageId: string;
      /** Project snapshots from the committed promotion. */
      folds: FileEditFold[];
      ts: string;
    }
  | {
      /** One folded diff per path, anchored at its first write. */
      kind: "file_edit";
      key: string;
      folds: FileEditFold[];
      /** Row id of the first write — the item's immutable ord anchor. */
      anchorMessageId: string;
    }
  | {
      kind: "blueprint_card";
      key: string;
      meta: BlueprintMeta;
      content: string;
      view: import("../../../blueprint/blueprint-inline-card-model.ts").BlueprintInlineCardView;
    }
  | {
      kind: "checkpoint";
      key: string;
      parentMessageId: string;
      meta: CheckpointDecisionMeta;
    }
  | {
      /** Visible row without a specialized card. */
      kind: "fallback";
      key: string;
      role: string;
      messageKind?: string;
      text: string;
    }
  | { kind: "workflow_boundary"; key: string; label: string }
  | TranscriptTimeItem;

/** Rows inserted by transcript-time-rows.ts. */

export type TranscriptTimeItem =
  | {
      /** Dates the row after it: the first row of a day, or the first after a quiet hour. */
      kind: "time_marker";
      key: string;
      variant: "day" | "gap";
      /** Epoch ms of the dated row. */
      at: number;
      anchorMessageId: string;
    }
  | {
      /** Where rows begin that arrived after the person last had this chat on screen. */
      kind: "unread_marker";
      key: string;
      /** Epoch ms of the seen stamp the marker compares against. */
      seenAt: number;
      anchorMessageId: string;
    }
  | {
      /** When one visible user turn's work finished, and how long it worked. */
      kind: "turn_tail";
      key: string;
      openingMessageId: string;
      /** Last message of the turn; the tail follows its row. */
      anchorMessageId: string;
      /** When the opening prompt was recorded; null when its timestamp is unreadable. */
      startedAt: number | null;
      /** Null reserves the timing row while the turn is still open. */
      settledAt: number | null;
      activeMs: number;
      workMs: number;
    };

export type RawTranscriptItem = Exclude<
  TranscriptItem,
  { kind: "activity_span" | "worker_group" | "file_edit" | "pending_user" } | TranscriptTimeItem
>;

export type DisplayTranscriptItem = Exclude<
  TranscriptItem,
  { kind: "index_warming" | "turn_load" }
>;

export type TranscriptBuildOptions = {
  /** Shows benign tool activity when enabled. */
  verboseMode?: boolean;
  /** Controls tool visibility by transcript surface. */
  layout?: TranscriptLayout;
  /** Decision-engine receipts keyed by the user message that opened their turn. */
  turnLoads?: Readonly<Record<string, readonly TurnLoad[]>>;
};
