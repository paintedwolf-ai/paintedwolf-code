import { cleanup, render, screen, waitFor, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { LineFactsCard, type LineFactsCardState } from "./LineFactsCard.tsx";
import type { LineFactRow } from "./line-facts.ts";

const row = (id: string): LineFactRow => ({ key: id, title: id, fact: "Edited lines 4–6 · turn 12",
  target: { kind: "chat", sessionId: id, toolCallId: `tool-${id}` } });
function fixture() {
  const anchor = document.createElement("button");
  document.body.append(anchor);
  anchor.getBoundingClientRect = () => new DOMRect(10, 20, 56, 20);
  const [state, setState] = createSignal<LineFactsCardState>({ anchor, line: 4, path: "a.go", enter: false,
    facts: { now: [], changed: [row("first"), row("second")], findings: [], count: 2 } });
  const handlers = { onActivate: vi.fn(), onKeepOpen: vi.fn(), onLeave: vi.fn(), onDismiss: vi.fn(), onReturnFocus: vi.fn() };
  render(() => <LineFactsCard state={state()} {...handlers} />);
  return { anchor, state, setState, handlers };
}
afterEach(() => { cleanup(); document.body.replaceChildren(); });
describe("line facts card", () => {
  it("opens the selected contributor at the originating line", () => {
    const { handlers } = fixture();
    fireEvent.click(screen.getByRole("button", { name: /second/ }));
    expect(handlers.onActivate).toHaveBeenCalledWith(row("second"), 4);
  });
  it("keeps focus and the surface when names refresh", async () => {
    const { anchor, setState, handlers } = fixture();
    const surface = screen.getByRole("dialog"), button = screen.getByRole("button", { name: /second/ });
    button.focus();
    setState(old => ({ ...old, facts: { ...old.facts, changed: old.facts.changed.map(row => ({ ...row, title: `${row.title} renamed` })) } }));
    await waitFor(() => expect(screen.getByText("second renamed")).toBeTruthy());
    expect(screen.getByRole("dialog")).toBe(surface);
    expect(document.activeElement).toBe(button);
    fireEvent.pointerDown(anchor);
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    fireEvent.pointerDown(document.body);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("anchors to the right, supports row navigation, and returns focus on Escape", async () => {
    const { handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    await waitFor(() => expect(dialog.style.left).toBe("70px"));
    expect(dialog.dataset.side).toBe("right");
    const first = screen.getByRole("button", { name: /first/ }), second = screen.getByRole("button", { name: /second/ });
    first.focus(); fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(second);
    fireEvent.keyDown(second, { key: "Escape" });
    expect(handlers.onReturnFocus).toHaveBeenCalledOnce();
  });
  it("keeps pointer grace separate from immediate scroll dismissal", () => {
    const { handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    fireEvent.mouseEnter(dialog); fireEvent.mouseLeave(dialog);
    expect(handlers.onKeepOpen).toHaveBeenCalled();
    expect(handlers.onLeave).toHaveBeenCalledOnce();
    fireEvent.scroll(window);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("enters an already-positioned card when the gutter requests keyboard entry", async () => {
    const { anchor, setState } = fixture();
    await waitFor(() => expect(screen.getByRole("dialog").style.left).toBe("70px"));
    anchor.focus();
    setState(state => ({ ...state, enter: true }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /first/ })));
  });
});
