import { stubClient } from "../../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GeneralSettingsPanel } from "./GeneralSettingsPanel.tsx";
import {
  recordNotificationPermission,
  syncNotificationPrefsFromSnapshot,
} from "../../../settings/chat/notification-prefs.ts";
import { syncFirstTimeTipsFromSnapshot } from "../../../settings/system/first-time-tips-prefs.ts";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../../store/app-state-snapshot.ts";
import { resetFileSummariesSetting } from "../../../settings/editor/file-summary-settings.ts";

const { ensurePermission } = vi.hoisted(() => ({
  ensurePermission: vi.fn<() => Promise<"granted" | "denied" | "unavailable">>(),
}));

vi.mock("../../../platform/desktop/notifications.ts", () => ({
  ensureNotificationPermission: ensurePermission,
  sendNotification: vi.fn(async () => undefined),
}));

vi.mock("../../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: async () => actual.getAppStateSnapshot(),
  };
});

describe("GeneralSettingsPanel notifications tab", () => {
  afterEach(() => {
    cleanup();
    recordNotificationPermission(null);
    ensurePermission.mockReset();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncNotificationPrefsFromSnapshot();
    syncFirstTimeTipsFromSnapshot();
    resetFileSummariesSetting();
  });

  it("renders toggles, test button, and denied-permission message", async () => {
    recordNotificationPermission("denied");
    render(() => (
      <GeneralSettingsPanel
        initialTab="notifications"
        version="0.0.0-test"
      />
    ));

    expect(
      await screen.findByTestId("general-panel-notifications"),
    ).toBeTruthy();
    expect(screen.getByTestId("notifications-enabled")).toBeTruthy();
    expect(screen.getByTestId("notifications-finished")).toBeTruthy();
    expect(screen.getByTestId("notifications-needs-you")).toBeTruthy();
    expect(screen.getByTestId("notifications-needs-approval")).toBeTruthy();
    expect(screen.getByTestId("notifications-send-test")).toBeTruthy();
    const denied = screen.getByTestId("notifications-approval-denied");
    expect(denied.textContent).toContain("Painted Wolf Code");
    expect(denied.textContent?.toLowerCase()).not.toContain("lycaon");
  });

  it("replaces permission feedback with the latest native outcome", async () => {
    render(() => <GeneralSettingsPanel initialTab="notifications" version="0.0.0-test" />);
    const button = await screen.findByTestId("notifications-send-test");
    await waitFor(() => expect((button as HTMLButtonElement).disabled).toBe(false));
    for (const result of ["unavailable", "denied", "granted"] as const) {
      ensurePermission.mockResolvedValueOnce(result);
      fireEvent.click(button);
      await waitFor(() => {
        expect(screen.queryByTestId("notifications-shell-unavailable") != null).toBe(result === "unavailable");
        expect(screen.queryByTestId("notifications-approval-denied") != null).toBe(result === "denied");
      });
    }
  });

  it("locks the notification kinds under the main switch", async () => {
    render(() => (
      <GeneralSettingsPanel
        initialTab="notifications"
        version="0.0.0-test"
      />
    ));

    const main = (await screen.findByTestId(
      "notifications-enabled",
    )) as HTMLInputElement;
    const group = () =>
      screen.getByTestId("notifications-finished").closest("fieldset");
    await waitFor(() => expect(main.disabled).toBe(false));
    expect(group()?.disabled).toBe(!main.checked);
    fireEvent.click(main);
    await waitFor(() => expect(group()?.disabled).toBe(!main.checked));
    expect(group()?.contains(screen.getByTestId("notifications-needs-approval"))).toBe(
      true,
    );
  });

  it("persists the first-time tips main switch from Display", async () => {
    render(() => (
      <GeneralSettingsPanel
        initialTab="display"
        version="0.0.0-test"
      />
    ));

    const toggle = await screen.findByTestId("display-first-time-tips");
    expect((toggle as HTMLInputElement).checked).toBe(true);
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(getAppStateSnapshot().firstTimeTips?.enabled).toBe(false),
    );
  });

  it("updates the shared File summaries backend switch from Display", async () => {
    const updateFileSummariesSettings = vi.fn().mockResolvedValue({ enabled: false });
    const client = stubClient({
      getFileSummariesSettings: vi.fn().mockResolvedValue({ enabled: true }),
      updateFileSummariesSettings,
    });
    render(() => (
      <GeneralSettingsPanel
        initialTab="display"
        version="0.0.0-test"
        client={client}
      />
    ));

    const toggle = await screen.findByTestId("display-file-summaries");
    await waitFor(() => expect((toggle as HTMLInputElement).disabled).toBe(false));
    fireEvent.click(toggle);
    await waitFor(() =>
      expect(updateFileSummariesSettings).toHaveBeenCalledWith({ enabled: false }),
    );
  });
});
