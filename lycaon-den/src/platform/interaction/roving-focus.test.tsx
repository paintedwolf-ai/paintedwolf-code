import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { createRovingFocus } from "./roving-focus.ts";

type HarnessProps = {
  orientation?: "horizontal" | "vertical";
  selectionFollowsFocus?: boolean;
  onArrowDown?: (item: HTMLElement) => void;
  onActivate?: (label: string) => void;
};

function Harness(props: HarnessProps) {
  let root: HTMLDivElement | undefined;
  createRovingFocus(() => root, {
    items: ".item",
    selectionFollowsFocus: props.selectionFollowsFocus,
    keys: props.onArrowDown ? { ArrowDown: props.onArrowDown } : undefined,
  });
  return (
    <div
      ref={(element) => {
        root = element;
      }}
      role="toolbar"
      aria-label="Harness"
      aria-orientation={props.orientation}
    >
      <button class="item" type="button" onClick={() => props.onActivate?.("one")}>
        One
      </button>
      <button class="item" type="button" disabled onClick={() => props.onActivate?.("two")}>
        Two
      </button>
      <button class="item" type="button" onClick={() => props.onActivate?.("three")}>
        Three
      </button>
    </div>
  );
}

function items(): HTMLButtonElement[] {
  return [...screen.getByRole("toolbar").children] as HTMLButtonElement[];
}

describe("createRovingFocus", () => {
  it("leaves one tab stop and takes disabled items out of the tab order", async () => {
    render(() => <Harness />);
    await waitFor(() => expect(items().map((item) => item.tabIndex)).toEqual([0, -1, -1]));
  });

  it("moves focus past disabled items without activating them", () => {
    const onActivate = vi.fn();
    render(() => <Harness onActivate={onActivate} />);
    const [one, , three] = items();

    one?.focus();
    fireEvent.keyDown(one!, { key: "ArrowRight" });
    expect(document.activeElement).toBe(three);
    expect(onActivate).not.toHaveBeenCalled();
  });

  it("activates what focus lands on when selection follows focus", () => {
    const onActivate = vi.fn();
    render(() => <Harness selectionFollowsFocus onActivate={onActivate} />);
    const [one] = items();

    one?.focus();
    fireEvent.keyDown(one!, { key: "ArrowRight" });
    expect(onActivate).toHaveBeenCalledWith("three");
  });

  it("reads its axis from aria-orientation", () => {
    render(() => <Harness orientation="vertical" />);
    const [one, , three] = items();

    one?.focus();
    fireEvent.keyDown(one!, { key: "ArrowRight" });
    expect(document.activeElement).toBe(one);
    fireEvent.keyDown(one!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(three);
  });

  it("answers widget keys only where navigation does not claim them", () => {
    const onArrowDown = vi.fn();
    render(() => <Harness onArrowDown={onArrowDown} />);
    const [one] = items();

    one?.focus();
    fireEvent.keyDown(one!, { key: "ArrowDown" });
    expect(onArrowDown).toHaveBeenCalledWith(one);

    // The vertical axis spends ArrowDown on movement, so the widget never sees it.
    onArrowDown.mockClear();
    render(() => <Harness orientation="vertical" onArrowDown={onArrowDown} />);
    const vertical = [...screen.getAllByRole("toolbar")[1]!.children] as HTMLButtonElement[];
    vertical[0]?.focus();
    fireEvent.keyDown(vertical[0]!, { key: "ArrowDown" });
    expect(onArrowDown).not.toHaveBeenCalled();
  });

  it("ignores navigation keys carrying a modifier", () => {
    render(() => <Harness />);
    const [one] = items();

    one?.focus();
    fireEvent.keyDown(one!, { key: "ArrowRight", metaKey: true });
    expect(document.activeElement).toBe(one);
  });
});
