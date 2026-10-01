import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { CriticalStop } from "./CriticalStop.tsx";

describe("CriticalStop", () => {
  it("renders the condition and fires its recovery", () => {
    const onConnect = vi.fn();
    render(() => (
      <CriticalStop
        code="offline"
        title="Could not start"
        message="Start the sidecar and try again."
        primaryAction={{ label: "Try again", onClick: onConnect }}
      />
    ));

    const stop = screen.getByTestId("critical-stop");
    expect(stop.textContent).toContain("Could not start");
    expect(stop.textContent).toContain("Start the sidecar and try again.");
    // The code is the e2e anchor, not decoration.
    expect(stop.getAttribute("data-code")).toBe("offline");

    fireEvent.click(screen.getByTestId("critical-stop-primary"));
    expect(onConnect).toHaveBeenCalledOnce();
  });

  it("announces assertively — the stage is gone, not merely degraded", () => {
    render(() => (
      <CriticalStop code="offline" title="Could not start" message="…" />
    ));
    expect(screen.getByTestId("critical-stop").getAttribute("role")).toBe(
      "alert",
    );
  });

  it("renders the host remedy only when there is one", () => {
    const { unmount } = render(() => (
      <CriticalStop
        code="OS_BELOW_FLOOR"
        title="macOS is too old"
        message="This app needs macOS 14 or newer."
        detail="Update macOS, then reopen the app."
      />
    ));
    expect(screen.getByTestId("critical-stop-detail").textContent).toBe(
      "Update macOS, then reopen the app.",
    );
    unmount();

    render(() => (
      <CriticalStop code="offline" title="Could not start" message="…" />
    ));
    expect(screen.queryByTestId("critical-stop-detail")).toBeNull();
  });
});
