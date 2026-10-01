import { filesBufferText } from "../documents/project-files-buffers.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { beforeEach, describe, expect, it } from "vitest";
import { gitReviewPreview, openGitReviewFile } from "./git-review-preview.ts";
import { gitReview, gitReviewComparison, gitReviewFile, gitReviewStep } from "./git-review-fixtures.ts";
import { applyFilesBufferLoad, applyFilesBufferDraft, openFilesBuffer, resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";

beforeEach(resetProjectFilesForTests);

describe("immutable Git file preview", () => {
  it("opens separate commit comparisons while preserving a working draft", () => {
    const live = openFilesBuffer("p1", { rootId: "r1", rootLabel: "Root", path: "src/app.ts", intent: "permanent" });
    applyFilesBufferLoad("p1", live, { file_id: "", version_id: "", workspace_id: "workspace", workspace_kind: "project", path: "src/app.ts", content: "base", binary: false, over_limit: false, writable: true, size_bytes: 4, sha256: "base" });
    applyFilesBufferDraft("p1", live, "unsaved work");
    const review = gitReview();
    const comparison = sourceReaderFixture().comparison(gitReviewComparison());
    const first = openGitReviewFile("p1", "Root", gitReviewStep(), review, gitReviewFile(), comparison);
    const second = openGitReviewFile("p1", "Root", gitReviewStep(), { ...review, before_commit: "e".repeat(40) }, gitReviewFile(), comparison);
    expect(new Set([live, first, second]).size).toBe(3);
    expect(filesBufferText(projectFilesState("p1").byKey[live]!)).toBe("unsaved work");
    expect(projectFilesState("p1").byKey[first]?.diffPreview?.version).toMatchObject({ initialComparison: "before", source: { kind: "reader", reader: comparison.reference } });
  });

  it("preserves deletion and rename endpoints and reports unavailable content", () => {
    const diff = gitReviewComparison();
    diff.after = { state: "absent", availability: "absent", size_bytes: 0, content: "" };
    const preview = gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile("gone.ts", { op: "delete" }), sourceReaderFixture().comparison(diff));
    expect(preview.version).toMatchObject({ availability: "absent", source: { kind: "reader" } });
    expect(preview.detail).toContain("Deleted file");
    const rename = gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile("new.ts", { before_path: "old.ts", op: "rename" }), sourceReaderFixture().comparison(gitReviewComparison()));
    expect(rename.detail).toContain("old.ts → new.ts");
    expect(rename.detail).not.toContain("New file");
    const added = gitReviewComparison();
    added.before = { state: "absent", availability: "absent", size_bytes: 0, content: "" };
    expect(gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile("added.ts", { op: "create" }), sourceReaderFixture().comparison(added)).detail).toContain("New file");
    diff.after = { state: "content", availability: "binary", size_bytes: 10, content: "" };
    expect(gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile(), sourceReaderFixture().comparison(diff)).notice).toMatchObject({ tone: "info", message: expect.stringContaining("binary") });
    const large = gitReviewComparison();
    large.after = { state: "content", availability: "not_captured", reason: "content_too_large", size_bytes: 5 * 1024 * 1024, content: "" };
    large.before = { state: "content", availability: "not_captured", reason: "content_too_large", size_bytes: 6 * 1024 * 1024, content: "" };
    const oversized = gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile(), sourceReaderFixture().comparison(large));
    expect(oversized.notice?.tone).toBe("info");
    expect(oversized.detail).toContain("6.0 MB");
    expect(() => gitReviewPreview(gitReviewStep(), gitReview(), gitReviewFile(), { in_range: false, location_changed: false })).toThrow("no endpoints");
  });
});
