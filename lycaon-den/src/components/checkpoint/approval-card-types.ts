import type { LycaonClient } from "../../api/client.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";

export type ContentResolveBody = {
  decision: "approve" | "reject" | "approve_partial";
  approvedHunks?: string[];
  /** True when the No button carried the composer draft as guidance. */
  withGuidance?: boolean;
};

export type ApprovalResolveOptions = {
  optionId?: string;
  /** True when the No button carried the composer draft as guidance. */
  withGuidance?: boolean;
};

export type ApprovalCardProps = {
  client?: LycaonClient;
  checkpoint: PendingCheckpoint;
  sessionId?: string;
  projectId?: string;
  resolving?: boolean;
  /** The checkpoint has resolved; the card stays only for the dock's exit. */
  inert?: boolean;
  onToolApproval: (
    action: "approve" | "reject",
    options?: ApprovalResolveOptions,
  ) => void;
  onContentApply: (body: ContentResolveBody) => void;
  onSkipReviewPath?: (mode: "day" | "always") => void;
  onShowInChat?: (toolCallId: string) => void;
  /** Controlled minimize state; omit to keep it card-local. */
  minimized?: boolean;
  onMinimizedChange?: (next: boolean) => void;
};
