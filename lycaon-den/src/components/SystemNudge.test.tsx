import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { SystemNudge } from "./SystemNudge.tsx";

describe("SystemNudge", () => {
  it("renders title, description, and fires actions", () => {
    const onPrimary = vi.fn();
    const onSecondary = vi.fn();
    render(() => (
      <SystemNudge
        testId="nudge"
        title="Set a test command?"
        description={<span>Detected ./task check</span>}
        primaryAction={{ label: "Set command", onClick: onPrimary }}
        secondaryAction={{ label: "Dismiss", onClick: onSecondary }}
        onDismiss={vi.fn()}
      />
    ));
    expect(screen.getByTestId("nudge").textContent).toContain(
      "Set a test command?",
    );
    expect(screen.getByTestId("nudge").textContent).toContain(
      "Detected ./task check",
    );
    fireEvent.click(screen.getByTestId("system-nudge-primary"));
    expect(onPrimary).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId("system-nudge-secondary"));
    expect(onSecondary).toHaveBeenCalledOnce();
  });

  it("renders and fires the tertiary action when provided", () => {
    const onTertiary = vi.fn();
    render(() => (
      <SystemNudge
        title="Set a test command?"
        primaryAction={{ label: "Set command", onClick: vi.fn() }}
        tertiaryAction={{ label: "Choose your own", onClick: onTertiary }}
        onDismiss={vi.fn()}
      />
    ));
    const btn = screen.getByTestId("system-nudge-tertiary");
    expect(btn.textContent).toContain("Choose your own");
    fireEvent.click(btn);
    expect(onTertiary).toHaveBeenCalledOnce();
  });

  it("always provides the × dismiss control and fires onDismiss when clicked", () => {
    const onDismiss = vi.fn();
    render(() => (
      <SystemNudge title="Test notification" onDismiss={onDismiss} />
    ));
    const dismissBtn = screen.getByTestId("system-nudge-dismiss");
    expect(dismissBtn).toBeTruthy();
    fireEvent.click(dismissBtn);
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it("disables an action when its disabled flag is set", () => {
    const onPrimary = vi.fn();
    render(() => (
      <SystemNudge
        title="Busy"
        primaryAction={{ label: "Setting…", onClick: onPrimary, disabled: true }}
        onDismiss={vi.fn()}
      />
    ));
    const btn = screen.getByTestId("system-nudge-primary") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });
});
