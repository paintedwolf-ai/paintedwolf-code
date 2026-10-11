import { describe, expect, it, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import {
  noteHealthResponse,
  resetStoreRevisionTracking,
} from "../../platform/connection/health.ts";
import { resetWhatsNewPrefsForTests } from "../../settings/system/whats-new-prefs.ts";
import { createNoticeStore } from "../../notices/notice-store.ts";
import { NoticeRail } from "../NoticeRail.tsx";
import { APP_SCOPE, noticeScopeKey } from "../../notices/notice-scope.ts";
import { WhatsNewNotice } from "./WhatsNewNotice.tsx";

vi.mock("../../settings/system/onboarding-prefs.ts", () => ({
  onboardingPrefsReady: () => true,
  firstRunSetupCompletedPref: () => true,
}));

describe("WhatsNewNotice", () => {
  it.each(["Got it", "Dismiss", "Dismiss all"])("acknowledges through %s across shared surfaces", async (action) => {
    const saveLatch = vi.fn(async (version: string) => resetWhatsNewPrefsForTests({ lastSeenVersion: version }));
    render(() => <><Notice currentVersion="0.2.0" notesFor={() => "Release notes"} saveLatch={saveLatch} /><NoticeRail notices={rows()} onDismiss={notices.dismiss} /></>);
    await waitFor(() => expect(rows()).toHaveLength(1));
    if (action === "Dismiss all") {
      notices.publish({ title: "Other condition", message: "Still works" }, APP_SCOPE);
      fireEvent.click(screen.getAllByRole("button", { name: "Dismiss all" })[0]!);
    } else {
      fireEvent.click(screen.getAllByRole("button", { name: action, exact: true })[0]!);
    }
    await waitFor(() => expect(saveLatch).toHaveBeenCalledExactlyOnceWith("0.2.0"));
    expect(rows()).toHaveLength(0);
  });

  it("withdraws on disposal without acknowledging the release", async () => {
    const saveLatch = vi.fn(async () => {});
    const { unmount } = render(() => <Notice currentVersion="0.2.0" notesFor={() => "Release notes"} saveLatch={saveLatch} />);
    await waitFor(() => expect(rows()).toHaveLength(1));
    unmount();
    expect(rows()).toHaveLength(0);
    expect(saveLatch).not.toHaveBeenCalled();
  });
  let notices: ReturnType<typeof createNoticeStore>;
  const rows = () => notices.index().get(noticeScopeKey(APP_SCOPE)) ?? [];
  function Notice(props: Omit<Parameters<typeof WhatsNewNotice>[0], "notices">) {
    return <><WhatsNewNotice {...props} notices={notices} /><NoticeRail notices={rows()} onDismiss={notices.dismiss} onDismissAll={() => notices.dismissScope(APP_SCOPE)} /></>;
  }
  beforeEach(() => {
    notices = createNoticeStore();
    resetStoreRevisionTracking();
    resetWhatsNewPrefsForTests({ lastSeenVersion: "0.1.0" });
  });

  it("shows when backend health arrives after the notice publisher mounts", async () => {
    render(() => (
      <Notice
        notesFor={() => "### Added\n\n- Cool feature"}
        saveLatch={vi.fn(async () => undefined)}
      />
    ));

    expect(screen.queryByTestId("notice-rail")).toBeNull();
    noteHealthResponse({
      status: "ok",
      version: "0.2.0",
      store_revision: 1,
      schema_version: 1,
      recovery_snapshot_available: false,
    });

    await waitFor(() => {
      expect(screen.getByTestId("notice-rail")).toBeTruthy();
    });
  });

  it("opens full notes with Read more and latches the version on Got it", async () => {
    const saveLatch = vi.fn(async (version: string) => {
      resetWhatsNewPrefsForTests({ lastSeenVersion: version });
    });

    render(() => (
      <Notice
        currentVersion="0.2.0"
        notesFor={() => "### Added\n\n- Cool feature"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("notice-rail")).toBeTruthy();
    });
    expect(screen.getByTestId("notice-rail").textContent).toMatch(
      /What’s new in 0\.2\.0/,
    );
    expect(screen.getByTestId("notice-rail").textContent).toMatch(
      /Review the latest changes/,
    );

    fireEvent.click(screen.getByRole("button", { name: "Read more" }));
    expect(screen.getByTestId("whats-new-dialog-notes").textContent).toMatch(
      /Cool feature/,
    );

    fireEvent.click(screen.getByTestId("whats-new-dialog-got-it"));

    await waitFor(() => {
      expect(saveLatch).toHaveBeenCalledWith("0.2.0");
      expect(screen.queryByTestId("notice-rail")).toBeNull();
    });
  });

  it("shows nothing when the latch already matches current", async () => {
    resetWhatsNewPrefsForTests({ lastSeenVersion: "0.2.0" });
    const saveLatch = vi.fn(async () => undefined);

    render(() => (
      <Notice
        currentVersion="0.2.0"
        notesFor={() => "### Added\n\n- Cool"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.queryByTestId("notice-rail")).toBeNull();
    });
    expect(saveLatch).not.toHaveBeenCalled();
  });

  it("keeps the notice until Got it when the reader is closed", async () => {
    const saveLatch = vi.fn(async () => undefined);
    render(() => (
      <Notice
        currentVersion="0.2.0"
        notesFor={() => "### Changed\n\n- Cool"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("notice-rail")).toBeTruthy();
    });
    fireEvent.click(screen.getByRole("button", { name: "Read more" }));
    fireEvent.click(screen.getByTestId("whats-new-dialog-close"));

    expect(screen.getByTestId("notice-rail")).toBeTruthy();
    expect(saveLatch).not.toHaveBeenCalled();
  });

  it("seeds silently on unset latch and shows nothing", async () => {
    resetWhatsNewPrefsForTests(undefined);
    const saveLatch = vi.fn(async (version: string) => {
      resetWhatsNewPrefsForTests({ lastSeenVersion: version });
    });

    render(() => (
      <Notice
        currentVersion="0.1.0"
        notesFor={() => "### Added\n\n- First"}
        saveLatch={saveLatch}
      />
    ));

    await waitFor(() => {
      expect(saveLatch).toHaveBeenCalledWith("0.1.0");
    });
    expect(screen.queryByTestId("notice-rail")).toBeNull();
  });
});
