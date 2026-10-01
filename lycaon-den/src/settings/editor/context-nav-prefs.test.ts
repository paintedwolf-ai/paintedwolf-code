import { beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1, DEFAULT_CONTEXT_NAV_VISIBLE } from "../../../shared/app-state-types.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import {
  normalizeContextNavVisible,
  reorderContextNavVisible,
  resolveContextNavVisible,
  saveContextNavVisible,
  syncContextNavPrefsFromSnapshot,
  contextNavVisiblePref,
} from "./context-nav-prefs.ts";

describe("context-nav-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncContextNavPrefsFromSnapshot();
  });

  it("defaults hide Artifacts and Extensions", () => {
    expect(resolveContextNavVisible(undefined)).toEqual([
      ...DEFAULT_CONTEXT_NAV_VISIBLE,
    ]);
    expect(resolveContextNavVisible(undefined)).toContain("security");
    expect(resolveContextNavVisible(undefined)).toContain("cost");
    expect(resolveContextNavVisible(undefined)).not.toContain("artifacts");
    expect(resolveContextNavVisible(undefined)).not.toContain("extensions");
  });

  it("preserves an intentional empty visible list", () => {
    expect(resolveContextNavVisible({ visible: [] })).toEqual([]);
  });

  it("normalizes unknown and duplicate ids", () => {
    expect(
      normalizeContextNavVisible([
        "search",
        "nope",
        "search",
        "artifacts",
        "blueprints",
      ]),
    ).toEqual(["search", "artifacts", "blueprints"]);
  });

  it("reorders within the visible list", () => {
    expect(
      reorderContextNavVisible(
        ["search", "files", "security", "blueprints"],
        "blueprints",
        "search",
      ),
    ).toEqual(["blueprints", "search", "files", "security"]);
  });

  it("persists visibility and updates the reactive pref", async () => {
    await saveContextNavVisible(["search", "artifacts"]);
    expect(contextNavVisiblePref()).toEqual(["search", "artifacts"]);
  });
});
