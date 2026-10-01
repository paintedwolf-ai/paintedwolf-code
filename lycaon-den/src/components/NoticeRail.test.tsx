import { APP_SCOPE, sessionScope } from "../notices/notice-scope.ts";
import { render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  registerSessionPromptActions,
  resetNoticeActionSinksForTest,
} from "../notices/notice-actions.ts";
import { NoticeRail } from "./NoticeRail.tsx";

afterEach(() => resetNoticeActionSinksForTest());

describe("NoticeRail", () => {
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
    expect(screen.getByText("Model provider not configured")).toBeTruthy();
    expect(screen.getByText(/openai/)).toBeTruthy();
    expect(screen.getByText("Open Settings → AI providers")).toBeTruthy();

    screen.getByLabelText("Dismiss").click();
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
    expect(screen.queryByText("Spend ceiling reached")).toBeNull();
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
        onDismissAll={vi.fn()}
      />
    ));

    const notices = screen.getAllByTestId("notice");
    expect(notices).toHaveLength(2);
    // Older notice shows only its teaser (suggestedAction); latest notice shows its full message.
    expect(screen.queryByText("Check connection")).toBeTruthy();
    expect(screen.queryByText("First failure detail")).toBeNull();
    expect(screen.queryByText("Most recent failure detail")).toBeTruthy();
    expect(screen.queryByText("Retry now")).toBeTruthy();
    expect(screen.getByText("Dismiss all")).toBeTruthy();
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
    expect(actions[0]!.textContent).toBe("Keep going");
    expect(actions[1]!.textContent).toBe("Rewind and retry");

    actions[0]!.click();
    expect(keepGoing).toHaveBeenCalledOnce();
    expect(onDismiss).toHaveBeenCalledWith("n1");
  });
});
