// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import {
  clearComposerDraft,
  composerDraftForSession,
  flushComposerDraftsToDisk,
  hasComposerDraft,
  resetComposerDraftsForTests,
  setComposerDraft,
  syncComposerDraftsFromSnapshot,
} from "./composer-drafts.ts";

describe("composer-drafts", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.useFakeTimers();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetComposerDraftsForTests();
  });

  afterEach(() => {
    vi.useRealTimers();
    resetComposerDraftsForTests();
  });

  it("stores and clears per-session drafts", () => {
    setComposerDraft("sess-a", "half typed");
    setComposerDraft("sess-b", "other");
    expect(composerDraftForSession("sess-a")).toBe("half typed");
    expect(hasComposerDraft("sess-a")).toBe(true);
    clearComposerDraft("sess-a");
    expect(composerDraftForSession("sess-a")).toBe("");
    expect(hasComposerDraft("sess-a")).toBe(false);
    expect(composerDraftForSession("sess-b")).toBe("other");
  });

  it("persists drafts across snapshot rehydrate", async () => {
    setComposerDraft("sess-a", "survive reload");
    await vi.runAllTimersAsync();
    await flushComposerDraftsToDisk();
    expect(getAppStateSnapshot().composerDrafts?.["sess-a"]).toBe("survive reload");

    const loaded = await loadAppState();
    expect(loaded.composerDrafts?.["sess-a"]).toBe("survive reload");

    resetComposerDraftsForTests();
    setAppStateSnapshot(loaded);
    syncComposerDraftsFromSnapshot();
    expect(composerDraftForSession("sess-a")).toBe("survive reload");
  });

  it("joins ordinary draft persistence to the shared app-state batch", async () => {
    setComposerDraft("sess-a", "batched thought");

    await vi.advanceTimersByTimeAsync(299);
    expect(getAppStateSnapshot().composerDrafts).toBeUndefined();
    await vi.advanceTimersByTimeAsync(1);
    expect(getAppStateSnapshot().composerDrafts?.["sess-a"]).toBe(
      "batched thought",
    );
    expect((await loadAppState()).composerDrafts).toBeUndefined();

    await vi.advanceTimersByTimeAsync(250);
    expect((await loadAppState()).composerDrafts?.["sess-a"]).toBe(
      "batched thought",
    );
  });

  it("empty text removes the session entry from persisted state", async () => {
    setComposerDraft("sess-a", "temp");
    await flushComposerDraftsToDisk();
    clearComposerDraft("sess-a");
    await flushComposerDraftsToDisk();
    expect(getAppStateSnapshot().composerDrafts).toBeUndefined();
  });

  it("a draft on one session survives working in another and coming back", () => {
    setComposerDraft("sess-a", "half typed");
    // Switching away is just another session's writes; nothing clears A.
    setComposerDraft("sess-b", "different thought");
    setComposerDraft("sess-b", "");
    expect(composerDraftForSession("sess-a")).toBe("half typed");
  });

  it("the draft dot tracks non-whitespace text only", () => {
    expect(hasComposerDraft("sess-a")).toBe(false);
    setComposerDraft("sess-a", "   ");
    // Whitespace is not a thought worth flagging on a session row.
    expect(hasComposerDraft("sess-a")).toBe(false);
    setComposerDraft("sess-a", "real text");
    expect(hasComposerDraft("sess-a")).toBe(true);
    clearComposerDraft("sess-a");
    expect(hasComposerDraft("sess-a")).toBe(false);
  });
});
