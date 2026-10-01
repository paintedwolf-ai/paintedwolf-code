import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import {
  chooseSidebarScope,
  cycleSidebarScope,
  reviewScopeAvailable,
  getDeletedLines,
  getMarkMyEdits,
  getSidebarScope,
  hydrateReviewPinLabel,
  isComparisonOff,
  openReviewLens,
  setComparisonOff,
  setDeletedLines,
  setMarkMyEdits,
  resetFilesStagePaneForTests,
  setFilesStagePaneMode,
  setSidebarScope,
  subscribeDeletedLines,
  subscribeFilesStagePaneMode,
  subscribeReviewScope,
  syncReviewPaneFromSnapshot,
  getFilesStagePaneMode,
  toggleComparison,
  toggleDeletedLines,
} from "./review-pane.ts";
import {
  enterWalk,
  isWalking,
  resetWalkForTests,
} from "../walk/walk-store.ts";

describe("sidebar scope state", () => {
  beforeEach(() => {
    resetFilesStagePaneForTests();
    resetWalkForTests();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  });

  it("defaults to the working lens", () => {
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });
    expect(getFilesStagePaneMode("p1")).toBe("files");
  });

  it.each([false, true])("hydrates only matching pin metadata while comparison off is %s", (off) => {
    setSidebarScope("p1", { kind: "pin", pinId: "pin-1" });
    setComparisonOff("p1", off);
    setMarkMyEdits("p1", true);
    const listener = vi.fn();
    const stop = subscribeReviewScope(listener);
    hydrateReviewPinLabel("p2", "pin-1", "Other project");
    hydrateReviewPinLabel("p1", "pin-2", "Other pin");
    expect(listener).not.toHaveBeenCalled();
    hydrateReviewPinLabel("p1", "pin-1", "Before refactor");
    hydrateReviewPinLabel("p1", "pin-1", "Before refactor");
    expect(listener).toHaveBeenCalledTimes(1);
    expect(getSidebarScope("p1")).toEqual({ kind: "pin", pinId: "pin-1", label: "Before refactor" });
    expect(isComparisonOff("p1")).toBe(off);
    expect(getMarkMyEdits("p1")).toBe(true);
    setSidebarScope("p1", { kind: "new" });
    hydrateReviewPinLabel("p1", "pin-1", "Late pin metadata");
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });
    stop();
  });

  it("sets the turn scope in one notification", () => {
    const scopes: unknown[] = [];
    const unsub = subscribeReviewScope((id) => {
      if (id !== "p1") return;
      scopes.push(getSidebarScope("p1"));
    });
    setSidebarScope("p1", { kind: "turn" });
    unsub();
    expect(scopes).toHaveLength(1);
    expect(scopes[0]).toEqual({ kind: "turn" });
  });

  it("reset clears to the working lens", () => {
    setSidebarScope("p1", { kind: "turn" });
    resetFilesStagePaneForTests();
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });
  });

  it("openReviewLens sets the scope and shows the Review pane", () => {
    openReviewLens("p1", { kind: "turn" });
    expect(getFilesStagePaneMode("p1")).toBe("review");
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
  });

  it("exits Walk when the eye chooses this turn", async () => {
    const listProjectSourceWalk = vi.fn().mockResolvedValue({
      baseline: "session:s1",
      files: [],
      commit_available: true,
      git_changes: [], commands: [], turns: [],
    });
    const client = stubClient({
      listProjectSourceWalk,
    });

    await enterWalk("p1", client, "s1");
    chooseSidebarScope("p1", { kind: "turn" });

    expect(isWalking("p1")).toBe(false);
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(1);
  });

  it("exits Walk when its comparison scope is picked again", async () => {
    const listProjectSourceWalk = vi.fn().mockResolvedValue({
      baseline: "session:s1",
      files: [],
      commit_available: false,
      git_changes: [], commands: [], turns: [],
    });
    const client = stubClient({
      listProjectSourceWalk,
    });

    await enterWalk("p1", client, "s1");
    chooseSidebarScope("p1", { kind: "commit" });

    expect(isWalking("p1")).toBe(false);
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(1);
  });

  it("Off keeps the scope it was turned off over", () => {
    setSidebarScope("p1", { kind: "commit" });
    setComparisonOff("p1", true);
    expect(isComparisonOff("p1")).toBe(true);
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });

    setComparisonOff("p1", false);
    expect(isComparisonOff("p1")).toBe(false);
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });
  });

  it("picking a comparison turns the eye back on", () => {
    setComparisonOff("p1", true);
    setSidebarScope("p1", { kind: "new" });
    expect(isComparisonOff("p1")).toBe(false);

    setComparisonOff("p1", true);
    setSidebarScope("p1", { kind: "turn" });
    expect(isComparisonOff("p1")).toBe(false);
  });

  it("notifies once per Off change and not at all on a repeat", () => {
    const spy = vi.fn();
    const unsub = subscribeReviewScope(spy);
    setComparisonOff("p1", true);
    setComparisonOff("p1", true);
    unsub();
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("defaults to on and resets to on", () => {
    expect(isComparisonOff("p1")).toBe(false);
    setComparisonOff("p1", true);
    resetFilesStagePaneForTests();
    expect(isComparisonOff("p1")).toBe(false);
  });

  it("persists the comparison and off state independently per project", () => {
    setSidebarScope("p1", { kind: "commit" });
    setComparisonOff("p1", true);
    setSidebarScope("p2", { kind: "turn" });

    expect(getAppStateSnapshot().reviewLens?.byProject).toMatchObject({
      p1: { comparison: "commit", comparisonOff: true },
      p2: { comparison: "turn" },
    });
  });

  it("leaves the person's edits unmarked and folds removed lines by default", () => {
    expect(getMarkMyEdits("p1")).toBe(false);
    expect(getDeletedLines("p1")).toBe("folded");
  });

  it("re-resolves the scope once when Mark my edits changes, and persists it", () => {
    const spy = vi.fn();
    const unsub = subscribeReviewScope(spy);
    setMarkMyEdits("p1", true);
    setMarkMyEdits("p1", true);
    unsub();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(getAppStateSnapshot().reviewLens?.byProject.p1).toMatchObject({ markMyEdits: true });
  });

  it("changes how removed lines show without re-resolving the scope", () => {
    const scope = vi.fn();
    const shown = vi.fn();
    const unsubScope = subscribeReviewScope(scope);
    const unsubShown = subscribeDeletedLines(shown);
    setDeletedLines("p1", "inplace");
    unsubScope();
    unsubShown();
    expect(scope).not.toHaveBeenCalled();
    expect(shown).toHaveBeenCalledTimes(1);
    expect(getAppStateSnapshot().reviewLens?.byProject.p1).toMatchObject({ deletedLines: "inplace" });
  });

  it("hydrates whose edits are marked and how removed lines show", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      reviewLens: {
        byProject: {
          p1: { comparison: "new", markMyEdits: true, deletedLines: "inplace", touchedAt: 1 },
          p2: { comparison: "new", touchedAt: 0 },
        },
      },
    });

    syncReviewPaneFromSnapshot();

    expect(getMarkMyEdits("p1")).toBe(true);
    expect(getDeletedLines("p1")).toBe("inplace");
    expect(getMarkMyEdits("p2")).toBe(false);
    expect(getDeletedLines("p2")).toBe("folded");
  });

  it("hydrates each project's last eye choice", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      reviewLens: {
        byProject: {
          p1: { comparison: "pin:pin-1", comparisonOff: true, touchedAt: 2 },
          p2: { comparison: "commit", touchedAt: 1 },
        },
      },
    });

    syncReviewPaneFromSnapshot();

    expect(getSidebarScope("p1")).toEqual({ kind: "pin", pinId: "pin-1" });
    expect(isComparisonOff("p1")).toBe(true);
    expect(getSidebarScope("p2")).toEqual({ kind: "commit" });
    expect(isComparisonOff("p2")).toBe(false);
  });

  it("notifies once per turn selection", () => {
    const spy = vi.fn();
    const unsub = subscribeReviewScope(spy);
    setSidebarScope("p1", { kind: "turn" });
    unsub();
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("does not notify comparison consumers when only the visible pane changes", () => {
    const scopeSpy = vi.fn();
    const modeSpy = vi.fn();
    const stopScope = subscribeReviewScope(scopeSpy);
    const stopMode = subscribeFilesStagePaneMode(modeSpy);
    setFilesStagePaneMode("p1", "review");
    stopScope();
    stopMode();
    expect(scopeSpy).not.toHaveBeenCalled();
    expect(modeSpy).toHaveBeenCalledWith("p1");
  });

  it("toggleComparison toggles comparison off and on and exits Walk when turning off", async () => {
    const listProjectSourceWalk = vi.fn().mockResolvedValue({
      baseline: "session:s1",
      files: [],
      commit_available: true,
      git_changes: [],
      commands: [],
      turns: [],
    });
    const client = stubClient({ listProjectSourceWalk });
    await enterWalk("p1", client, "s1");
    expect(isWalking("p1")).toBe(true);

    expect(isComparisonOff("p1")).toBe(false);
    const turnedOff = toggleComparison("p1");
    expect(turnedOff).toBe(false);
    expect(isComparisonOff("p1")).toBe(true);
    expect(isWalking("p1")).toBe(false);

    const turnedOn = toggleComparison("p1");
    expect(turnedOn).toBe(true);
    expect(isComparisonOff("p1")).toBe(false);
  });

  it("cycleSidebarScope advances through standard comparison scopes", () => {
    const all = { chatSelected: true, commitAvailable: true };
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });

    expect(cycleSidebarScope("p1", 1, all)).toEqual({ kind: "turn" });
    expect(getSidebarScope("p1")).toEqual({ kind: "turn" });

    expect(cycleSidebarScope("p1", 1, all)).toEqual({ kind: "session" });
    expect(getSidebarScope("p1")).toEqual({ kind: "session" });

    expect(cycleSidebarScope("p1", 1, all)).toEqual({ kind: "commit" });
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });

    expect(cycleSidebarScope("p1", 1, all)).toEqual({ kind: "new" });
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });

    expect(cycleSidebarScope("p1", -1, all)).toEqual({ kind: "commit" });
    expect(getSidebarScope("p1")).toEqual({ kind: "commit" });

    setComparisonOff("p1", true);
    expect(cycleSidebarScope("p1", 1, all)).toEqual({ kind: "commit" });
    expect(isComparisonOff("p1")).toBe(false);
  });

  it("cycleSidebarScope skips comparisons the picker would disable", () => {
    const noChatNoGit = { chatSelected: false, commitAvailable: false };
    expect(cycleSidebarScope("p1", 1, noChatNoGit)).toEqual({ kind: "new" });
    expect(getSidebarScope("p1")).toEqual({ kind: "new" });

    const gitOnly = { chatSelected: false, commitAvailable: true };
    expect(cycleSidebarScope("p1", 1, gitOnly)).toEqual({ kind: "commit" });
    expect(cycleSidebarScope("p1", 1, gitOnly)).toEqual({ kind: "new" });
    expect(cycleSidebarScope("p1", -1, gitOnly)).toEqual({ kind: "commit" });

    const chatOnly = { chatSelected: true, commitAvailable: false };
    expect(cycleSidebarScope("p1", 1, chatOnly)).toEqual({ kind: "new" });
    expect(cycleSidebarScope("p1", -1, chatOnly)).toEqual({ kind: "session" });
    expect(reviewScopeAvailable({ kind: "commit" }, chatOnly)).toBe(false);
  });

  it("toggleDeletedLines toggles between inplace and folded", () => {
    expect(getDeletedLines("p1")).toBe("folded");
    expect(toggleDeletedLines("p1")).toBe("inplace");
    expect(getDeletedLines("p1")).toBe("inplace");
    expect(toggleDeletedLines("p1")).toBe("folded");
    expect(getDeletedLines("p1")).toBe("folded");
  });
});
