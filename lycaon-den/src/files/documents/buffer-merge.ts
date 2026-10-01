/** Merge state for a dirty buffer and its disk version. */

export type BufferMergeModel = {
  /** Buffer that originated the merge. */
  bufferKey: string;
  documentRevision: number;
  comparison?: "disk" | "retained";
  mine: string;
  theirs: string;
  theirsSha256: string;
};

type BufferMergeApplyPayload = {
  bufferKey: string;
  documentRevision: number;
  content: string;
  baseSha256: string;
};

/** Build the accepted merge payload. */
export function mergeApplyPayload(
  model: BufferMergeModel,
  mergedMine: string,
): BufferMergeApplyPayload {
  return {
    bufferKey: model.bufferKey,
    documentRevision: model.documentRevision,
    content: mergedMine,
    baseSha256: model.theirsSha256,
  };
}
