import { describe, expect, it } from "vitest";
import {
  CACHE_SETTINGS_COPY,
  formatCacheBytes,
  cacheBucketCopy,
  cacheClearAllConfirm,
  cacheClearBucketConfirm,
  cachePresenceLabel,
} from "./cache-settings-copy.ts";

describe("cache-settings-copy", () => {
  it("maps known bucket ids to labels without engine name", () => {
    const web = cacheBucketCopy("web_index");
    expect(web.label).toBe("Web research index");
    expect(web.clearLabel.toLowerCase()).not.toContain("lycaon");
    expect(CACHE_SETTINGS_COPY.intro.toLowerCase()).not.toContain("lycaon");
    expect(CACHE_SETTINGS_COPY.durableKeepNote).toMatch(/chats/i);
  });

  it("uses generic label for unknown host ids", () => {
    const unk = cacheBucketCopy("future_bucket");
    expect(unk.label).toBe(CACHE_SETTINGS_COPY.unknownBucketLabel);
    expect(unk.hint).toBe("future_bucket");
  });

  it("builds confirms that keep durable messaging", () => {
    expect(cacheClearBucketConfirm("fetch_cache")).toContain("Fetched pages");
    expect(cacheClearBucketConfirm("fetch_cache")).toContain(
      CACHE_SETTINGS_COPY.durableKeepNote,
    );
    const all = cacheClearAllConfirm(["A", "B"]);
    expect(all).toContain("• A");
    expect(all).toContain("• B");
    expect(all).toContain(CACHE_SETTINGS_COPY.durableKeepNote);
  });

  it("formats bytes", () => {
    expect(formatCacheBytes(0)).toBe("");
    expect(formatCacheBytes(512)).toBe("512 B");
    expect(formatCacheBytes(2048)).toMatch(/KB/);
  });

  it("presence label prefers size when present", () => {
    expect(cachePresenceLabel(false, 2048)).toBe(CACHE_SETTINGS_COPY.empty);
    expect(cachePresenceLabel(true, 2048)).toBe("2.0 KB");
    expect(cachePresenceLabel(true, undefined)).toBe(CACHE_SETTINGS_COPY.present);
  });
});
