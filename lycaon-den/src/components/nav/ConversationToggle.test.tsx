import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { AttentionRow } from "../../api/types.ts";
import {
  ConversationCollapseButton,
  ConversationExpandButton,
} from "./ConversationToggle.tsx";

const mirrored = (el: HTMLElement) =>
  el.querySelector("svg")!.classList.contains("den-theme-icon--mirror");

function attention(cls: AttentionRow["class"], reason: AttentionRow["reason"]) {
  return { session_id: "s1", project_id: "p1", class: cls, reason } as AttentionRow;
}

describe("ConversationToggle", () => {
  it("hides toward the conversation's edge", () => {
    const onClick = vi.fn();
    const right = render(() => (
      <ConversationCollapseButton side="right" onClick={onClick} />
    ));
    const hide = screen.getByTestId("conversation-collapse-btn");
    expect(hide.getAttribute("aria-label")).toBe("Hide conversation");
    expect(mirrored(hide)).toBe(true);
    fireEvent.click(hide);
    expect(onClick).toHaveBeenCalledOnce();
    right.unmount();

    render(() => <ConversationCollapseButton side="left" onClick={vi.fn()} />);
    expect(mirrored(screen.getByTestId("conversation-collapse-btn"))).toBe(false);
  });

  it("marks a hidden conversation that waits on the person", () => {
    render(() => (
      <ConversationExpandButton
        side="right"
        attention={attention("needs_you", "checkpoint")}
        onClick={vi.fn()}
      />
    ));
    const show = screen.getByTestId("conversation-expand-btn");
    expect(show.getAttribute("aria-label")).toBe(
      "Show conversation — Waiting on approval",
    );
    expect(show.querySelector(".den-pane-toggle__dot")).toBeTruthy();
    expect(mirrored(show)).toBe(true);
  });

  it("marks a hidden conversation holding an unread result, in its own tone", () => {
    render(() => (
      <ConversationExpandButton
        side="right"
        attention={attention("finished", "turn_finished")}
        onClick={vi.fn()}
      />
    ));
    const show = screen.getByTestId("conversation-expand-btn");
    expect(show.getAttribute("aria-label")).toBe(
      "Show conversation — Finished while you were away",
    );
    // The dot class carries the attention tone.
    const dot = show.querySelector(".den-pane-toggle__dot");
    expect(dot?.getAttribute("data-attention-class")).toBe("finished");
  });

  it("distinguishes an obligation from a result by the dot's class", () => {
    render(() => (
      <ConversationExpandButton
        side="right"
        attention={attention("needs_you", "checkpoint")}
        onClick={vi.fn()}
      />
    ));
    const dot = screen
      .getByTestId("conversation-expand-btn")
      .querySelector(".den-pane-toggle__dot");
    expect(dot?.getAttribute("data-attention-class")).toBe("needs_you");
  });

  it("adds nothing for a running or failed turn", () => {
    const running = render(() => (
      <ConversationExpandButton
        side="left"
        attention={attention("running", "turn_running")}
        onClick={vi.fn()}
      />
    ));
    let show = screen.getByTestId("conversation-expand-btn");
    expect(show.getAttribute("aria-label")).toBe("Show conversation");
    expect(show.querySelector(".den-pane-toggle__dot")).toBeNull();
    running.unmount();

    render(() => (
      <ConversationExpandButton
        side="left"
        attention={attention("error", "turn_error")}
        onClick={vi.fn()}
      />
    ));
    show = screen.getByTestId("conversation-expand-btn");
    expect(show.querySelector(".den-pane-toggle__dot")).toBeNull();
  });

  it("is a plain show control while the conversation is idle", () => {
    const onClick = vi.fn();
    render(() => <ConversationExpandButton side="left" onClick={onClick} />);
    const show = screen.getByTestId("conversation-expand-btn");
    expect(show.getAttribute("aria-label")).toBe("Show conversation");
    expect(show.querySelector(".den-pane-toggle__dot")).toBeNull();
    expect(mirrored(show)).toBe(false);
    fireEvent.click(show);
    expect(onClick).toHaveBeenCalledOnce();
  });
});
