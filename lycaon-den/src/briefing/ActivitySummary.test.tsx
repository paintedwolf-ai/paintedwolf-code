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

const generated = (text: string) => ({ text, source: "summarizer" as const });

const region = (root: HTMLElement) => root.querySelector<HTMLElement>('[data-testid="activity-summary"]')!;

describe("ActivitySummary", () => {
  it("marks generated sections without attributing host facts to AI", () => {
    const view = render(() => <ActivitySummary state={{ kind: "ready", purpose: generated("Runs the command."), change: { text: "Nothing ran.", source: "deterministic" }, recordId: "refused" }} running={false} onRetry={() => {}} />);
    expect(region(view.container).querySelector('[data-section="intent"] .den-briefing-mark')).toBeTruthy();
    expect(region(view.container).querySelector('[data-section="outcome"] .den-briefing-mark')).toBeNull();
    expect(region(view.container).textContent).toContain("Outcome: Nothing ran.");
  });

  it("holds its box from a pending open through arrival, then fades the text in once", () => {
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(view.container).querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(true);
    expect(region(view.container).textContent).toContain(briefingCopy.waiting);
    setState({ kind: "ready", purpose: generated("Checks the `greet` test."), change: generated("All passed."), recordId: "r1" });
    const box = region(view.container);
    expect(box.querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(true);
    expect(box.querySelector('[data-section="intent"]')?.textContent).toContain("Intent: Checks the greet test.");
    expect(box.querySelector('[data-section="outcome"]')?.textContent).toContain("Outcome: All passed.");
    expect(box.querySelector(".den-briefing-summary__code")?.textContent).toBe("greet");
    expect(box.querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(1);
    view.unmount();

    const reopened = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(reopened.container).querySelector('[data-section="intent"]')?.hasAttribute("data-held")).toBe(false);
    expect(region(reopened.container).querySelector(`.${SUMMARY_FADE_CLASS}`)).toBeNull();
  });

  it("says when the summary did not arrive and retries once per press", () => {
    const retry = vi.fn();
    const view = render(() => <ActivitySummary state={{ kind: "missing", purpose: generated("Starts the dev server.") }} running={false} onRetry={retry} />);
    const box = region(view.container);
    expect(box.textContent).toContain("Starts the dev server.");
    expect(box.textContent).toContain(briefingCopy.notArrived);
    fireEvent.click(view.getByTestId("activity-summary-retry"));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it("says a running activity's result comes later, and a pending change is still being written", () => {
    const running = render(() => <ActivitySummary state={{ kind: "ready", purpose: generated("Starts the server."), recordId: "r2" }} running onRetry={() => {}} />);
    expect(region(running.container).textContent).toContain(briefingCopy.resultPending);
    running.unmount();
    const writing = render(() => <ActivitySummary state={{ kind: "pending", purpose: generated("Runs the tests.") }} running={false} onRetry={() => {}} />);
    expect(region(writing.container).textContent).toContain(briefingCopy.waiting);
  });

  it("reveals both sections within one fade", () => {
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    const intent = () => region(view.container).querySelector('[data-section="intent"]')!;
    const outcome = () => region(view.container).querySelector('[data-section="outcome"]')!;
    expect(intent().textContent).toContain(`Intent: ${briefingCopy.waiting}`);
    expect(outcome().textContent).toContain(`Outcome: ${briefingCopy.waiting}`);
    setState({ kind: "ready", purpose: generated("Finds the entry point."), change: generated("Found `main.go`."), recordId: "partial" });
    expect(intent().textContent).toContain("Intent: Finds the entry point.");
    const fade = region(view.container).querySelector(`.${SUMMARY_FADE_CLASS}`)!;
    expect(fade.contains(intent())).toBe(true);
    expect(fade.contains(outcome())).toBe(true);
    expect(region(view.container).querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(1);
  });

  it("makes a missing section retryable without hiding the available section", () => {
    const retry = vi.fn();
    const view = render(() => <ActivitySummary state={{ kind: "ready", change: generated("The command failed."), recordId: "failed" }} running={false} onRetry={retry} />);
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
    const view = render(() => <ActivitySummary state={{ kind: "ready", purpose: generated("Reads source."), change: generated("A long outcome."), recordId: "long" }} running={false} onRetry={() => {}} />);
    expect(view.queryByRole("button", { name: "Show all intent" })).toBeNull();
    fireEvent.click(view.getByRole("button", { name: "Show all outcome" }));
    expect(region(view.container).querySelector('[data-section="outcome"] .den-activity-summary__text')?.hasAttribute("data-expanded")).toBe(true);
    expect(region(view.container).querySelector('[data-section="intent"] .den-activity-summary__text')?.hasAttribute("data-expanded")).toBe(false);
    view.unmount();
    expect(disconnect).toHaveBeenCalledTimes(2);
  });


  it("measures summary text only after it arrives", () => {
    const observe = vi.fn();
    const disconnect = vi.fn();
    vi.stubGlobal("ResizeObserver", class {
      observe = observe;
      disconnect = disconnect;
    });
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(observe).not.toHaveBeenCalled();
    setState({ kind: "ready", purpose: generated("Reads source."), change: generated("Found the entry point."), recordId: "measure" });
    expect(observe).toHaveBeenCalledTimes(2);
    setState({ kind: "pending" });
    expect(disconnect).toHaveBeenCalledTimes(2);
    view.unmount();
  });

});
