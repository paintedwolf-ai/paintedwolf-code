import type { SourceComparison, SourceGitReview, SourceGitReviewFile } from "../../api/types.ts";
import type { WalkGitStep } from "../walk/walk-model.ts";

export function gitReviewStep(): WalkGitStep {
  return { kind: "git", key: "git:commit-change", label: "git commit", ordinal: 7, toolCallId: null, effects: [],
    change: { session_id: "", turn: 0, tool_call_id: "", tool_name: "", id: "commit-change", root_id: "r1", kind: "commit", from_commit: "a".repeat(40), to_commit: "b".repeat(40),
      from_ref: "main", to_ref: "main", detail: "Record the changes", ordinal: 7, observed_at: "2026-09-10T10:00:00Z" } };
}

export function gitReviewFile(path = "src/app.ts", overrides: Partial<SourceGitReviewFile> = {}): SourceGitReviewFile {
  return { path, before_path: path, op: "write", before_mode: "100644", after_mode: "100644", before_oid: "c".repeat(40), after_oid: "d".repeat(40),
    insertions: 2, deletions: 1, binary: false, ...overrides };
}

export function gitReview(files = [gitReviewFile()], overrides: Partial<SourceGitReview> = {}): SourceGitReview {
  return { change: gitReviewStep().change, commit: { hash: "b".repeat(40), parents: ["a".repeat(40)], message: "Record the changes\n\nExplain the details.",
    author_name: "Test author", authored_at: "2026-09-10T10:00:00Z", committed_at: "2026-09-10T10:00:00Z" },
    commit_comparison: true, before_commit: "a".repeat(40), after_commit: "b".repeat(40), files,
    files_total: files.length, insertions: 2, deletions: 1, ...overrides };
}

export function gitReviewComparison(): SourceComparison {
  return { truncated: false, in_range: true, location_changed: false, op: "write",
    before: { state: "content", availability: "available", size_bytes: 7, content: "before\n" },
    after: { state: "content", availability: "available", size_bytes: 6, content: "after\n" } };
}
