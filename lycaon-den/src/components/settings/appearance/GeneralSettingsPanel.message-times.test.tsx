import "../../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GeneralSettingsPanel } from "./GeneralSettingsPanel.tsx";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../../store/app-state-snapshot.ts";
import { messageTimesPref, syncChatPrefsFromSnapshot } from "../../../settings/chat/chat-prefs.ts";

vi.mock("../../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: async () => actual.getAppStateSnapshot(),
  };
});

describe("GeneralSettingsPanel message times", () => {
  afterEach(() => {
    cleanup();
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    syncChatPrefsFromSnapshot();
  });

  it("shows message times on hover until the person picks always", async () => {
    render(() => <GeneralSettingsPanel initialTab="display" version="0.0.0-test" />);
    const hover = await screen.findByTestId("display-message-times-hover");
    await waitFor(() => expect(hover.getAttribute("aria-pressed")).toBe("true"));

    fireEvent.click(screen.getByTestId("display-message-times-always"));
    expect(messageTimesPref()).toBe("always");
    await waitFor(() => expect(getAppStateSnapshot().chat?.messageTimes).toBe("always"));
    expect(screen.getByTestId("display-message-times-always").getAttribute("aria-pressed")).toBe("true");
  });

  it("opens on the stored choice", async () => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1, chat: { messageTimes: "always" } });
    render(() => <GeneralSettingsPanel initialTab="display" version="0.0.0-test" />);
    const always = await screen.findByTestId("display-message-times-always");
    await waitFor(() => expect(always.getAttribute("aria-pressed")).toBe("true"));
  });
});
