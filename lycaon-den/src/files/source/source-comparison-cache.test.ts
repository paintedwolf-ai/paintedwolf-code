import { stubFilesClient as stubClient } from "../../test/source-client-fixture.ts";
import type { SourceComparisonTarget } from "../../api/http-capabilities/source-history.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  loadSourceComparison,
  resetSourceComparisonCacheForTests,
} from "./source-comparison-cache.ts";

const comparison = {
  in_range: true,
  before: {
    state: "absent" as const,
    size_bytes: 0,
    availability: "absent" as const,
    content: "",
  },
  after: {
    state: "content" as const,
    size_bytes: 3,
    availability: "available" as const,
    content: "new",
  },
  location_changed: false,
};

beforeEach(resetSourceComparisonCacheForTests);

describe("source comparison cache", () => {
  it("does not retain a comparison beyond the byte budget", async () => {
    const large = { ...comparison, after: { ...comparison.after, content: "new", reason: "x".repeat(17 * 1024 * 1024) } };
    const readComparison = vi.fn().mockResolvedValueOnce(comparison).mockResolvedValue(large);
    const client = stubClient({ readComparison });
    await loadSourceComparison(client, "project", { effectId: "small" });
    await loadSourceComparison(client, "project", { effectId: "large" });
    await loadSourceComparison(client, "project", { effectId: "large" });
    expect(await loadSourceComparison(client, "project", { effectId: "small" })).toMatchObject({ before: expect.objectContaining({ availability: comparison.before.availability }), after: expect.objectContaining({ availability: comparison.after.availability }) });
    expect(readComparison).toHaveBeenCalledTimes(3);
  });

  it("reads working bytes on every request even when HEAD is unchanged", async () => {
    const updated = { ...comparison, after: { ...comparison.after, content: "updated" } };
    const readComparison = vi.fn()
      .mockResolvedValueOnce(comparison)
      .mockResolvedValue(updated);
    const client = stubClient({ readComparison });
    const target: SourceComparisonTarget = {
      rootId: "root", path: "file.txt", baseline: "commit", expectedHead: "head",
    };

    const first = loadSourceComparison(client, "project", target, "session");
    const second = loadSourceComparison(client, "project", target, "session");

    expect(second).not.toBe(first);
    expect(await first).toMatchObject({ before: expect.objectContaining({ availability: comparison.before.availability }), after: expect.objectContaining({ availability: comparison.after.availability }) });
    expect(await second).toMatchObject({ after: expect.objectContaining({ availability: updated.after.availability }) });
    expect(await loadSourceComparison(client, "project", target, "session")).toMatchObject({ after: expect.objectContaining({ availability: updated.after.availability }) });
    expect(readComparison).toHaveBeenCalledTimes(3);
    expect(readComparison).toHaveBeenLastCalledWith("project", target, { sessionId: "session" });
  });

  it("refreshes presentation scope after a file is acknowledged", async () => {
    const readComparison = vi.fn().mockResolvedValueOnce(comparison)
      .mockResolvedValue({ in_range: false, location_changed: false });
    const client = stubClient({ readComparison });
    const target = { fileId: "file", baseline: "presentation" };
    expect((await loadSourceComparison(client, "project", target)).in_range).toBe(true);
    expect((await loadSourceComparison(client, "project", target)).in_range).toBe(false);
    expect(readComparison).toHaveBeenCalledTimes(2);
  });

  it("shares an in-flight effect comparison and reuses its settled snapshot", async () => {
    const readComparison = vi.fn(async () => comparison);
    const client = stubClient({ readComparison });

    const first = loadSourceComparison(client, "project", { effectId: "effect" });
    const second = loadSourceComparison(client, "project", { effectId: "effect" });

    expect(second).toBe(first);
    expect(await first).toMatchObject({ before: expect.objectContaining({ availability: comparison.before.availability }), after: expect.objectContaining({ availability: comparison.after.availability }) });
    expect(await loadSourceComparison(client, "project", { effectId: "effect" }))
      .toMatchObject({ before: expect.objectContaining({ availability: comparison.before.availability }), after: expect.objectContaining({ availability: comparison.after.availability }) });
    expect(readComparison).toHaveBeenCalledOnce();
  });

  it("does not mix session-scoped comparisons", async () => {
    const readComparison = vi.fn(async () => comparison);
    const client = stubClient({ readComparison });

    await loadSourceComparison(client, "project", { versionId: "version" }, "one");
    await loadSourceComparison(client, "project", { versionId: "version" }, "two");

    expect(readComparison).toHaveBeenCalledTimes(2);
  });

  it("separates reviewed snapshots by file and reviewed-through ordinal", async () => {
    const readComparison = vi.fn(async () => comparison);
    const client = stubClient({ readComparison });
    const first = { fileId: "file", reviewedThroughOrdinal: 3 };

    await loadSourceComparison(client, "project", first);
    await loadSourceComparison(client, "project", first);
    await loadSourceComparison(client, "project", { fileId: "file", reviewedThroughOrdinal: 4 });
    await loadSourceComparison(client, "project", { fileId: "other", reviewedThroughOrdinal: 3 });
    await loadSourceComparison(client, "project", { fileId: "file", baseline: "presentation" });

    expect(readComparison).toHaveBeenCalledTimes(4);
  });

  it("does not reuse a comparison from another client", async () => {
    const first = stubClient({
      readComparison: vi.fn(async () => comparison),
    });
    const secondComparison = {
      ...comparison,
      after: { ...comparison.after, content: "other" },
    };
    const second = stubClient({
      readComparison: vi.fn(async () => secondComparison),
    });

    await loadSourceComparison(first, "project", { effectId: "effect" });
    expect(await loadSourceComparison(second, "project", { effectId: "effect" }))
      .toMatchObject({ after: expect.objectContaining({ availability: secondComparison.after.availability }) });
    expect(second.readComparison).toHaveBeenCalledOnce();
  });

  it("bounds pending entries without letting an evicted request delete its replacement", async () => {
    const pending: { reject?: (reason: Error) => void } = {};
    const pendingPromise = new Promise<typeof comparison>((_resolve, reject) => {
      pending.reject = reject;
    });
    let firstRequested = false;
    const readComparison = vi.fn(
      (_projectId: string, target: SourceComparisonTarget) => {
        if ("effectId" in target && target.effectId === "effect-0" && !firstRequested) {
          firstRequested = true;
          return pendingPromise;
        }
        return new Promise<typeof comparison>(() => {});
      },
    );
    const client = stubClient({ readComparison });
    const first = loadSourceComparison(client, "project", { effectId: "effect-0" });
    for (let index = 1; index <= 160; index += 1) {
      void loadSourceComparison(client, "project", { effectId: `effect-${index}` });
    }

    const replacement = loadSourceComparison(client, "project", { effectId: "effect-0" });
    expect(pending.reject, "request rejection callback").toBeTypeOf("function");
    pending.reject!(new Error("request failed"));

    await expect(first).rejects.toThrow("request failed");
    expect(loadSourceComparison(client, "project", { effectId: "effect-0" }))
      .toBe(replacement);
    expect(readComparison).toHaveBeenCalledTimes(162);
  });
});
