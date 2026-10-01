import { createSignal } from "solid-js";
import { render, screen, cleanup } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { createThinkingLabelHold } from "./thinking-label-hold.ts";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

it("shows measured progress immediately and reacts to subsequent counts", () => {
  vi.useFakeTimers();
  const [visible, setVisible] = createSignal(true);
  const [immediate, setImmediate] = createSignal(false);
  const [label, setLabel] = createSignal("Waiting on provider");
  render(() => {
    const held = createThinkingLabelHold({ visible, immediate, resolveLabel: label });
    return <span data-testid="activity-label">{held()}</span>;
  });
  expect(screen.getByTestId("activity-label").textContent).toBe("Waiting on provider");
  setLabel("Searching files · 8 searched");
  setImmediate(true);
  expect(screen.getByTestId("activity-label").textContent).toBe("Searching files · 8 searched");
  setLabel("Searching files · 120 searched");
  expect(screen.getByTestId("activity-label").textContent).toBe("Searching files · 120 searched");
  setVisible(false);
  expect(screen.getByTestId("activity-label").textContent).toBe("");
});
