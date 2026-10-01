import { beforeEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { loadWalkGitReview, resetWalkGitLoadsForTests } from "./walk-git-loading.ts";

beforeEach(resetWalkGitLoadsForTests);
describe("Git review acquisition", () => {
  it("shares preparation with visible reads and separates comparison selectors", async () => {
    const getProjectSourceGitReview = vi.fn().mockResolvedValue({ files: [] });
    const client = stubClient({ getProjectSourceGitReview });
    const first = loadWalkGitReview(client, "p", "change", { sessionId: "s" });
    expect(loadWalkGitReview(client, "p", "change", { sessionId: "s" })).toBe(first);
    const review = await first;
    expect(await loadWalkGitReview(client, "p", "change", { sessionId: "s" })).toBe(review);
    await loadWalkGitReview(client, "p", "change", { sessionId: "s", movement: true });
    await loadWalkGitReview(client, "p", "change", { sessionId: "s", parent: 2 });
    await loadWalkGitReview(client, "p", "change", { sessionId: "s", cursor: "next" });
    await loadWalkGitReview(client, "p", "change", { sessionId: "another" });
    expect(getProjectSourceGitReview).toHaveBeenCalledTimes(5);
  });
  it("retries failures and does not retain oversized pages", async () => {
    const getProjectSourceGitReview = vi.fn().mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue({ files: [], message: "x".repeat(4 * 1024 * 1024) });
    const client = stubClient({ getProjectSourceGitReview });
    await expect(loadWalkGitReview(client, "p", "c")).rejects.toThrow("offline");
    await loadWalkGitReview(client, "p", "c");
    await loadWalkGitReview(client, "p", "c");
    expect(getProjectSourceGitReview).toHaveBeenCalledTimes(3);
  });
});
