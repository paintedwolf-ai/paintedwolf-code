import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { bindDisclosureSummary } from "./disclosure-summary.ts";

afterEach(cleanup);

function Summary(props: { initiallyOpen?: boolean; onLink?: () => void; onAction?: () => void }) {
  const [open, setOpen] = createSignal(props.initiallyOpen ?? false);
  let toggle: HTMLButtonElement | undefined;
  return (
    <div data-testid="summary" ref={(element) => bindDisclosureSummary(element, () => toggle)}>
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
  it.each([false, true])("toggles from anywhere in the summary (open=%s)", (initiallyOpen) => {
    for (const target of ["summary", "words", "link", "toggle", "toggle-words"]) {
      const onLink = vi.fn();
      render(() => <Summary initiallyOpen={initiallyOpen} onLink={onLink} />);
      fireEvent.click(screen.getByTestId(target));
      expect(expanded(), target).toBe(String(!initiallyOpen));
      expect(onLink).not.toHaveBeenCalled();
      cleanup();
    }
  });

  it.each([false, true])("leaves a declared action its own click (open=%s)", (initiallyOpen) => {
    for (const target of ["action", "action-words"]) {
      const onAction = vi.fn();
      render(() => <Summary initiallyOpen={initiallyOpen} onAction={onAction} />);
      fireEvent.click(screen.getByTestId(target));
      expect(onAction).toHaveBeenCalledOnce();
      expect(expanded()).toBe(String(initiallyOpen));
      cleanup();
    }
  });
});
