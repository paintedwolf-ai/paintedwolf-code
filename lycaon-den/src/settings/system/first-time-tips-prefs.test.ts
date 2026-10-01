import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { parseAppState } from "../../platform/persistence/app-state-parse.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import {
  dismissFirstTimeTip,
  firstTimeTipsEnabledPref,
  isFirstTimeTipDismissed,
  saveFirstTimeTipsEnabled,
  syncFirstTimeTipsFromSnapshot,
} from "./first-time-tips-prefs.ts";

describe("first-time-tips-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncFirstTimeTipsFromSnapshot();
  });

  afterEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  });

  it("defaults enabled and persists the main switch without losing dismissals", async () => {
    expect(firstTimeTipsEnabledPref()).toBe(true);
    await dismissFirstTimeTip("files-review-scope");
    await saveFirstTimeTipsEnabled(false);
    expect(firstTimeTipsEnabledPref()).toBe(false);
    expect(isFirstTimeTipDismissed("files-review-scope")).toBe(true);
    expect(getAppStateSnapshot().firstTimeTips).toEqual({
      enabled: false,
      dismissed: ["files-review-scope"],
    });
  });

  it("keeps only known catalog ids", () => {
    const state = parseAppState({
      version: 1,
      recents: [],
      firstTimeTips: {
        enabled: false,
        dismissed: ["files-review-scope", "unknown-tip", "files-review-scope"],
      },
    });
    expect(state.firstTimeTips).toEqual({
      enabled: false,
      dismissed: ["files-review-scope"],
    });
  });
});
