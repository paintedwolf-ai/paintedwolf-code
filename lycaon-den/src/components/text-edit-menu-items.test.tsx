import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { textEditMenuItems } from "./text-edit-menu-items.ts";
import { TextEditContextMenuHost } from "./TextEditContextMenuHost.tsx";
import { ContextMenu } from "./ContextMenu.tsx";
import { SourceUrlLink } from "./source/SourceUrlLink.tsx";
import type { EditableSnapshot } from "../platform/interaction/text-edit-context.ts";

describe("textEditMenuItems", () => {
  it("offers the system edit quartet + Add to chat for a live selection", () => {
    const items = textEditMenuItems({
      kind: "selection",
      text: "hi",
      target: null,
    });
    expect(items.map((i) => i.label)).toEqual([
      "Cut",
      "Copy",
      "Paste",
      "Select all",
      "Add to chat",
    ]);
    expect(items[0]?.disabled).toBe(true);
    expect(items[1]?.disabled).toBe(false);
    expect(items[2]?.disabled).toBe(true);
  });

  it("offers Cut/Copy/Paste/Select all for editables with disabled Cut/Copy when empty", () => {
    const snap: EditableSnapshot = {
      el: document.createElement("textarea"),
      start: 0,
      end: 0,
      range: null,
      readOnly: false,
    };
    const items = textEditMenuItems({ kind: "editable", snapshot: snap });
    expect(items.map((i) => i.label)).toEqual([
      "Cut",
      "Copy",
      "Paste",
      "Select all",
    ]);
    expect(items[0]?.disabled).toBe(true);
    expect(items[1]?.disabled).toBe(true);
    expect(items[2]?.disabled).toBe(false);
  });

  it("adds Add to chat when an editable has a selection", () => {
    const el = document.createElement("textarea");
    el.value = "hello world";
    const snap: EditableSnapshot = {
      el,
      start: 0,
      end: 5,
      range: null,
      readOnly: false,
    };
    const items = textEditMenuItems({ kind: "editable", snapshot: snap });
    expect(items.map((i) => i.label)).toContain("Add to chat");
  });

});

describe("ContextMenu disabled items", () => {
  it("does not invoke a disabled item", () => {
    const onSelect = vi.fn();
    const onDismiss = vi.fn();
    render(() => (
      <ContextMenu
        anchor={{ x: 10, y: 10 }}
        onDismiss={onDismiss}
        items={[
          {
            label: "Cut",
            testId: "item-cut",
            disabled: true,
            onSelect,
          },
        ]}
      />
    ));
    fireEvent.click(screen.getByTestId("item-cut"));
    expect(onSelect).not.toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
  });
});

describe("TextEditContextMenuHost", () => {
  afterEach(() => {
    cleanup();
    window.getSelection()?.removeAllRanges();
  });

  it.each(["pointer", "keyboard"])("opens the URL destination menu from a source link (%s)", async (input) => {
    render(() => <><SourceUrlLink url="https://example.com/report" label="Report" /><TextEditContextMenuHost /></>);
    const link = screen.getByRole("link", { name: "Report" });
    if (input === "keyboard") {
      link.focus();
      fireEvent.keyDown(link, { key: "F10", shiftKey: true });
    } else {
      fireEvent.contextMenu(link, { button: 2 });
    }
    fireEvent.click(await screen.findByTestId("open-in-menu"));
    expect(screen.getByTestId("open-in-browser")).toBeTruthy();
    expect(screen.getByTestId("link-menu-copy")).toBeTruthy();
    expect(screen.queryByTestId("text-edit-copy")).toBeNull();
  });

  it("opens the selection menu from nested chrome inside the island", async () => {
    const prose = document.createElement("p");
    prose.style.userSelect = "text";
    prose.dataset.testid = "prose-island";
    const chip = document.createElement("button");
    chip.style.userSelect = "none";
    chip.dataset.testid = "nested-chrome";
    chip.textContent = "path.ts";
    prose.append("see ", chip);
    document.body.appendChild(prose);
    const range = document.createRange();
    range.selectNodeContents(prose);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);

    const surface = vi.fn((e: Event) => {
      e.preventDefault();
      e.stopPropagation();
    });
    chip.addEventListener("contextmenu", surface);

    render(() => <TextEditContextMenuHost />);
    fireEvent.contextMenu(chip, { clientX: 20, clientY: 20, button: 2 });
    expect(await screen.findByTestId("text-edit-copy")).toBeTruthy();
    expect(screen.getByTestId("text-edit-paste")).toBeTruthy();
    expect(surface).not.toHaveBeenCalled();
    chip.removeEventListener("contextmenu", surface);
    prose.remove();
  });

  it.each(["data-den-source-path", "data-den-navigation-reference"])(
    "lets a selected source link handle its menu through %s",
    (attribute) => {
      const surface = vi.fn((event: MouseEvent) => event.preventDefault());
      const { getByTestId } = render(() => <>
        <p style={{ "user-select": "text" }}>
          <button {...{ [attribute]: "reference" }} onContextMenu={surface}>
            <code data-testid="selected-path">Taskfile.yml</code>
          </button>
        </p>
        <TextEditContextMenuHost />
      </>);
      const label = getByTestId("selected-path");
      const range = document.createRange();
      range.selectNodeContents(label);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(range);
      fireEvent.contextMenu(label, { button: 2 });
      expect(surface).toHaveBeenCalledOnce();
      expect(screen.queryByTestId("text-edit-copy")).toBeNull();
      expect(window.getSelection()?.toString()).toBe("Taskfile.yml");
    },
  );

  it("opens Cut/Copy/Paste/Select all on a textarea contextmenu", async () => {
    render(() => (
      <>
        <textarea data-testid="field" />
        <TextEditContextMenuHost />
      </>
    ));
    const field = screen.getByTestId("field") as HTMLTextAreaElement;
    field.value = "hello";
    field.focus();
    field.setSelectionRange(0, 2);
    fireEvent.contextMenu(field, { clientX: 40, clientY: 50 });
    expect(await screen.findByTestId("context-menu")).toBeTruthy();
    expect(screen.getByTestId("text-edit-cut")).toBeTruthy();
    expect(screen.getByTestId("text-edit-copy")).toBeTruthy();
    expect(screen.getByTestId("text-edit-paste")).toBeTruthy();
    expect(screen.getByTestId("text-edit-select-all")).toBeTruthy();
  });
});
