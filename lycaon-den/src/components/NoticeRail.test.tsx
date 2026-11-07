import { APP_SCOPE, sessionScope } from "../notices/notice-scope.ts";
import { render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { assertNamedControl } from "../test/controls.ts";
import {
  registerSessionPromptActions,
  resetNoticeActionSinksForTest,
} from "../notices/notice-actions.ts";
import { NoticeRail } from "./NoticeRail.tsx";

afterEach(() => resetNoticeActionSinksForTest());

describe("NoticeRail", () => {
  it("keeps acknowledgement notices past informational expiry", () => {
    vi.useFakeTimers();
    try {
      const onDismiss = vi.fn();
      render(() => <NoticeRail notices={[{ id: "release", severity: "info", title: "Release", message: "Notes", createdAt: Date.now(), scope: APP_SCOPE, interaction: { autoDismiss: false, actions: [] } }]} onDismiss={onDismiss} />);
      vi.advanceTimersByTime(60_000);
      expect(onDismiss).not.toHaveBeenCalled();
    } finally { vi.useRealTimers(); }
  });
  it("renders inline rail with expanded latest notice", () => {
    const onDismiss = vi.fn();
    render(() => (
      <NoticeRail
        notices={[
          {
            id: "n1",
            severity: "error",
            title: "Model provider not configured",
            message: 'provider "openai" is not configured',
            suggestedAction: "Open Settings → AI providers",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
        ]}
        onDismiss={onDismiss}
      />
    ));

    expect(screen.getByTestId("notice-rail")).toBeTruthy();
    // The message renders only while the notice is expanded.
    expect(screen.getByText('provider "openai" is not configured')).toBeTruthy();

    assertNamedControl(screen.getByTestId("notice-dismiss"), "button").click();
    expect(onDismiss).toHaveBeenCalledWith("n1");
  });

  it("hides session_spend_ceiling_reached so the nudge controls that UX", () => {
    render(() => (
      <NoticeRail
        notices={[
          {
            id: "ceiling",
            severity: "error",
            title: "Spend ceiling reached",
            message: "Stopped at ceiling",
            code: "session_spend_ceiling_reached",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
          {
            id: "other",
            severity: "error",
            title: "Other notice",
            message: "Still visible",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
        ]}
        onDismiss={vi.fn()}
      />
    ));
    // The spend-ceiling notice is filtered out entirely, so only the other row renders.
    expect(screen.getAllByTestId("notice")).toHaveLength(1);
    expect(screen.getByText("Other notice")).toBeTruthy();
  });

  it("uses status live region and can mute polite announcements", () => {
    const { getByTestId, unmount } = render(() => (
      <NoticeRail
        notices={[
          {
            id: "n1",
            severity: "info",
            title: "Synced",
            message: "Index caught up",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
        ]}
        onDismiss={vi.fn()}
      />
    ));
    const rail = getByTestId("notice-rail");
    expect(rail.getAttribute("role")).toBe("status");
    expect(rail.getAttribute("aria-live")).toBe("polite");
    unmount();

    const muted = render(() => (
      <NoticeRail
        notices={[
          {
            id: "n1",
            severity: "info",
            title: "Synced",
            message: "Index caught up",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
        ]}
        onDismiss={vi.fn()}
        announceLive={false}
      />
    ));
    expect(muted.getByTestId("notice-rail").getAttribute("aria-live")).toBe("off");
  });

  it("expands only the latest notice by default", () => {
    render(() => (
      <NoticeRail
        notices={[
          {
            id: "older",
            severity: "warning",
            title: "Earlier issue",
            message: "First failure detail",
            suggestedAction: "Check connection",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
          {
            id: "newer",
            severity: "error",
            title: "Latest issue",
            message: "Most recent failure detail",
            suggestedAction: "Retry now",
            createdAt: Date.now(),
      scope: APP_SCOPE,
          },
        ]}
        onDismiss={vi.fn()}
      />
    ));

    const notices = screen.getAllByTestId("notice");
    expect(notices).toHaveLength(2);
    // Older notice shows only its teaser (suggestedAction); latest notice shows its full message.
    expect(notices[0]!.querySelector(".den-notice-teaser")).not.toBeNull();
    expect(screen.queryByText("First failure detail")).toBeNull();
    expect(screen.queryByText("Most recent failure detail")).toBeTruthy();
    expect(notices[1]!.querySelector(".den-notice-action")).not.toBeNull();
  });

  it("renders multiple action buttons and invokes callback on click", () => {
    const onDismiss = vi.fn();
    const keepGoing = vi.fn();
    const rewindAndRetry = vi.fn();
    registerSessionPromptActions("s1", {
      retry: vi.fn(),
      keepGoing,
      rewindAndRetry,
    });

    render(() => (
      <NoticeRail
        notices={[
          {
            id: "n1",
            severity: "error",
            title: "Provider server error",
            message: "500 Internal Server Error",
            actions: ["prompt_keep_going", "prompt_rewind_and_retry"],
            createdAt: Date.now(),
            scope: sessionScope("p1", "s1"),
          },
        ]}
        onDismiss={onDismiss}
      />
    ));

    const actions = screen.getAllByTestId("notice-action");
    expect(actions).toHaveLength(2);

    actions[0]!.click();
    expect(keepGoing).toHaveBeenCalledOnce();
    expect(onDismiss).toHaveBeenCalledWith("n1");
  });
});
