import { describe, expect, it } from "vitest";
import { PrettyJobScheduler, PrettyProjectionCache } from "./pretty-cache.ts";
import {
  createRuneOffsetTable,
  DEFAULT_PRETTY_RESOURCE_LIMITS,
  estimateProjectionCacheBytes,
  maxPrettyVisits,
} from "./pretty-limits.ts";
import { computePrettyNativeLayout } from "./pretty-native-layout.ts";

describe("pretty-limits & pretty-cache (FMT-AC-05, FMT-AC-11, FMT-AC-15)", () => {
  it("FMT-AC-11: enforces resource ceilings and visits formula", () => {
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

  it("FMT-AC-05: createRuneOffsetTable preserves surrogate-pair Unicode scalar identity", () => {
    const raw = '{"emoji":"🌲","ok":1}';
    const table = createRuneOffsetTable(raw);
    expect(table.utf16Length).toBe(raw.length);
    // "🌲" is 1 rune, 2 UTF-16 code units: runeCount === raw.length - 1
    expect(table.runeCount).toBe(raw.length - 1);
    const emojiRuneIdx = Array.from(raw).indexOf("🌲");
    const emojiUtf16Range = table.runeRangeToUtf16(emojiRuneIdx, emojiRuneIdx + 1);
    expect(raw.slice(emojiUtf16Range.from, emojiUtf16Range.to)).toBe("🌲");
    expect(emojiUtf16Range.to - emojiUtf16Range.from).toBe(2);
  });

  it("FMT-AC-11: enforces maxNestingDepth (128) deterministically and falls back to usable passthrough", () => {
    const deep128 = "[".repeat(128) + "1" + "]".repeat(128);
    const ok128 = computePrettyNativeLayout(deep128, {
      sourceIdentity: "depth-128.json",
      sourceRevision: 1,
      path: "depth-128.json",
    });
    expect(ok128.outcome).toBe("formatted");
    expect(ok128.counters.maxDepthReached).toBe(128);

    const deep129 = "[".repeat(129) + "1" + "]".repeat(129);
    const over129 = computePrettyNativeLayout(deep129, {
      sourceIdentity: "depth-129.json",
      sourceRevision: 1,
      path: "depth-129.json",
    });
    expect(over129.outcome).toBe("limit-exceeded");
    expect(over129.limitReason).toBe("maxNestingDepth");
    expect(over129.boundaries).toEqual([]);
  });

  it("FMT-AC-11: enforces input, line, token, decoration-run, and visit limits before unbounded growth", () => {
    const raw = '{"a":1,"b":2,"c":3,"d":4}';

    const inputExceeded = computePrettyNativeLayout(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxInputUtf16Units: 10 },
    });
    expect(inputExceeded.outcome).toBe("limit-exceeded");
    expect(inputExceeded.limitReason).toBe("maxInputUtf16Units");
    expect(inputExceeded.boundaries).toEqual([]);

    const lineExceeded = computePrettyNativeLayout(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxOutputLines: 3 },
    });
    expect(lineExceeded.outcome).toBe("limit-exceeded");
    expect(lineExceeded.limitReason).toBe("maxOutputLines");

    const tokenExceeded = computePrettyNativeLayout(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxTokens: 5 },
    });
    expect(tokenExceeded.outcome).toBe("limit-exceeded");
    expect(tokenExceeded.limitReason).toBe("maxTokens");

    const runExceeded = computePrettyNativeLayout(raw, {
      sourceIdentity: "lim",
      sourceRevision: 1,
      limits: { maxMappingRuns: 2 },
    });
    expect(runExceeded.outcome).toBe("limit-exceeded");
    expect(runExceeded.limitReason).toBe("maxMappingRuns");

    const visitExceeded = computePrettyNativeLayout(raw, {
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
    const canceledRes = computePrettyNativeLayout(raw, {
      sourceIdentity: "cancel-doc",
      sourceRevision: 1,
      shouldCancel: () => {
        calls += 1;
        return calls >= 2;
      },
    });
    expect(canceledRes.outcome).toBe("canceled");
    expect(canceledRes.boundaries).toEqual([]);
  });

  it("FMT-AC-11, FMT-AC-15: evicts cold cache entries under byte ceiling and frees entries on surface disposal", () => {
    const r1 = computePrettyNativeLayout('{"k":"first-entry"}', {
      sourceIdentity: "tab-1",
      sourceRevision: 1,
    });
    const r2 = computePrettyNativeLayout('{"k":"second-entry"}', {
      sourceIdentity: "tab-2",
      sourceRevision: 1,
    });
    const r3 = computePrettyNativeLayout('{"k":"third-entry"}', {
      sourceIdentity: "tab-3",
      sourceRevision: 1,
    });

    const b1 = estimateProjectionCacheBytes(r1);
    const b2 = estimateProjectionCacheBytes(r2);
    const cache = new PrettyProjectionCache(b1 + b2 + 8);

    expect(cache.set(r1)).toBe(true);
    expect(cache.set(r2)).toBe(true);
    expect(cache.size).toBe(2);

    expect(cache.get("tab-1", 1)).toBe(r1);

    expect(cache.set(r3)).toBe(true);
    expect(cache.evictionCount).toBe(1);
    expect(cache.get("tab-2", 1)).toBeUndefined();
    expect(cache.get("tab-1", 1)).toBe(r1);
    expect(cache.get("tab-3", 1)).toBe(r3);

    expect(cache.deleteSurface("tab-1")).toBe(1);
    expect(cache.get("tab-1", 1)).toBeUndefined();
  });

  it("FMT-AC-11, FMT-AC-15: schedules one job per surface, cancels superseded jobs, and discards stale revisions", async () => {
    const scheduler = new PrettyJobScheduler();
    const published: number[] = [];

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
