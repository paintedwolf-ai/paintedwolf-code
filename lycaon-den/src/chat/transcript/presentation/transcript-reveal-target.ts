export type RevealChickletKind = "citation" | "tool" | "message";

export type TranscriptRevealAnchor = {
  chicklet: RevealChickletKind;
  anchorId: string;
  /** File path used to select the matching activity inside a multi-step command. */
  path?: string;
  /** Keep opened parent disclosures expanded after the reveal flash finishes. */
  persistExpansion?: boolean;
  /** Matched text to highlight inside a message reveal. */
  matchText?: string;
};

export type TranscriptRevealTarget = {
  sessionId: string;
  /** Open the host job in its root chat before revealing a worker tool. */
  workerId?: string;
  /** Pending approvals are reviewed in the composer dock. */
  checkpointId?: string;
  /** Omit to open the session without a row target. */
  anchor?: TranscriptRevealAnchor;
};
