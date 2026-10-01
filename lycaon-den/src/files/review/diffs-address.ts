import {
  lensPickerLabel,
  lensRangeNote,
  lensScopeHeader,
  type ReviewLensScope,
} from "./review-model.ts";

/** Comparison address for a diffs page. */
export type DiffsAddress =
  /** Follows the active review lens. */
  | { kind: "lens" }
  /** Scoped to a specific session turn. */
  | {
      kind: "turn";
      sessionId: string;
      /** Turn ordinal. */
      turn: number;
      /** Opening user message ID. */
      messageId: string;
    }
  /** Comparison between two Git commits. */
  | {
      kind: "git";
      rootId: string;
      /** Revision spec string. */
      spec: string;
      /** Base commit ID or empty for empty tree. */
      beforeCommit: string;
      afterCommit: string;
      /** Comparison label. */
      label: string;
    };

export type TurnDiffsAddress = Extract<DiffsAddress, { kind: "turn" }>;
export type GitDiffsAddress = Extract<DiffsAddress, { kind: "git" }>;

/** Cache key distinguishing lens, turn, and git comparison addresses. */
export function diffsAddressKey(address: DiffsAddress): string {
  switch (address.kind) {
    case "lens": return "lens";
    case "turn": return `turn\u0000${address.sessionId.trim()}\u0000${address.messageId.trim()}`;
    case "git": return `git\u0000${address.rootId.trim()}\u0000${address.beforeCommit.trim()}\u0000${address.afterCommit.trim()}`;
  }
}

/** Resolved review lens scope and title state. */
export type DiffsLensState = { scope: ReviewLensScope; comparisonOff: boolean; subjectTitle?: string };

export type DiffsPageCopy = {
  /** Page heading: the comparison's own name. */
  title: string;
  /** Tab name. */
  tab: string;
  /** Explanatory description of the comparison boundaries. */
  note: string;
};

function shortCommit(id: string): string {
  return id.slice(0, 7);
}

export function diffsPageCopy(address: DiffsAddress, lens: DiffsLensState): DiffsPageCopy {
  if (address.kind === "git") {
    return {
      title: address.label,
      tab: `All diffs · ${address.label}`,
      note: address.beforeCommit
        ? `From commit ${shortCommit(address.beforeCommit)} to ${shortCommit(address.afterCommit)}, read from Git. Uncommitted changes never appear here.`
        : `Every file commit ${shortCommit(address.afterCommit)} holds, since it has no parent. Uncommitted changes never appear here.`,
    };
  }
  if (address.kind === "turn") {
    return {
      title: "What this turn did",
      tab: `All diffs · turn ${address.turn}`,
      note: "From the state this turn found each file to the state it left it, across every write it made. A later turn's work never appears here.",
    };
  }
  const opts = { comparisonOff: lens.comparisonOff, subjectTitle: lens.subjectTitle };
  return {
    title: lensScopeHeader(lens.scope, opts),
    tab: lensPickerLabel(lens.scope, opts),
    note: lensRangeNote(lens.scope),
  };
}
