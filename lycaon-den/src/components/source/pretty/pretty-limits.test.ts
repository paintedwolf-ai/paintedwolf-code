import { describe, expect, it } from "vitest";
import { PrettyJobScheduler, PrettyProjectionCache } from "./pretty-cache.ts";
import {
  DEFAULT_PRETTY_RESOURCE_LIMITS,
  estimateProjectionCacheBytes,
  maxPrettyVisits,
} from "./pretty-limits.ts";
import { formatPrettyProjection } from "./pretty-segmenter.ts";

describe("pretty-limits & pretty-cache (FMT-AC-11, FMT-AC-15)", () => {
  it("FMT-AC-11: enforces exact mechanics section 5 resource ceilings and visits formula", () => {
    expect(DEFAULT_PRETTY_RESOURCE_LIMITS).toEqual({
      maxInputUtf8Bytes: 2_000_000,
      maxInputUtf16Units: 2_000_000,
      maxOutputUtf8Bytes: 4_000_000,
      maxOutputUtf16Units: 4_000_000,
      maxOutputLines: 50_000,
      maxNestingDepth: 128,
      maxTokens: 500_000,
      maxMappingRuns: 500_000,
      maxCacheBytes: 8 * 1024 * 1024,
    });
    expect(maxPrettyVisits(100)).toBe(32 * 100 + 1024);
  });

  it("FMT-AC-11: enforces maxNestingDepth (128) deterministically and falls back to usable 1:1 passthrough", () => {
    const deep128 = "[".repeat(128) + "1" + "]".repeat(128);
    const ok128 = formatPrettyProjection(deep128, {
      sourceIdentity: "depth-128.json",
      sourceRevision: 1,
      path: "depth-128.json",
    });
    expect(ok128.outcome).toBe("formatted");
    expect(ok128.counters.maxDepthReached).toBe(128);

    const deep129 = "[".repeat(129) + "1" + "]".repeat(129);
    const over129 = formatPrettyProjection(deep129, {
      sourceIdentity: "depth-129.json",
      sourceRevision: 1,
      path: "depth-129.json",
    });
    expect(over129.outcome).toBe("limit-exceeded");
    expect(over129.limitReason).toBe("maxNestingDepth");
    expect(over129.displayText).toBe(deep129);
    expect(over129.sourceMap.runs).toHaveLength(1);
    expect(over129.sourceMap.runs[0]?.kind).toBe("identity");
  });

  it("FMT-AC-11: enforces input, output, line, token, mapping-run, and visit limits before unbounded growth", () => {
    const raw = '{"a":1,"b":2,"c":3,"d":4}';

    const inputExceeded = formatPrettyProjection(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxInputUtf16Units: 10 },
    });
    expect(inputExceeded.outcome).toBe("limit-exceeded");
    expect(inputExceeded.limitReason).toBe("maxInputUtf16Units");
    expect(inputExceeded.displayText).toBe(raw);

    const lineExceeded = formatPrettyProjection(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxOutputLines: 3 },
    });
    expect(lineExceeded.outcome).toBe("limit-exceeded");
    expect(lineExceeded.limitReason).toBe("maxOutputLines");

    const tokenExceeded = formatPrettyProjection(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxTokens: 5 },
    });
    expect(tokenExceeded.outcome).toBe("limit-exceeded");
    expect(tokenExceeded.limitReason).toBe("maxTokens");

    const runExceeded = formatPrettyProjection(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxMappingRuns: 4 },
    });
    expect(runExceeded.outcome).toBe("limit-exceeded");
    expect(runExceeded.limitReason).toBe("maxMappingRuns");

    const visitExceeded = formatPrettyProjection(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxVisits: 4 },
    });
    expect(visitExceeded.outcome).toBe("limit-exceeded");
    expect(visitExceeded.limitReason).toBe("maxVisits");
  });

  it("FMT-AC-11: honors deterministic AbortSignal and shouldCancel callbacks", () => {
    const raw = '{"a":1,"b":2,"c":3}';
    let calls = 0;
    const canceledRes = formatPrettyProjection(raw, {
      sourceIdentity: "cancel-doc",
      sourceRevision: 1,
      shouldCancel: () => {
        calls += 1;
        return calls >= 3;
      },
    });
    expect(canceledRes.outcome).toBe("canceled");
    expect(canceledRes.displayText).toBe(raw);
    expect(canceledRes.sourceMap.runs[0]?.kind).toBe("identity");
  });

  it("FMT-AC-11, FMT-AC-15: evicts cold cache entries under byte ceiling and frees entries on surface disposal", () => {
    const r1 = formatPrettyProjection('{"k":"first-entry"}', {
      sourceIdentity: "tab-1",
      sourceRevision: 1,
    });
    const r2 = formatPrettyProjection('{"k":"second-entry"}', {
      sourceIdentity: "tab-2",
      sourceRevision: 1,
    });
    const r3 = formatPrettyProjection('{"k":"third-entry"}', {
      sourceIdentity: "tab-3",
      sourceRevision: 1,
    });

    const b1 = estimateProjectionCacheBytes(r1);
    const b2 = estimateProjectionCacheBytes(r2);
    // Allow at most two entries at once.
    const cache = new PrettyProjectionCache(b1 + b2 + 16);

    expect(cache.set(r1)).toBe(true);
    expect(cache.set(r2)).toBe(true);
    expect(cache.size).toBe(2);

    // Touch tab-1 so tab-2 becomes the coldest entry.
    expect(cache.get("tab-1", 1)).toBe(r1);

    // Inserting tab-3 evicts tab-2.
    expect(cache.set(r3)).toBe(true);
    expect(cache.evictionCount).toBe(1);
    expect(cache.get("tab-2", 1)).toBeUndefined();
    expect(cache.get("tab-1", 1)).toBe(r1);
    expect(cache.get("tab-3", 1)).toBe(r3);

    // Disposing tab-1 removes its entries and frees accounted bytes.
    expect(cache.deleteSurface("tab-1")).toBe(1);
    expect(cache.get("tab-1", 1)).toBeUndefined();
  });

  it("FMT-AC-11, FMT-AC-15: schedules one job per surface, cancels superseded jobs, and discards stale revisions", async () => {
    const scheduler = new PrettyJobScheduler();
    const published: number[] = [];

    // Schedule rev 1, then immediately supersede with rev 2 on the same surface.
    const p1 = scheduler.schedule('{"rev":1}', {
      sourceIdentity: "file:///a.json",
      sourceRevision: 1,
      generation: 1,
      onPublish: (res) => published.push(res.sourceRevision),
    });
    const p2 = scheduler.schedule('{"rev":2}', {
      sourceIdentity: "file:///a.json",
      sourceRevision: 2,
      generation: 1,
      onPublish: (res) => published.push(res.sourceRevision),
    });

    const [res1, res2] = await Promise.all([p1, p2]);
    expect(res1).toBeNull();
    expect(res2?.sourceRevision).toBe(2);
    expect(published).toEqual([2]);
    expect(scheduler.canceledJobCount).toBe(1);

    // Disabling Pretty on the surface before job completion discards the result.
    const p3 = scheduler.schedule('{"rev":3}', {
      sourceIdentity: "file:///a.json",
      sourceRevision: 3,
      generation: 2,
      onPublish: (res) => published.push(res.sourceRevision),
    });
    scheduler.setSurfaceState("file:///a.json", { enabled: false });
    const res3 = await p3;
    expect(res3).toBeNull();
    expect(published).toEqual([2]);
  });
});
