import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { VerifyEditor } from "./VerifyEditor.tsx";
import { createAppStore } from "../../store/app-state.ts";
import type { VerifySettingsResponse } from "../../api/types.ts";

const getVerifySettings = vi.fn();
const updateVerifySettings = vi.fn();

const verifyClient = stubClient({
  getVerifySettings,
  updateVerifySettings,
});

describe("VerifyEditor", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getVerifySettings.mockResolvedValue({
      scope: "project",
      test: "",
      verify_path: ".paintedwolf/verify.yaml",
      detected_command: "./task check",
      detected_source: "README.md",
      backend_configured: true,
    } satisfies VerifySettingsResponse);
    updateVerifySettings.mockImplementation(async () => ({
      scope: "project",
      test: "./task check",
      verify_path: ".paintedwolf/verify.yaml",
      backend_configured: true,
    } satisfies VerifySettingsResponse));
  });

  it("distinguishes the default check from required workflow gates", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <VerifyEditor client={verifyClient} appStore={appStore} projectId="proj-1" />
    ));
    const hint = await screen.findByTestId("verify-editor-hint");
    expect(hint.textContent).toContain("default project check");
    expect(hint.textContent).toContain("does not run the full suite after every edit");
    expect(hint.textContent).toContain("workflow test gates still need a pass");
  });

  it("accepts detected candidate without auto-applying", async () => {
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <VerifyEditor client={verifyClient} appStore={appStore} projectId="proj-1" />
    ));
    const banner = await screen.findByTestId("verify-editor-detect-banner");
    expect(banner.textContent).toContain("Found");
    expect(banner.textContent).toContain("README.md");
    expect(banner.textContent).toContain("proposes only");
    expect(banner.textContent).toContain("applies silently");
    expect(updateVerifySettings).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("verify-editor-accept-detected"));
    fireEvent.click(screen.getByTestId("verify-editor-save"));
    await waitFor(() => {
      expect(updateVerifySettings).toHaveBeenCalledWith(
        { test: "./task check" },
        "proj-1",
      );
    });
  });

  it("re-fetches when async detection bumps the verify revision", async () => {
    // First load: no proposal yet (detection still running).
    getVerifySettings.mockResolvedValueOnce({
      scope: "project",
      test: "",
      verify_path: ".paintedwolf/verify.yaml",
      backend_configured: true,
    } satisfies VerifySettingsResponse);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <VerifyEditor client={verifyClient} appStore={appStore} projectId="proj-1" />
    ));
    await waitFor(() => expect(getVerifySettings).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("verify-editor-detect-banner")).toBeNull();

    // Settings SSE landed → revision bumps → re-fetch surfaces the proposal.
    appStore.actions.bumpVerifyDetectRevision();
    expect(await screen.findByTestId("verify-editor-detect-banner")).toBeTruthy();
    expect(getVerifySettings).toHaveBeenCalledTimes(2);
  });

  it("is disabled when backend is not configured", async () => {
    getVerifySettings.mockResolvedValue({
      scope: "global",
      test: "",
      verify_path: ".paintedwolf/verify.yaml",
      backend_configured: false,
    } satisfies VerifySettingsResponse);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <VerifyEditor client={verifyClient} appStore={appStore} projectId="proj-1" />
    ));
    expect(await screen.findByTestId("verify-editor-disabled")).toBeTruthy();
  });
});
