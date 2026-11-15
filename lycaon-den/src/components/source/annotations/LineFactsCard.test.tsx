import { cleanup, render, screen, waitFor, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { LineFactsCard, type LineFactsCardState } from "./LineFactsCard.tsx";
import type { LineFactRow } from "./line-facts.ts";

const row = (id: string, extra: Partial<LineFactRow> = {}): LineFactRow => ({
  key: id,
  title: id,
  fact: "Edited lines 4–6 · turn 12",
  target: { kind: "chat", sessionId: id, toolCallId: `tool-${id}` },
  ...extra,
});
function fixture(rows: LineFactRow[] = [row("first"), row("second")]) {
  const editor = document.createElement("div");
  editor.className = "cm-editor";
  const anchor = document.createElement("button");
  const code = document.createElement("div");
  code.tabIndex = 0;
  editor.append(anchor, code);
  document.body.append(editor);
  anchor.getBoundingClientRect = () => new DOMRect(10, 20, 56, 20);
  const [state, setState] = createSignal<LineFactsCardState>({ anchor, line: 4, path: "a.go", enter: false,
    facts: { now: [], changed: rows, findings: [], count: rows.length } });
  const handlers = { onActivate: vi.fn(), onDismiss: vi.fn(), onReturnFocus: vi.fn() };
  render(() => <LineFactsCard state={state()} {...handlers} />);
  return { editor, anchor, code, state, setState, handlers };
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
    setState(old => ({ ...old, facts: { ...old.facts, changed: old.facts.changed.map(item => ({ ...item, title: `${item.title} renamed` })) } }));
    await waitFor(() => expect(screen.getByText("second renamed")).toBeTruthy());
    expect(screen.getByRole("dialog")).toBe(surface);
    expect(document.activeElement).toBe(button);
    fireEvent.pointerDown(anchor);
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    fireEvent.pointerDown(document.body);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("anchors to the right, supports row navigation, and returns focus on Escape from inside", async () => {
    const { handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    await waitFor(() => expect(dialog.style.left).toBe("70px"));
    expect(dialog.dataset.side).toBe("right");
    const first = screen.getByRole("button", { name: /first/ }), second = screen.getByRole("button", { name: /second/ });
    first.focus(); fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(second);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(handlers.onReturnFocus).toHaveBeenCalledOnce();
    expect(handlers.onDismiss).not.toHaveBeenCalled();
  });
  it("dismisses on Escape before editor keymaps see the press and keeps focus outside the card", () => {
    const { code, handlers } = fixture();
    const editorKeyDown = vi.fn();
    code.addEventListener("keydown", editorKeyDown);
    code.focus();
    expect(document.activeElement).toBe(code);
    fireEvent.keyDown(code, { key: "Escape" });
    expect(editorKeyDown).not.toHaveBeenCalled();
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
    expect(handlers.onReturnFocus).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(code);
  });
  it("ignores pointer entry and exit, dismisses on scroll, and dismisses when focus leaves editor and card", () => {
    const { code, handlers } = fixture();
    const dialog = screen.getByRole("dialog");
    const first = screen.getByRole("button", { name: /first/ });
    fireEvent.mouseEnter(dialog);
    fireEvent.mouseLeave(dialog);
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    expect(handlers.onReturnFocus).not.toHaveBeenCalled();
    first.focus();
    fireEvent.focusOut(first, { relatedTarget: code });
    expect(handlers.onDismiss).not.toHaveBeenCalled();
    const outside = document.body.appendChild(document.createElement("button"));
    fireEvent.focusOut(first, { relatedTarget: outside });
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
    handlers.onDismiss.mockClear();
    fireEvent.scroll(window);
    expect(handlers.onDismiss).toHaveBeenCalledOnce();
  });
  it("renders fact, relative time, and tool in order", () => {
    const full = row("with-time", { when: "17m ago", tool: "edit" });
    const plain = row("without-time");
    fixture([full, plain]);
    const facts = [...document.querySelectorAll<HTMLElement>(".files-line-facts__fact")].map(el => el.textContent);
    expect(facts).toEqual([
      [full.fact, full.when, full.tool].join(" · "),
      plain.fact,
    ]);
  });
  it("enters an already-positioned card when the gutter requests keyboard entry", async () => {
    const { anchor, setState } = fixture();
    await waitFor(() => expect(screen.getByRole("dialog").style.left).toBe("70px"));
    anchor.focus();
    setState(state => ({ ...state, enter: true }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /first/ })));
  });
  it("mounts the change briefing slot when projectId and effectId are present", () => {
    const { setState } = fixture();
    setState(old => ({ ...old, projectId: "proj-1", facts: { ...old.facts, changed: [row("first", { effectId: "eff-1" })] } }));
    expect(screen.getByRole("region", { name: "Changed" })).toBeTruthy();
  });
});
