export type RevealChickletKind = "citation" | "tool" | "message";

export type TranscriptRevealAnchor = {
  chicklet: RevealChickletKind;
  anchorId: string;
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
