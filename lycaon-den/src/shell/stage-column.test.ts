// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../shared/app-state-types.ts";
import {
  resolveStageColumn,
  type StageColumnInput,
} from "./stage-placement.ts";
import {
  companionStageId,
  resetLayoutStoreForTests,
  saveStagePlacementMode,
  setSplitProjectId,
  syncLayoutFromSnapshot,
} from "./layout-store.ts";
import { resetAppStateSnapshotForTests } from "../store/app-state-snapshot.ts";

beforeEach(() => {
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  resetLayoutStoreForTests();
});

afterEach(() => resetLayoutStoreForTests());

const column = (over: Partial<StageColumnInput> = {}) =>
  resolveStageColumn({
    navStage: null,
    foregroundIsChat: true,
    hasChat: true,
    companion: null,
    splitMode: false,
    ...over,
  });

describe("stage column", () => {
  it("renders the foreground stage, split when the shell is in split mode", () => {
    expect(
      column({
        navStage: "files",
        foregroundIsChat: false,
        splitMode: true,
      }),
    ).toEqual({ splitLive: true, stageId: "files" });
    expect(column({ navStage: "files", foregroundIsChat: false })).toEqual({
      splitLive: false,
      stageId: "files",
    });
  });

  it("opens Files when split is selected and no companion is set", () => {
    expect(column({ splitMode: true })).toEqual({
      splitLive: true,
      stageId: "files",
    });
  });

  it("keeps the companion — and the split — when the foreground moves to a chat", () => {
    expect(column({ companion: "files", splitMode: true })).toEqual({
      splitLive: true,
      stageId: "files",
    });
  });

  it("drops the companion when the mode leaves split", () => {
    expect(column({ companion: "files" })).toEqual({
      splitLive: false,
      stageId: null,
    });
  });

  it("needs a conversation to sit beside", () => {
    expect(
      column({
        companion: "files",
        hasChat: false,
        splitMode: true,
      }),
    ).toEqual({ splitLive: false, stageId: null });
    expect(
      column({
        navStage: "files",
        foregroundIsChat: false,
        hasChat: false,
        splitMode: true,
      }),
    ).toEqual({ splitLive: false, stageId: "files" });
  });

  it("says what the split is without deciding whether it fits", () => {
    // Width and which column yields are not inputs; CSS controls those.
    expect(
      column({
        companion: "files",
        splitMode: true,
      }),
    ).toEqual({ splitLive: true, stageId: "files" });
  });

  it("gives the whole host back to Settings, Configuration, and All chats", () => {
    expect(
      column({
        foregroundIsChat: false,
        companion: "files",
        splitMode: true,
      }),
    ).toEqual({ splitLive: false, stageId: null });
  });
});

describe("companion stage", () => {
  it("keeps the companion kind when the project changes", async () => {
    await saveStagePlacementMode({ mode: "split", companion: "search" });
    setSplitProjectId("p1");
    expect(companionStageId()).toBe("search");
    setSplitProjectId("p1");
    expect(companionStageId()).toBe("search");
    setSplitProjectId("p2");
    expect(companionStageId()).toBe("search");
  });

  it("defaults a stored split with no companion to Files", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { mode: "split" },
    });
    syncLayoutFromSnapshot();
    expect(companionStageId()).toBe("files");
  });
});
