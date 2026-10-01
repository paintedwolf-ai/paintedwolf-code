// @vitest-environment jsdom
import { describe, expect, it, beforeEach } from "vitest";
import {
  EMPTY_APP_STATE_V1,
} from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
import {
  resetWhatsNewPrefsForTests,
  saveLastSeenVersion,
  syncWhatsNewFromSnapshot,
  whatsNewLastSeenVersion,
  whatsNewPrefsReady,
} from "./whats-new-prefs.ts";

describe("whats-new-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetWhatsNewPrefsForTests(undefined, false);
  });

  it("syncs lastSeenVersion from the app-state snapshot", () => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      whatsNew: { lastSeenVersion: "0.1.0" },
    });
    syncWhatsNewFromSnapshot();
    expect(whatsNewPrefsReady()).toBe(true);
    expect(whatsNewLastSeenVersion()).toBe("0.1.0");
  });

  it("saveLastSeenVersion persists the whatsNew slice", async () => {
    syncWhatsNewFromSnapshot();
    await saveLastSeenVersion("0.2.0");
    expect(whatsNewLastSeenVersion()).toBe("0.2.0");
    expect(getAppStateSnapshot().whatsNew).toEqual({
      lastSeenVersion: "0.2.0",
    });
    await persistAppState({});
    expect(getAppStateSnapshot().whatsNew?.lastSeenVersion).toBe("0.2.0");
  });
});
