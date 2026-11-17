import { fireEvent, render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActivitySummary, type ActivitySummaryState } from "./ActivitySummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { SUMMARY_FADE_CLASS, clearSummaryFades } from "./summary-fade.ts";

afterEach(() => {
  clearSummaryFades();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const region = (root: HTMLElement) => root.querySelector<HTMLElement>('[data-testid="activity-summary"]')!;

describe("ActivitySummary", () => {
  it("holds its box from a pending open through arrival, then fades the text in once", () => {
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(view.container).querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(true);
    expect(region(view.container).textContent).toContain(briefingCopy.waiting);
    setState({ kind: "ready", purpose: "Checks the `greet` test.", change: "All passed.", recordId: "r1", changePending: false });
    const box = region(view.container);
    expect(box.querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(true);
    expect(box.querySelector('[data-section="intent"]')?.textContent).toContain("Intent: Checks the greet test.");
    expect(box.querySelector('[data-section="outcome"]')?.textContent).toContain("Outcome: All passed.");
    expect(box.querySelector(".den-briefing-summary__code")?.textContent).toBe("greet");
    expect(box.querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(2);
    view.unmount();

    const reopened = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(reopened.container).querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(false);
    expect(region(reopened.container).querySelector(`.${SUMMARY_FADE_CLASS}`)).toBeNull();
  });

  it("says when the summary did not arrive and retries once per press", () => {
    const retry = vi.fn();
    const view = render(() => <ActivitySummary state={{ kind: "missing", purpose: "Starts the dev server." }} running={false} onRetry={retry} />);
    const box = region(view.container);
    expect(box.textContent).toContain("Starts the dev server.");
    expect(box.textContent).toContain(briefingCopy.notArrived);
    fireEvent.click(view.getByTestId("activity-summary-retry"));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("says a running activity's result comes later, and a pending change is still being written", () => {
    const running = render(() => <ActivitySummary state={{ kind: "ready", purpose: "Starts the server.", recordId: "r2", changePending: false }} running onRetry={() => {}} />);
    expect(region(running.container).textContent).toContain(briefingCopy.resultPending);
    running.unmount();
    const writing = render(() => <ActivitySummary state={{ kind: "ready", purpose: "Runs the tests.", recordId: "r3", changePending: true }} running={false} onRetry={() => {}} />);
    expect(region(writing.container).textContent).toContain(briefingCopy.waiting);
  });

  it("keeps both labels visible while each section arrives independently", () => {
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    const intent = () => region(view.container).querySelector('[data-section="intent"]')!;
    const outcome = () => region(view.container).querySelector('[data-section="outcome"]')!;
    expect(intent().textContent).toContain(`Intent: ${briefingCopy.waiting}`);
    expect(outcome().textContent).toContain(`Outcome: ${briefingCopy.waiting}`);
    setState({ kind: "ready", change: "Found `main.go`.", recordId: "partial", changePending: false, purposePending: true });
    expect(intent().textContent).toContain(briefingCopy.waiting);
    expect(outcome().textContent).toContain("Outcome: Found main.go.");
    expect(view.queryByTestId("activity-summary-retry")).toBeNull();
    setState({ kind: "ready", purpose: "Finds the entry point.", change: "Found `main.go`.", recordId: "partial", changePending: false });
    expect(intent().textContent).toContain("Intent: Finds the entry point.");
    expect(outcome().querySelector(`.${SUMMARY_FADE_CLASS}`)).not.toBeNull();
  });

  it("makes a missing section retryable without hiding the available section", () => {
    const retry = vi.fn();
    const view = render(() => <ActivitySummary state={{ kind: "ready", change: "The command failed.", recordId: "failed", changePending: false }} running={false} onRetry={retry} />);
    expect(view.getByRole("button", { name: "Retry intent summary" })).toBeTruthy();
    expect(region(view.container).textContent).toContain("Outcome: The command failed.");
    fireEvent.click(view.getByRole("button", { name: "Retry intent summary" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("expands only the clipped section and disconnects its observer", () => {
    const disconnect = vi.fn();
    vi.stubGlobal("ResizeObserver", class {
      observe = vi.fn();
      disconnect = disconnect;
    });
    vi.spyOn(window, "getComputedStyle").mockReturnValue({
      getPropertyValue: () => "3", lineHeight: "20px",
    } as unknown as CSSStyleDeclaration);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      return { height: this.closest('[data-section="outcome"]') ? 100 : 20 } as DOMRect;
    });
    const view = render(() => <ActivitySummary state={{ kind: "ready", purpose: "Reads source.", change: "A long outcome.", recordId: "long", changePending: false }} running={false} onRetry={() => {}} />);
    expect(view.queryByRole("button", { name: "Show all intent" })).toBeNull();
    fireEvent.click(view.getByRole("button", { name: "Show all outcome" }));
    expect(region(view.container).querySelector('[data-section="outcome"] .den-activity-summary__text')?.hasAttribute("data-expanded")).toBe(true);
    expect(region(view.container).querySelector('[data-section="intent"] .den-activity-summary__text')?.hasAttribute("data-expanded")).toBe(false);
    view.unmount();
    expect(disconnect).toHaveBeenCalledTimes(2);
  });

});
