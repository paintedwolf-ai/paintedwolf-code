import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RemovedLinesPopover, type RemovedLinesPreview } from "./RemovedLinesPopover.tsx";

function row(text: string): HTMLElement {
  const el = document.createElement("div");
  el.className = "cm-deletedLine";
  el.textContent = text;
  return el;
}

function preview(over: Partial<RemovedLinesPreview> = {}): RemovedLinesPreview {
  return {
    anchor: new DOMRect(10, 10, 16, 14),
    fromLine: 4,
    toLine: 4,
    rows: [row("gone")],
    themeClasses: "",
    font: {},
    ...over,
  };
}

function handlers() {
  return {
    onShowInPlace: vi.fn(),
    onReject: vi.fn(),
    onKeepOpen: vi.fn(),
    onLeave: vi.fn(),
    onDismiss: vi.fn(),
  };
}

describe("RemovedLinesPopover", () => {
  afterEach(cleanup);

  it("names the removed line, where it was, and its text", () => {
    render(() => <RemovedLinesPopover preview={preview()} canReject {...handlers()} />);
    const card = screen.getByTestId("files-removed-lines");
    expect(card.textContent).toContain("1 line removed");
    expect(card.textContent).toContain("was line 4");
    expect(card.textContent).toContain("gone");
  });

  it("counts a run of removed lines", () => {
    render(() => (
      <RemovedLinesPopover
        preview={preview({ toLine: 6, rows: [row("a"), row("b"), row("c")] })}
        canReject
        {...handlers()}
      />
    ));
    const card = screen.getByTestId("files-removed-lines");
    expect(card.textContent).toContain("3 lines removed");
    expect(card.textContent).toContain("was lines 4–6");
  });

  it("shows the lines in place and rejects the change", () => {
    const on = handlers();
    render(() => <RemovedLinesPopover preview={preview()} canReject {...on} />);
    fireEvent.click(screen.getByTestId("files-removed-lines-show"));
    fireEvent.click(screen.getByTestId("files-removed-lines-reject"));
    expect(on.onShowInPlace).toHaveBeenCalledTimes(1);
    expect(on.onReject).toHaveBeenCalledTimes(1);
  });

  it("holds Reject while typing is unsaved", () => {
    render(() => <RemovedLinesPopover preview={preview()} canReject={false} {...handlers()} />);
    const reject = screen.getByTestId("files-removed-lines-reject") as HTMLButtonElement;
    expect(reject.disabled).toBe(true);
    expect(reject.dataset.tip).toBe("Save first. Reject writes against the file on disk.");
  });

  it("closes when the pointer leaves, unless a selection is in the card", () => {
    const on = handlers();
    render(() => <RemovedLinesPopover preview={preview()} canReject {...on} />);
    const card = screen.getByTestId("files-removed-lines");
    fireEvent.mouseLeave(card);
    expect(on.onLeave).toHaveBeenCalledTimes(1);

    document.getSelection()!.selectAllChildren(card.querySelector(".cm-deletedLine")!);
    fireEvent.mouseLeave(card);
    expect(on.onLeave).toHaveBeenCalledTimes(1);
    document.getSelection()!.removeAllRanges();
  });

  it("stays open while a drag that started in the card is held", () => {
    const on = handlers();
    render(() => <RemovedLinesPopover preview={preview()} canReject {...on} />);
    const card = screen.getByTestId("files-removed-lines");
    fireEvent.mouseDown(card);
    fireEvent.mouseLeave(card);
    expect(on.onLeave).not.toHaveBeenCalled();

    document.dispatchEvent(new Event("pointerup", { bubbles: true }));
    fireEvent.mouseLeave(card);
    expect(on.onLeave).toHaveBeenCalledTimes(1);
  });

  it("renders nothing without a preview", () => {
    render(() => <RemovedLinesPopover preview={null} canReject {...handlers()} />);
    expect(screen.queryByTestId("files-removed-lines")).toBeNull();
  });
});
