import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import {
  noteHealthResponse,
  resetStoreRevisionTracking,
} from "../../platform/connection/health.ts";
import { resetWhatsNewPrefsForTests } from "../../settings/system/whats-new-prefs.ts";
import { WhatsNewCard } from "./WhatsNewCard.tsx";

vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  onboardingPrefsReady: () => true,
  firstRunSetupCompletedPref: () => true,
}));

describe("WhatsNewCard", () => {
  beforeEach(() => {
    resetStoreRevisionTracking();
    resetWhatsNewPrefsForTests({ lastSeenVersion: "0.1.0" });
  });

  it("shows when backend health arrives after Home mounts", async () => {
    render(() => (
      <WhatsNewCard
        notesFor={() => "### Added\n\n- Cool feature"}
        saveLatch={vi.fn(async () => undefined)}
      />
    ));

    expect(screen.queryByTestId("whats-new-card")).toBeNull();
    noteHealthResponse({
      status: "ok",
      version: "0.2.0",
      store_revision: 1,
      schema_version: 1,
      recovery_snapshot_available: false,
    });

    await waitFor(() => {
      expect(screen.getByTestId("whats-new-card")).toBeTruthy();
    });
  });

  it("opens full notes with Read more and latches the version on Got it", async () => {
    const saveLatch = vi.fn(async (version: string) => {
      resetWhatsNewPrefsForTests({ lastSeenVersion: version });
    });

    render(() => (
      <WhatsNewCard
        currentVersion="0.2.0"
        notesFor={() => "### Added\n\n- Cool feature"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("whats-new-card")).toBeTruthy();
    });
    expect(screen.getByTestId("whats-new-nudge").textContent).toMatch(
      /What’s new in 0\.2\.0/,
    );
    expect(screen.getByTestId("whats-new-summary").textContent).toMatch(
      /Review the latest changes/,
    );

    fireEvent.click(screen.getByTestId("whats-new-read-more"));
    expect(screen.getByTestId("whats-new-dialog-notes").textContent).toMatch(
      /Cool feature/,
    );

    fireEvent.click(screen.getByTestId("whats-new-dialog-got-it"));

    await waitFor(() => {
      expect(saveLatch).toHaveBeenCalledWith("0.2.0");
      expect(screen.queryByTestId("whats-new-card")).toBeNull();
    });
  });

  it("shows nothing when the latch already matches current", async () => {
    resetWhatsNewPrefsForTests({ lastSeenVersion: "0.2.0" });
    const saveLatch = vi.fn(async () => undefined);

    render(() => (
      <WhatsNewCard
        currentVersion="0.2.0"
        notesFor={() => "### Added\n\n- Cool"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.queryByTestId("whats-new-card")).toBeNull();
    });
    expect(saveLatch).not.toHaveBeenCalled();
  });

  it("keeps the notice until Got it when the reader is closed", async () => {
    const saveLatch = vi.fn(async () => undefined);
    render(() => (
      <WhatsNewCard
        currentVersion="0.2.0"
        notesFor={() => "### Changed\n\n- Cool"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("whats-new-card")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("whats-new-read-more"));
    fireEvent.click(screen.getByTestId("whats-new-dialog-close"));

    expect(screen.getByTestId("whats-new-card")).toBeTruthy();
    expect(saveLatch).not.toHaveBeenCalled();
  });

  it("seeds silently on unset latch and shows nothing", async () => {
    resetWhatsNewPrefsForTests(undefined);
    const saveLatch = vi.fn(async (version: string) => {
      resetWhatsNewPrefsForTests({ lastSeenVersion: version });
    });

    render(() => (
      <WhatsNewCard
        currentVersion="0.1.0"
        notesFor={() => "### Added\n\n- First"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(saveLatch).toHaveBeenCalledWith("0.1.0");
    });
    expect(screen.queryByTestId("whats-new-card")).toBeNull();
  });
});
