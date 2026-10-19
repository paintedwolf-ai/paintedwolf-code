import { describe, expect, it } from "vitest";
import type { SourceWatchCoverage } from "../../api/types.ts";
import { sourceWatchNotice } from "./source-watch-notice.ts";

const coverage = (state: SourceWatchCoverage["state"], extra: Partial<SourceWatchCoverage> = {}): SourceWatchCoverage => ({
  state, recursive: false, unwatched_directories: 0, ...extra,
});

describe("source observation coverage", () => {
  it("reports the kernel watch limit only from the host's explicit fact", () => {
    for (const state of ["partial", "faulted", "unwatched"] as const) {
      expect(sourceWatchNotice(coverage(state, { watch_limit_reached: true }))).toContain("fs.inotify.max_user_watches");
      expect(sourceWatchNotice(coverage(state))).not.toContain("fs.inotify.max_user_watches");
    }
  });
  it("clears exhaustion guidance after live coverage recovers", () => {
    expect(sourceWatchNotice(coverage("partial", { watch_limit_reached: true }))).not.toBeNull();
    expect(sourceWatchNotice(coverage("live"))).toBeNull();
    expect(sourceWatchNotice(coverage("live", { watch_limit_reached: true }))).toBeNull();
  });
  it("keeps intentional lazy boundaries distinct from failed registrations", () => {
    expect(sourceWatchNotice(coverage("live", { policy_unwatched: 3 }))).toContain("on demand");
    expect(sourceWatchNotice(coverage("partial", { unwatched_directories: 3 }))).toContain("not watched");
    expect(sourceWatchNotice(coverage("faulted"))).toContain("stopped");
    expect(sourceWatchNotice(coverage("unwatched"))).toContain("not being watched");
    expect(sourceWatchNotice(undefined)).toBeNull();
  });
});
