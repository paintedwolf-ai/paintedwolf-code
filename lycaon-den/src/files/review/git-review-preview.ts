import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import type {  SourceGitReview, SourceGitReviewFile } from "../../api/types.ts";
import { fileVersionFromComparison } from "../history/file-version.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import { type FileDiffPreview } from "../documents/files-buffer-state.ts";
import { sourceContentNotice, type ContentNotice } from "../source/source-content-availability.ts";
import type { WalkGitStep } from "../walk/walk-model.ts";

export function gitReviewPreview(step: WalkGitStep, review: SourceGitReview, file: SourceGitReviewFile, comparison: ComparisonSnapshot): FileDiffPreview {
  const version = fileVersionFromComparison({
    versionId: "", fileId: "", rootId: step.change.root_id, path: file.path, op: file.op,
    ts: review.commit.committed_at, initialComparison: "before",
  }, comparison);
  if (!version || !comparison.before || !comparison.after) throw new Error("The Git comparison has no endpoints.");
  const parts = [
    `${review.before_commit.slice(0, 12) || "Empty tree"} → ${review.after_commit.slice(0, 12)}`,
    review.commit_comparison ? "Commit compared with parent" : "Movement between tips",
  ];
  if (comparison.before.availability === "absent") parts.push("New file");
  else if (comparison.after.availability === "absent") parts.push("Deleted file");
  if (file.before_path !== file.path) parts.push(`${file.before_path} → ${file.path}`);
  if (file.before_mode !== file.after_mode) parts.push(`Mode ${file.before_mode} → ${file.after_mode}`);
  const beforeNotice = sourceContentNotice({ availability: comparison.before.availability, reason: comparison.before.reason, sizeBytes: comparison.before.size_bytes });
  if (beforeNotice) parts.push(`Before: ${beforeNotice.message}`);
  let notice: ContentNotice | undefined;
  if (file.before_mode === "160000" || file.after_mode === "160000") {
    notice = { tone: "info", message: `Submodule reference: ${file.before_oid || "Absent"} → ${file.after_oid || "Absent"}.` };
  } else if (comparison.after.reason === "content_too_large") {
    notice = { tone: "info", message: "This committed file is too large for the text preview." };
  } else if (comparison.after.availability === "binary") {
    notice = { tone: "info", message: "This committed file is binary. Its contents cannot be shown as a text diff." };
  }
  if (file.before_mode === "120000" || file.after_mode === "120000") parts.push("Symlink target; the link is not followed");
  return { version, title:`${review.commit.message.split("\n")[0] || "Commit without a message"} · ${file.path}`, detail: parts.join(" · "), notice, walkStep: step, walkSelection: { movement: !review.commit_comparison, parent: review.commit_comparison && review.commit.parents.length > 1 ? review.commit.parents.indexOf(review.before_commit) + 1 : undefined } };
}

export function openGitReviewFile(projectId: string, rootLabel: string, step: WalkGitStep, review: SourceGitReview, file: SourceGitReviewFile, comparison: ComparisonSnapshot): string {
  const diffPreview = gitReviewPreview(step, review, file, comparison);
  return openFilesBuffer(projectId, {
    rootId: step.change.root_id, rootLabel, path: file.path, kind: "diff", intent: "permanent", origin: "reader",
    jobId: `preview-diff:git:${step.change.id}:${review.before_commit}:${review.after_commit}`,
    diffPreview,
  });
}
