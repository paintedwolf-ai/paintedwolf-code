import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TurnOutcomeMarker } from "./TurnOutcomeMarker.tsx";

afterEach(cleanup);

describe("TurnOutcomeMarker", () => {
  it("renders a calm marker for a user stop, not an error", () => {
    render(() => <TurnOutcomeMarker disposition="user_stopped" />);
    const marker = screen.getByTestId("turn-stopped-marker");
    expect(marker.textContent).toBe("Stopped");
    expect(marker.className).not.toContain("error");
    expect(marker.getAttribute("role")).toBeNull();
    expect(screen.queryByTestId("turn-error-retry")).toBeNull();
  });

  it("renders an error row with Retry for a failed turn", () => {
    const onRetry = vi.fn();
    render(() => <TurnOutcomeMarker disposition="turn_error" onRetry={onRetry} />);
    expect(screen.getByTestId("turn-error-row").getAttribute("role")).toBe("alert");
    fireEvent.click(screen.getByTestId("turn-error-retry"));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows the error without Retry when there is no ask to repeat", () => {
    render(() => <TurnOutcomeMarker disposition="turn_error" />);
    expect(screen.getByTestId("turn-error-row")).toBeTruthy();
    expect(screen.queryByTestId("turn-error-retry")).toBeNull();
  });

  it("renders an interruption as calm, with Retry — the turn did not fail", () => {
    const onRetry = vi.fn();
    render(() => <TurnOutcomeMarker disposition="interrupted" onRetry={onRetry} />);
    const row = screen.getByTestId("turn-interrupted-row");
    expect(row.textContent).toContain("interrupted");
    expect(row.className).not.toContain("error");
    expect(row.getAttribute("role")).toBeNull();
    expect(screen.queryByTestId("turn-error-row")).toBeNull();
    fireEvent.click(screen.getByTestId("turn-interrupted-retry"));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("says nothing after a normal finish", () => {
    render(() => <TurnOutcomeMarker disposition="completed" />);
    expect(screen.queryByTestId("turn-stopped-marker")).toBeNull();
    expect(screen.queryByTestId("turn-error-row")).toBeNull();
  });

  it("says nothing before any turn has settled", () => {
    render(() => <TurnOutcomeMarker disposition={undefined} />);
    expect(screen.queryByTestId("turn-stopped-marker")).toBeNull();
  });

  it("renders Keep going and Rewind and retry when there is progress on turn_error", () => {
    const onKeepGoing = vi.fn();
    const onRewindAndRetry = vi.fn();
    render(() => (
      <TurnOutcomeMarker
        disposition="turn_error"
        hasProgress={true}
        onKeepGoing={onKeepGoing}
        onRewindAndRetry={onRewindAndRetry}
      />
    ));
    expect(screen.queryByTestId("turn-error-retry")).toBeNull();
    const keepGoing = screen.getByTestId("turn-error-keep-going");
    expect(keepGoing.textContent).toBe("Keep going");
    fireEvent.click(keepGoing);
    expect(onKeepGoing).toHaveBeenCalledTimes(1);

    const rewindAndRetry = screen.getByTestId("turn-error-rewind-and-retry");
    expect(rewindAndRetry.textContent).toBe("Rewind and retry");
    fireEvent.click(rewindAndRetry);
    expect(onRewindAndRetry).toHaveBeenCalledTimes(1);
  });

  it("renders Keep going and Rewind and retry when there is progress on interrupted", () => {
    const onKeepGoing = vi.fn();
    const onRewindAndRetry = vi.fn();
    render(() => (
      <TurnOutcomeMarker
        disposition="interrupted"
        hasProgress={true}
        onKeepGoing={onKeepGoing}
        onRewindAndRetry={onRewindAndRetry}
      />
    ));
    expect(screen.queryByTestId("turn-interrupted-retry")).toBeNull();
    const keepGoing = screen.getByTestId("turn-interrupted-keep-going");
    expect(keepGoing.textContent).toBe("Keep going");
    fireEvent.click(keepGoing);
    expect(onKeepGoing).toHaveBeenCalledTimes(1);

    const rewindAndRetry = screen.getByTestId("turn-interrupted-rewind-and-retry");
    expect(rewindAndRetry.textContent).toBe("Rewind and retry");
    fireEvent.click(rewindAndRetry);
    expect(onRewindAndRetry).toHaveBeenCalledTimes(1);
  });
});
