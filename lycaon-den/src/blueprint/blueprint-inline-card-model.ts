import type { BlueprintCardPhase, BlueprintTranscriptStatus } from "../api/types.ts";

/** One host-declared choice leaf on the approval phase (`run.ui.choice_transitions[]`). */
export type BlueprintChoiceTransition = {
  id: string;
  label: string;
  armed: boolean;
};

export type BlueprintInlineCardView = {
  blueprintPath: string;
  revisionKey: string;
  phaseLabel: string;
  phase: BlueprintCardPhase;
  /** The host's lifecycle word for this row — what the decision record reads. */
  status: BlueprintTranscriptStatus;
  canApprove: boolean;
  collapsed: boolean;
  showActions: boolean;
};

export const BLUEPRINT_CARD_COPY = {
  requestChanges: "Request changes",
  open: "Open",
  reject: "Reject",
  approve: "Approve",
  show: "Show blueprint",
  hide: "Hide blueprint",
  approved: "Approved",
  rejected: "Rejected",
  superseded: "Superseded",
} as const;

export type BlueprintCardDecision = {
  kind: "approved" | "rejected";
  label: string;
};

/** Card decisions follow host status; superseded counts as rejection. */
export function blueprintCardDecision(
  status: BlueprintTranscriptStatus,
): BlueprintCardDecision | null {
  switch (status) {
    case "approved":
      return { kind: "approved", label: BLUEPRINT_CARD_COPY.approved };
    case "rejected":
      return { kind: "rejected", label: BLUEPRINT_CARD_COPY.rejected };
    case "superseded":
      return { kind: "rejected", label: BLUEPRINT_CARD_COPY.superseded };
    default:
      return null;
  }
}
