// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import {
  fullDebugLoggingPref,
  resolveFullDebugLogging,
  resolveVerboseMode,
  saveFullDebugLogging,
  saveVerboseMode,
  syncDebugPrefsFromSnapshot,
  verboseModePref,
} from "./debug-prefs.ts";
import { loadAppState } from "../../platform/persistence/app-state.ts";
import {
  getAppStateSnapshot,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";

describe("debug-prefs", () => {
  beforeEach(() => {
    localStorage.clear();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    syncDebugPrefsFromSnapshot();
  });

  it("defaults verbose mode to false", () => {
    expect(resolveVerboseMode()).toBe(false);
    expect(verboseModePref()).toBe(false);
  });

  it("persists verbose mode toggle", async () => {
    await saveVerboseMode(true);
    expect(verboseModePref()).toBe(true);
    expect(getAppStateSnapshot().debug?.verboseMode).toBe(true);
    const loaded = await loadAppState();
    expect(loaded.debug?.verboseMode).toBe(true);
  });

  it("defaults full debug logging to false", () => {
    expect(resolveFullDebugLogging()).toBe(false);
    expect(fullDebugLoggingPref()).toBe(false);
  });

  it("persists full debug logging toggle without clobbering verbose mode", async () => {
    await saveVerboseMode(true);
    await saveFullDebugLogging(true);
    expect(fullDebugLoggingPref()).toBe(true);
    expect(verboseModePref()).toBe(true);
    expect(getAppStateSnapshot().debug?.fullDebugLogging).toBe(true);
    const loaded = await loadAppState();
    expect(loaded.debug?.fullDebugLogging).toBe(true);
    expect(loaded.debug?.verboseMode).toBe(true);
  });
});
