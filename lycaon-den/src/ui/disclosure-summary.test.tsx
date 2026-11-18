import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { bindDisclosureSummary } from "./disclosure-summary.ts";

afterEach(cleanup);

function Summary(props: { initiallyOpen?: boolean; onLink?: () => void; onAction?: () => void }) {
  const [open, setOpen] = createSignal(props.initiallyOpen ?? false);
  let toggle: HTMLButtonElement | undefined;
  return (
    <div data-testid="summary" ref={(element) => bindDisclosureSummary(element, { open, toggle: () => toggle })}>
      <button ref={(element) => { toggle = element; }} data-testid="toggle" aria-expanded={open()} onClick={() => setOpen(!open())}>
        <span data-testid="toggle-words">Toggle</span>
      </button>
      <span data-testid="words">Subject</span>
      <a href="#file" data-testid="link" onClick={(event) => { event.preventDefault(); props.onLink?.(); }}>file.ts</a>
      <button data-testid="action" data-disclosure-action="" onClick={() => props.onAction?.()}>
        <span data-testid="action-words">Revoke</span>
      </button>
    </div>
  );
}

const expanded = () => screen.getByTestId("toggle").getAttribute("aria-expanded");

describe("bindDisclosureSummary", () => {
  it.each(["summary", "words", "link", "toggle", "toggle-words"])("opens a closed summary from %s", (target) => {
    const onLink = vi.fn();
    render(() => <Summary onLink={onLink} />);
    fireEvent.click(screen.getByTestId(target));
    expect(expanded()).toBe("true");
    expect(onLink).not.toHaveBeenCalled();
  });

  it.each(["action", "action-words"])("leaves a declared action its own click from %s", (target) => {
    const onAction = vi.fn();
    render(() => <Summary onAction={onAction} />);
    fireEvent.click(screen.getByTestId(target));
    expect(onAction).toHaveBeenCalledOnce();
    expect(expanded()).toBe("false");
  });

  it("lets an open summary's controls act and closes it from anywhere else", () => {
    const onLink = vi.fn();
    render(() => <Summary initiallyOpen onLink={onLink} />);
    fireEvent.click(screen.getByTestId("link"));
    expect(onLink).toHaveBeenCalledOnce();
    expect(expanded()).toBe("true");
    fireEvent.click(screen.getByTestId("words"));
    expect(expanded()).toBe("false");
  });

  it("states whether the summary is closed", () => {
    render(() => <Summary />);
    const summary = screen.getByTestId("summary");
    expect(summary.getAttribute("data-disclosure-summary")).toBe("closed");
    fireEvent.click(screen.getByTestId("words"));
    expect(summary.getAttribute("data-disclosure-summary")).toBe("open");
  });
});
