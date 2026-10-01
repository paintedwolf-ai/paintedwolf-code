import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  EMPTY_APP_STATE_V1,
  type DenNotificationPrefs,
} from "../../../shared/app-state-types.ts";
import { parseAppState } from "../../platform/persistence/app-state-parse.ts";
import {
  getAppStateSnapshot,
  persistAppState,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import {
  notificationFinishedPref,
  notificationNeedsApprovalPref,
  notificationNeedsYouPref,
  notificationsEnabledPref,
  resolveNotificationPrefs,
  saveNotificationFinished,
  saveNotificationsEnabled,
  syncNotificationPrefsFromSnapshot,
} from "./notification-prefs.ts";

describe("notification-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncNotificationPrefsFromSnapshot();
  });

  afterEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  });

  it("defaults all-on when the slice is absent", () => {
    expect(resolveNotificationPrefs(undefined)).toEqual({
      enabled: true,
      finished: true,
      needsYou: true,
      needsApproval: true,
    });
    expect(notificationsEnabledPref()).toBe(true);
    expect(notificationFinishedPref()).toBe(true);
  });

  it("persists and syncs round-trip through the snapshot", async () => {
    await saveNotificationsEnabled(false);
    await saveNotificationFinished(false);
    expect(getAppStateSnapshot().notifications).toEqual({
      enabled: false,
      finished: false,
    });
    expect(notificationsEnabledPref()).toBe(false);
    expect(notificationFinishedPref()).toBe(false);

    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      notifications: {
        enabled: true,
        finished: true,
        needsYou: false,
        needsApproval: true,
      },
    });
    syncNotificationPrefsFromSnapshot();
    expect(notificationsEnabledPref()).toBe(true);
    expect(notificationNeedsYouPref()).toBe(false);
    expect(notificationNeedsApprovalPref()).toBe(true);
  });

  it("parse keeps booleans and drops garbage", () => {
    const ok = parseAppState({
      version: 1,
      recents: [],
      notifications: {
        enabled: false,
        finished: true,
        needsYou: "nope",
        needsApproval: false,
      },
    });
    expect(ok.notifications).toEqual({
      enabled: false,
      finished: true,
      needsApproval: false,
    } satisfies DenNotificationPrefs);

    const bad = parseAppState({
      version: 1,
      recents: [],
      notifications: "nope",
    });
    expect(bad.notifications).toBeUndefined();
  });

  it("persistAppState merges notifications without clobbering siblings", async () => {
    await persistAppState({
      display: { diffWordWrap: false },
      notifications: { enabled: false },
    });
    await persistAppState({ notifications: { finished: false } });
    const snap = getAppStateSnapshot();
    expect(snap.display?.diffWordWrap).toBe(false);
    // Field-merge replaces the notifications object when provided — callers
    // spread the prior slice (notification-prefs save* helpers).
    expect(snap.notifications).toEqual({ finished: false });
  });
});
