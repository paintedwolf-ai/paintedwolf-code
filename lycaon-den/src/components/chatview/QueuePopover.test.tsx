import { describe, expect, it, vi } from "vitest";
import { at } from "../../test/at.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { QueuePopover } from "./QueuePopover.tsx";
import type { QueueItem } from "../../api/types.ts";
import { createSignal } from "solid-js";
import type { PendingSend } from "../../chat/send/pending-sends.ts";

function handlers() {
  return {
    sending: false,
    arranging: false,
    onBeginArrange: vi.fn(),
    onEndArrange: vi.fn(),
    onSetPaused: vi.fn(),
    onFireNow: vi.fn(),
    onRemove: vi.fn(),
    onUpdate: vi.fn(),
    onReorder: vi.fn(),
    onLink: vi.fn(),
    onUnlink: vi.fn(),
    onSend: vi.fn(),
    onCancelSend: vi.fn(),
    sendWaitingOnApproval: false,
  };
}

const item = (id: string, text: string, group_id?: string): QueueItem => ({
  submitted_by: "00000000-0000-4000-8000-000000000002", id,
  text,
  group_id,
  created_at: "2026-06-17T00:00:00Z",
});

describe("QueuePopover", () => {
  it("shows pending admission immediately and hands off to the host queue without duplicates", () => {
    const [items, setItems] = createSignal<QueueItem[]>([]);
    const [pending, setPending] = createSignal<PendingSend[]>([{
      kind: "queued_prompt", operationId: "next", text: "Next prompt", state: "sending", createdAt: 1,
    }]);
    const h = handlers();
    render(() => <QueuePopover items={items()} pendingSends={pending()} running paused={false} {...h} />);
    expect(screen.getByTestId("queue-pending-item").textContent).toContain("Next prompt");
    expect(screen.queryByTestId("queue-remove")).toBeNull();
    expect(screen.getByTestId("queue-send").hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByTestId("queue-send"));
    expect(h.onSend).not.toHaveBeenCalled();
    setItems([item("next", "Next prompt")]);
    expect(screen.queryByTestId("queue-pending-item")).toBeNull();
    expect(screen.getAllByText("Next prompt")).toHaveLength(1);
    expect(screen.getByTestId("queue-pill").textContent).toContain("1");
    setPending([]);
    expect(screen.getByTestId("queue-item").textContent).toContain("Next prompt");
    expect(screen.getByTestId("queue-send").hasAttribute("disabled")).toBe(false);
  });

  it.each([false, true])("names the edited queue position when paused is %s", (paused) => {
    render(() => (
      <QueuePopover items={[item("a", "first"), item("b", "second")]} running paused={paused} {...handlers()} />
    ));
    fireEvent.click(at(screen.getAllByRole("button", { name: "Edit message" }), 1));
    expect((screen.getByRole("textbox", { name: "Queued message 2" }) as HTMLTextAreaElement).value).toBe("second");
  });

  it("renders nothing when the queue is empty", () => {
    render(() => (
      <QueuePopover items={[]} running={false} paused={false} {...handlers()} />
    ));
    expect(screen.queryByTestId("queue-popover")).toBeNull();
  });

  it("auto-opens the card with items and fires fire-now / remove", () => {
    const h = handlers();
    render(() => (
      <QueuePopover
        items={[item("a", "first"), item("b", "second")]}
        running={false}
        paused={false}
        {...h}
      />
    ));
    expect(screen.getByTestId("queue-popover-card")).toBeTruthy();
    expect(screen.getByTestId("queue-pill").textContent).toMatch(/2/);
    // The front item is already next.
    expect(screen.getAllByTestId("queue-fire-now")).toHaveLength(1);
    fireEvent.click(screen.getByTestId("queue-fire-now"));
    expect(h.onFireNow).toHaveBeenCalledWith("b");
    fireEvent.click(at(screen.getAllByTestId("queue-remove"), 0));
    expect(h.onRemove).toHaveBeenCalledWith("a");
  });

  it("keeps keyboard focus on a surviving control after collapse", async () => {
    render(() => <QueuePopover items={[item("a", "first")]} running={false} paused={false} {...handlers()} />);
    const collapse = screen.getByTestId("queue-collapse");
    collapse.focus();
    fireEvent.click(collapse);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByTestId("queue-pill")));
  });

  it("collapses to the pill and reopens on click", () => {
    render(() => (
      <QueuePopover
        items={[item("a", "x")]}
        running={false}
        paused={false}
        {...handlers()}
      />
    ));
    fireEvent.click(screen.getByTestId("queue-collapse"));
    expect(screen.queryByTestId("queue-popover-card")).toBeNull();
    fireEvent.click(screen.getByTestId("queue-pill"));
    expect(screen.getByTestId("queue-popover-card")).toBeTruthy();
  });

  it("sends the reserved head group without presenting an interruption", () => {
    const h = handlers();
    const { unmount } = render(() => (
      <QueuePopover items={[item("a", "x")]} running paused={false} {...h} />
    ));
    expect(screen.getByTestId("queue-send").textContent).toBe("Send");
    fireEvent.click(screen.getByTestId("queue-send"));
    expect(h.onSend).toHaveBeenCalledOnce();
    unmount();
    render(() => (
      <QueuePopover
        items={[item("a", "x")]}
        running={false}
        paused={false}
        {...handlers()}
      />
    ));
    expect(screen.getByTestId("queue-send").textContent).toBe("Send");
  });

  it("locks queue editing while the head group is being sent", () => {
    render(() => (
      <QueuePopover
        items={[item("a", "first"), item("b", "second")]}
        running
        paused={false}
        {...handlers()}
        sending
      />
    ));
    expect(screen.getByTestId("queue-status").textContent).toBe("Sending");
    // An unclaimed reservation can still be canceled.
    expect(screen.queryByTestId("queue-send")).toBeNull();
    expect(screen.getByTestId("queue-cancel-send").textContent).toBe("Cancel send");
    for (const button of screen.getAllByTestId("queue-edit")) {
      expect((button as HTMLButtonElement).disabled).toBe(true);
    }
    for (const button of screen.getAllByTestId("queue-grip")) {
      expect((button as HTMLButtonElement).disabled).toBe(true);
    }
    expect(screen.getByTestId("queue-pill").textContent).toContain("sending");
  });

  it("waits for an active edit or drag to finish before sending", () => {
    render(() => (
      <QueuePopover
        items={[item("a", "first")]}
        running
        paused={false}
        {...handlers()}
        arranging
      />
    ));
    expect((screen.getByTestId("queue-send") as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows queue status: paused beats running, arranging suppresses paused", () => {
    const { unmount } = render(() => (
      <QueuePopover items={[item("a", "x")]} running paused {...handlers()} />
    ));
    expect(screen.getByTestId("queue-status").textContent).toBe("Paused");
    unmount();
    const { unmount: unmount2 } = render(() => (
      <QueuePopover
        items={[item("a", "x")]}
        running
        paused
        {...handlers()}
        arranging
      />
    ));
    expect(screen.getByTestId("queue-status").textContent).toBe(
      "Sends at turn end",
    );
    unmount2();
    render(() => (
      <QueuePopover
        items={[item("a", "x")]}
        running={false}
        paused={false}
        {...handlers()}
      />
    ));
    expect(screen.getByTestId("queue-status").textContent).toBe("Sends next");
  });

  it("toggles pause from the header control", () => {
    const h = handlers();
    const { unmount } = render(() => (
      <QueuePopover items={[item("a", "x")]} running={false} paused={false} {...h} />
    ));
    fireEvent.click(screen.getByTestId("queue-pause"));
    expect(h.onSetPaused).toHaveBeenCalledWith(true);
    unmount();
    const h2 = handlers();
    render(() => (
      <QueuePopover items={[item("a", "x")]} running={false} paused {...h2} />
    ));
    fireEvent.click(screen.getByTestId("queue-pause"));
    expect(h2.onSetPaused).toHaveBeenCalledWith(false);
  });

  it("links adjacent unlinked items and unlinks linked ones", () => {
    const h = handlers();
    const { unmount } = render(() => (
      <QueuePopover
        items={[item("a", "x"), item("b", "y")]}
        running={false}
        paused={false}
        {...h}
      />
    ));
    const toggle = screen.getByTestId("queue-link-toggle");
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(toggle);
    expect(h.onLink).toHaveBeenCalledWith(["a", "b"]);
    unmount();

    const h2 = handlers();
    render(() => (
      <QueuePopover
        items={[item("a", "x", "g1"), item("b", "y", "g1")]}
        running={false}
        paused={false}
        {...h2}
      />
    ));
    const linkedToggle = screen.getByTestId("queue-link-toggle");
    expect(linkedToggle.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(linkedToggle);
    expect(h2.onUnlink).toHaveBeenCalledWith(["b"]);
  });

  it("edits a message inline: Enter saves, Escape cancels, empty removes", () => {
    const h = handlers();
    const { unmount } = render(() => (
      <QueuePopover items={[item("a", "before")]} running={false} paused={false} {...h} />
    ));
    fireEvent.click(screen.getByTestId("queue-edit"));
    expect(h.onBeginArrange).toHaveBeenCalledOnce();
    const input = screen.getByRole("textbox", { name: "Queued message 1" }) as HTMLTextAreaElement;
    input.value = "after";
    fireEvent.keyDown(input, { key: "Enter" });
    expect(h.onUpdate).toHaveBeenCalledWith("a", "after");
    expect(h.onEndArrange).toHaveBeenCalledOnce();
    unmount();

    const h2 = handlers();
    const { unmount: unmount2 } = render(() => (
      <QueuePopover items={[item("a", "before")]} running={false} paused={false} {...h2} />
    ));
    fireEvent.click(screen.getByTestId("queue-edit"));
    const input2 = screen.getByTestId("queue-edit-input") as HTMLTextAreaElement;
    input2.value = "changed";
    fireEvent.keyDown(input2, { key: "Escape" });
    expect(h2.onUpdate).not.toHaveBeenCalled();
    expect(h2.onEndArrange).toHaveBeenCalledOnce();
    unmount2();

    const h3 = handlers();
    render(() => (
      <QueuePopover items={[item("a", "before")]} running={false} paused={false} {...h3} />
    ));
    fireEvent.click(screen.getByTestId("queue-edit"));
    const input3 = screen.getByTestId("queue-edit-input") as HTMLTextAreaElement;
    input3.value = "   ";
    fireEvent.keyDown(input3, { key: "Enter" });
    expect(h3.onRemove).toHaveBeenCalledWith("a");
    expect(h3.onUpdate).not.toHaveBeenCalled();
  });

  it("reorders with Arrow keys on the grip", () => {
    const h = handlers();
    render(() => (
      <QueuePopover
        items={[item("a", "x"), item("b", "y"), item("c", "z")]}
        running={false}
        paused={false}
        {...h}
      />
    ));
    const grips = screen.getAllByTestId("queue-grip");
    fireEvent.keyDown(at(grips, 1), { key: "ArrowUp" });
    expect(h.onReorder).toHaveBeenCalledWith(["b", "a", "c"]);
    fireEvent.keyDown(at(grips, 1), { key: "ArrowDown" });
    expect(h.onReorder).toHaveBeenLastCalledWith(["a", "c", "b"]);
    // Ends of the list are hard stops, not wraparounds.
    h.onReorder.mockClear();
    fireEvent.keyDown(at(grips, 0), { key: "ArrowUp" });
    expect(h.onReorder).not.toHaveBeenCalled();
  });

  it("shows the paused marker on the collapsed pill", () => {
    render(() => (
      <QueuePopover items={[item("a", "x")]} running={false} paused {...handlers()} />
    ));
    fireEvent.click(screen.getByTestId("queue-collapse"));
    expect(screen.getByTestId("queue-pill").querySelector(".queue-pill__paused")).toBeTruthy();
  });

  it("offers to take a reservation back and says what it is waiting on", () => {
    const h = handlers();
    render(() => (
      <QueuePopover
        items={[item("a", "first")]}
        running
        paused={false}
        {...h}
        sending
        sendWaitingOnApproval
      />
    ));
    // Pending approval holds the queued send.
    expect(screen.getByTestId("queue-status").textContent).toBe("Waiting on your approval");
    fireEvent.click(screen.getByTestId("queue-cancel-send"));
    expect(h.onCancelSend).toHaveBeenCalled();
  });

  it("names the reorder control for what it does", () => {
    render(() => (
      <QueuePopover
        items={[item("a", "first"), item("b", "second")]}
        running={false}
        paused={false}
        {...handlers()}
      />
    ));
    // This action prioritizes the message without sending it.
    expect(screen.getByTestId("queue-fire-now").getAttribute("aria-label")).toBe(
      "Move to the front",
    );
  });
});
