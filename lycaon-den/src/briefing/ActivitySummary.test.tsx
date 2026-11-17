import { fireEvent, render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActivitySummary, type ActivitySummaryState } from "./ActivitySummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { SUMMARY_FADE_CLASS, clearSummaryFades } from "./summary-fade.ts";

afterEach(() => clearSummaryFades());

const region = (root: HTMLElement) => root.querySelector<HTMLElement>('[data-testid="activity-summary"]')!;

describe("ActivitySummary", () => {
  it("holds its box from a pending open through arrival, then fades the text in once", () => {
    const [state, setState] = createSignal<ActivitySummaryState>({ kind: "pending" });
    const view = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(view.container).hasAttribute("data-held")).toBe(true);
    expect(region(view.container).textContent).toContain(briefingCopy.waiting);
    setState({ kind: "ready", purpose: "Checks the `greet` test.", change: "All passed.", recordId: "r1", changePending: false });
    const box = region(view.container);
    expect(box.hasAttribute("data-held")).toBe(true);
    expect(box.textContent).toContain("Checks the greet test. All passed.");
    expect(box.querySelector(".den-briefing-summary__code")?.textContent).toBe("greet");
    expect(box.querySelectorAll(`.${SUMMARY_FADE_CLASS}`)).toHaveLength(1);
    view.unmount();

    const reopened = render(() => <ActivitySummary state={state()} running={false} onRetry={() => {}} />);
    expect(region(reopened.container).hasAttribute("data-held")).toBe(false);
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
});
