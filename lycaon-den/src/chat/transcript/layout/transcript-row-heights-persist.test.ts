import { beforeEach, describe, expect, it } from "vitest";
import {
  clearTranscriptRowHeightsForTests,
  flushTranscriptRowHeightsToDisk,
  loadTranscriptRowHeightsFromSnapshot,
  recordTranscriptRowHeight,
  seedTranscriptRowHeightsForTests,
  transcriptRowHeightForScope,
} from "./transcript-row-heights-persist.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../../store/app-state-snapshot.ts";
import { transcriptRowEstimatedHeight } from "./transcript-virtualizer.ts";

describe("transcript-row-heights-persist", () => {
  beforeEach(() => {
    clearTranscriptRowHeightsForTests();
    resetAppStateSnapshotForTests();
  });

  it("stores and restores heights for their explicit session scope", async () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    recordTranscriptRowHeight(scope, "tool-1", "collapsed", 240.6);
    await flushTranscriptRowHeightsToDisk();

    clearTranscriptRowHeightsForTests();
    resetAppStateSnapshotForTests({
      version: 1,
      recents: [],
      transcriptRowHeights: getAppStateSnapshot().transcriptRowHeights,
    });
    loadTranscriptRowHeightsFromSnapshot();

    expect(transcriptRowHeightForScope(scope, "tool-1", "collapsed")).toBe(240.59375);
    const tool = { kind: "tool" as const, key: "tool-1", part: {} as never };
    expect(
      transcriptRowHeightForScope(scope, tool.key, "collapsed") ??
        transcriptRowEstimatedHeight(tool),
    ).toBe(240.59375);
  });

  it("keeps heights isolated between sessions", () => {
    const first = { projectId: "p1", sessionId: "s1" };
    const second = { projectId: "p1", sessionId: "s2" };
    recordTranscriptRowHeight(first, "tool-1", "collapsed", 200);
    seedTranscriptRowHeightsForTests(second, {
      "tool-1": { collapsed: 180 },
    });
    expect(transcriptRowHeightForScope(first, "tool-1", "collapsed")).toBe(200);
    expect(transcriptRowHeightForScope(second, "tool-1", "collapsed")).toBe(180);
  });

  it("keeps concurrent transcript measurements with their sessions", () => {
    const leaving = { projectId: "p1", sessionId: "s1" };
    const arriving = { projectId: "p1", sessionId: "s2" };
    recordTranscriptRowHeight(leaving, "row-1", "collapsed", 200);
    recordTranscriptRowHeight(arriving, "row-2", "collapsed", 300);
    expect(transcriptRowHeightForScope(leaving, "row-1", "collapsed")).toBe(200);
    expect(transcriptRowHeightForScope(arriving, "row-2", "collapsed")).toBe(300);
  });

  it("keeps collapsed and expanded measurements separate", () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    recordTranscriptRowHeight(scope, "tool-1", "collapsed", 32);
    recordTranscriptRowHeight(scope, "tool-1", '["tool-1"]', 376);

    expect(transcriptRowHeightForScope(scope, "tool-1", "collapsed")).toBe(32);
    expect(transcriptRowHeightForScope(scope, "tool-1", '["tool-1"]')).toBe(376);
  });

  it("debounces disk writes until flush", async () => {
    recordTranscriptRowHeight(
      { projectId: "p1", sessionId: "s1" },
      "tool-1",
      "collapsed",
      120,
    );
    expect(getAppStateSnapshot().transcriptRowHeights).toBeUndefined();
    await flushTranscriptRowHeightsToDisk();
    expect(
      getAppStateSnapshot().transcriptRowHeights?.[0]?.rows["tool-1"]?.collapsed,
    ).toBe(120);
  });
});
