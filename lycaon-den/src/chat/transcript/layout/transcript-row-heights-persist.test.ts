import { beforeEach, describe, expect, it } from "vitest";
import {
  clearTranscriptRowHeightsForTests,
  flushTranscriptRowHeightsToDisk,
  loadTranscriptRowHeightsFromSnapshot,
  parseTranscriptRowHeightsSession,
  recordTranscriptRowHeight,
  seedTranscriptRowHeightsForTests,
  transcriptRowHeightCountForTests,
  transcriptRowHeightForScope,
} from "./transcript-row-heights-persist.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../../store/app-state-snapshot.ts";
import {
  TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP,
  TRANSCRIPT_ROW_HEIGHT_VARIANT_CAP,
} from "../../../../shared/app-state-types.ts";

const key = (itemKey: string, variant = "collapsed", revision = "r1") => ({ itemKey, revision, variant });

describe("transcript-row-heights-persist", () => {
  beforeEach(() => {
    clearTranscriptRowHeightsForTests();
    resetAppStateSnapshotForTests();
  });

  it("stores and restores heights for their explicit session scope", async () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    recordTranscriptRowHeight(scope, key("tool-1"), 240.6);
    await flushTranscriptRowHeightsToDisk();

    clearTranscriptRowHeightsForTests();
    resetAppStateSnapshotForTests({
      version: 1,
      recents: [],
      transcriptRowHeights: getAppStateSnapshot().transcriptRowHeights,
    });
    loadTranscriptRowHeightsFromSnapshot();

    expect(transcriptRowHeightForScope(scope, key("tool-1"))).toBe(240.59375);
  });

  it("keeps heights isolated between sessions", () => {
    const first = { projectId: "p1", sessionId: "s1" };
    const second = { projectId: "p1", sessionId: "s2" };
    recordTranscriptRowHeight(first, key("tool-1"), 200);
    seedTranscriptRowHeightsForTests(second, { "tool-1": { revision: "r1", heights: { collapsed: 180 } } });
    expect(transcriptRowHeightForScope(first, key("tool-1"))).toBe(200);
    expect(transcriptRowHeightForScope(second, key("tool-1"))).toBe(180);
  });

  it("keeps collapsed and expanded measurements separate", () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    recordTranscriptRowHeight(scope, key("tool-1", "collapsed"), 32);
    recordTranscriptRowHeight(scope, key("tool-1", '["tool-1"]'), 376);

    expect(transcriptRowHeightForScope(scope, key("tool-1", "collapsed"))).toBe(32);
    expect(transcriptRowHeightForScope(scope, key("tool-1", '["tool-1"]'))).toBe(376);
  });

  it("forgets every layout of a row whose content changed", () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    recordTranscriptRowHeight(scope, key("span", "collapsed", "three-entries"), 64);
    recordTranscriptRowHeight(scope, key("span", "open", "three-entries"), 190);
    recordTranscriptRowHeight(scope, key("span", "collapsed", "five-entries"), 64);

    expect(transcriptRowHeightForScope(scope, key("span", "open", "three-entries"))).toBeUndefined();
    expect(transcriptRowHeightForScope(scope, key("span", "open", "five-entries"))).toBeUndefined();
    expect(transcriptRowHeightForScope(scope, key("span", "collapsed", "five-entries"))).toBe(64);
  });

  it("cycles a row's layouts on resize without evicting other rows", () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    for (let row = 0; row < 200; row += 1) {
      for (let width = 0; width < TRANSCRIPT_ROW_HEIGHT_VARIANT_CAP + 3; width += 1) {
        recordTranscriptRowHeight(scope, key(`row-${row}`, `w${width}`), 40 + width);
      }
    }
    expect(transcriptRowHeightCountForTests(scope)).toBe(200);
    const newest = TRANSCRIPT_ROW_HEIGHT_VARIANT_CAP + 2;
    expect(transcriptRowHeightForScope(scope, key("row-0", `w${newest}`))).toBe(40 + newest);
    expect(transcriptRowHeightForScope(scope, key("row-0", "w0"))).toBeUndefined();
  });

  it("keeps a long chat's rows, evicting the least recently seen beyond the cap", () => {
    const scope = { projectId: "p1", sessionId: "s1" };
    for (let row = 0; row <= TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP; row += 1) {
      recordTranscriptRowHeight(scope, key(`row-${row}`), 40);
      // The first row stays in view, so it stays recent.
      recordTranscriptRowHeight(scope, key("row-0"), 40);
    }
    expect(transcriptRowHeightCountForTests(scope)).toBe(TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP);
    expect(transcriptRowHeightForScope(scope, key("row-0"))).toBe(40);
    expect(transcriptRowHeightForScope(scope, key("row-1"))).toBeUndefined();
  });

  it("refreshes a read row before evicting the least recently used row", () => {
    const scope = { projectId: "p", sessionId: "read" };
    for (let row = 0; row < TRANSCRIPT_ROW_HEIGHTS_PER_SESSION_CAP; row += 1) {
      recordTranscriptRowHeight(scope, key(`row-${row}`, "initial"), row + 1);
    }
    expect(transcriptRowHeightForScope(scope, key("row-0", "initial"))).toBe(1);
    recordTranscriptRowHeight(scope, key("new", "initial"), 50);
    expect(transcriptRowHeightForScope(scope, key("row-0", "initial"))).toBe(1);
    expect(transcriptRowHeightForScope(scope, key("row-1", "initial"))).toBeUndefined();
  });

  it("drops saved rows in an earlier shape, which rebuild from measurement", () => {
    expect(parseTranscriptRowHeightsSession({
      projectId: "p1", sessionId: "s1", touchedAt: 1,
      rows: { "tool-1": { collapsed: 32 } },
    })).toBeNull();
  });

  it("debounces disk writes until flush", async () => {
    recordTranscriptRowHeight({ projectId: "p1", sessionId: "s1" }, key("tool-1"), 120);
    expect(getAppStateSnapshot().transcriptRowHeights).toBeUndefined();
    await flushTranscriptRowHeightsToDisk();
    expect(
      getAppStateSnapshot().transcriptRowHeights?.[0]?.rows["tool-1"],
    ).toEqual({ revision: "r1", heights: { collapsed: 120 } });
  });
});
