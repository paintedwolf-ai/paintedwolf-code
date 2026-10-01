import { stubClient } from "../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { VerifySettingsResponse } from "../api/types.ts";
import { createAppStore } from "../store/app-state.ts";
import { createVerifyTestSuggestion } from "../settings/extensions/verify-test-suggestion.ts";
import { VerifyTestNudge } from "./VerifyTestNudge.tsx";

const getVerifySettings = vi.fn();
const updateVerifySettings = vi.fn();
const dismissVerifySettings = vi.fn();

const client = stubClient({
  getVerifySettings,
  updateVerifySettings,
  dismissVerifySettings,
});

const suggestDoc: VerifySettingsResponse = {
  scope: "project",
  test: "",
  verify_path: ".paintedwolf/verify.yaml",
  detected_command: "./task check",
  detected_source: "README.md",
  backend_configured: true,
  suggestion_state: "suggest",
};

function renderNudge(onOpenSettings?: () => void) {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  render(() => {
    const suggestion = createVerifyTestSuggestion({
      client: () => client,
      appStore,
      projectId: () => "proj-1",
    });
    return (
      <VerifyTestNudge
        suggestion={suggestion}
        onOpenSettings={onOpenSettings}
      />
    );
  });
  return appStore;
}

describe("VerifyTestNudge", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getVerifySettings.mockResolvedValue(suggestDoc);
    updateVerifySettings.mockResolvedValue({
      ...suggestDoc,
      test: "./task check",
      suggestion_state: "accepted",
    } satisfies VerifySettingsResponse);
    dismissVerifySettings.mockResolvedValue({
      ...suggestDoc,
      suggestion_state: "dismissed",
      dismissed: true,
    } satisfies VerifySettingsResponse);
  });

  it("renders on suggestion_state=suggest and sets the detected command", async () => {
    renderNudge();
    const nudge = await screen.findByTestId("verify-test-nudge");
    expect(nudge.textContent).toContain("./task check");
    expect(nudge.textContent).toContain("default");
    expect(nudge.textContent).toContain("not a requirement to run the full suite after every edit");
    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    await waitFor(() =>
      expect(updateVerifySettings).toHaveBeenCalledWith(
        { test: "./task check" },
        "proj-1",
      ),
    );
  });

  it("records a sticky dismissal and stops showing", async () => {
    renderNudge();
    await screen.findByTestId("verify-test-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-secondary"));
    await waitFor(() =>
      expect(dismissVerifySettings).toHaveBeenCalledWith(
        { dismissed: true },
        "proj-1",
      ),
    );
    await waitFor(() =>
      expect(screen.queryByTestId("verify-test-nudge")).toBeNull(),
    );
  });

  it("opens the settings panel from the tertiary action", async () => {
    const onOpenSettings = vi.fn();
    renderNudge(onOpenSettings);
    await screen.findByTestId("verify-test-nudge");
    fireEvent.click(screen.getByTestId("system-nudge-tertiary"));
    expect(onOpenSettings).toHaveBeenCalledOnce();
  });

  it("omits the settings action when no handler is provided", async () => {
    renderNudge();
    await screen.findByTestId("verify-test-nudge");
    expect(screen.queryByTestId("system-nudge-tertiary")).toBeNull();
  });

  it("stays hidden for non-suggest states", async () => {
    getVerifySettings.mockResolvedValue({
      ...suggestDoc,
      suggestion_state: "waiting",
      detected_command: undefined,
    } satisfies VerifySettingsResponse);
    renderNudge();
    await waitFor(() => expect(getVerifySettings).toHaveBeenCalled());
    expect(screen.queryByTestId("verify-test-nudge")).toBeNull();
  });
});
