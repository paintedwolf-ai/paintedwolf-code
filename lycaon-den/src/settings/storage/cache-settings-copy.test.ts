import { describe, expect, it } from "vitest";
import {
  CACHE_SETTINGS_COPY,
  cacheBucketCopy,
  cacheClearAllConfirm,
  cacheClearBucketConfirm,
  cachePresenceLabel,
} from "./cache-settings-copy.ts";

describe("cache-settings-copy", () => {
  it("uses generic label for unknown host ids", () => {
    const unk = cacheBucketCopy("future_bucket");
    expect(unk.label).toBe(CACHE_SETTINGS_COPY.unknownBucketLabel);
    expect(unk.hint).toBe("future_bucket");
  });

  it("builds confirms that keep durable messaging", () => {
    expect(cacheClearBucketConfirm("fetch_cache")).toContain(
      CACHE_SETTINGS_COPY.durableKeepNote,
    );
    const all = cacheClearAllConfirm(["A", "B"]);
    expect(all).toContain("• A");
    expect(all).toContain("• B");
    expect(all).toContain(CACHE_SETTINGS_COPY.durableKeepNote);
  });

  it("presence label prefers size when present", () => {
    expect(cachePresenceLabel(false, 2048)).toBe(CACHE_SETTINGS_COPY.empty);
    expect(cachePresenceLabel(true, 2048)).toBe("2.0 KB");
    expect(cachePresenceLabel(true, undefined)).toBe(CACHE_SETTINGS_COPY.present);
  });
});
