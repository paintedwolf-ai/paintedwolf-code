import { render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createPresentationWaiting, PRESENTATION_LOADING_GRACE_MS } from "./presentation.ts";

describe("presentation waiting", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("keeps one grace period across continuous pending states", () => {
    const [phase, setPhase] = createSignal("catalog");
    const view = render(() => {
      const waiting = createPresentationWaiting(() => phase() !== "ready");
      return <span>{waiting() ? "Waiting" : ""}</span>;
    });
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS - 1);
    setPhase("history");
    expect(view.container.textContent).toBe("");
    vi.advanceTimersByTime(1);
    expect(view.container.textContent).toBe("Waiting");
    setPhase("ready");
    expect(view.container.textContent).toBe("");
  });

  it("cancels brief waits and disposes scheduled feedback", () => {
    const [pending, setPending] = createSignal(true);
    const view = render(() => {
      const waiting = createPresentationWaiting(pending);
      return <span>{waiting() ? "Waiting" : ""}</span>;
    });
    setPending(false);
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS);
    expect(view.container.textContent).toBe("");
    setPending(true);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
