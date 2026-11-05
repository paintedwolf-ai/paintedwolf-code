import { createSignal } from "solid-js";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { GrantMenu } from "./GrantMenu.tsx";

type MenuProps = Parameters<typeof GrantMenu>[0];

function mountMenu(props: { face: MenuProps["face"]; items: MenuProps["items"]; note?: string }) {
  const [open, setOpen] = createSignal(false);
  const onPick = vi.fn();
  render(() => (
    <GrantMenu face={props.face} items={props.items} note={props.note} open={open()} onOpenChange={setOpen} onPick={onPick} />
  ));
  return { open, setOpen, onPick };
}

const focusedOffer = () => document.activeElement?.getAttribute("data-offer-id");

describe("GrantMenu keyboard", () => {
  it("walks the items with wraparound and returns focus to the face on Escape", async () => {
    const { open } = mountMenu({
      face: { title: "Face" },
      items: [{ id: "a", title: "A" }, { id: "b", title: "B" }, { id: "c", title: "C" }],
    });
    const face = screen.getByTestId("approval-grant-face");
    expect(face.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(face);
    expect(face.getAttribute("aria-expanded")).toBe("true");
    await waitFor(() => expect(focusedOffer()).toBe("a"));

    const steps: Array<[string, string]> = [
      ["ArrowDown", "b"], ["ArrowUp", "a"], ["ArrowUp", "c"], ["ArrowDown", "a"], ["End", "c"], ["Home", "a"],
    ];
    for (const [key, expected] of steps) {
      fireEvent.keyDown(document.activeElement!, { key });
      await waitFor(() => expect(focusedOffer()).toBe(expected));
    }

    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(open()).toBe(false);
    expect(screen.queryByTestId("approval-grant-menu")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(face));
  });

  it("closes without taking focus back when dismissed from outside", async () => {
    const { open } = mountMenu({ face: { title: "Face" }, items: [{ id: "a", title: "A" }] });
    const face = screen.getByTestId("approval-grant-face");
    fireEvent.click(face);
    await waitFor(() => expect(focusedOffer()).toBe("a"));
    fireEvent.pointerDown(document.body);
    expect(open()).toBe(false);
    expect(document.activeElement).not.toBe(face);
  });
});

describe("GrantMenu face", () => {
  it("renders nothing with neither a pickable face nor items", () => {
    mountMenu({ face: { title: "Face" }, items: [] });
    expect(screen.queryByTestId("approval-grant-face")).toBeNull();
  });

  it("offers a pickable face alone when there is nothing more to choose", () => {
    const { onPick } = mountMenu({ face: { id: "once", title: "Face" }, items: [] });
    fireEvent.click(screen.getByTestId("approval-grant-face"));
    expect(onPick).toHaveBeenCalledWith("once");
    expect(screen.queryByTestId("approval-grant-caret")).toBeNull();
  });

  it("splits picking the face from opening the other items", () => {
    const { open, onPick } = mountMenu({ face: { id: "once", title: "Face" }, items: [{ id: "chat", title: "Chat" }] });
    fireEvent.click(screen.getByTestId("approval-grant-face"));
    expect(onPick).toHaveBeenCalledWith("once");
    expect(open()).toBe(false);

    const caret = screen.getByTestId("approval-grant-caret");
    fireEvent.click(caret);
    expect(open()).toBe(true);
    expect(caret.getAttribute("aria-expanded")).toBe("true");
    fireEvent.click(screen.getByTestId("approval-grant-menu").querySelector('[data-offer-id="chat"]')!);
    expect(onPick).toHaveBeenLastCalledWith("chat");
    expect(open()).toBe(false);
  });
});

describe("GrantMenu rows", () => {
  it("heads consecutive group members once and separates each change of group", () => {
    const { setOpen } = mountMenu({
      face: { title: "Face" },
      items: [
        { id: "a", title: "A", group: "First" },
        { id: "b", title: "B", group: "First" },
        { id: "c", title: "C", group: "Second" },
        { id: "d", title: "D" },
      ],
    });
    setOpen(true);
    const list = screen.getByTestId("approval-grant-menu").querySelector(".den-approval-grant-list")!;
    const rows = [...list.children].map((row) =>
      row.getAttribute("data-offer-id") ??
      (row.getAttribute("data-testid") === "approval-grant-group" ? `group:${row.textContent}` : "sep"));
    expect(rows).toEqual(["group:First", "a", "b", "sep", "group:Second", "c", "sep", "d"]);
  });

  it("shows a held rung's note in place of its coverage and does not pick it", () => {
    const { setOpen, onPick } = mountMenu({
      face: { title: "Face" },
      items: [
        { id: "open", title: "Open", meta: "open coverage", slot: 2 },
        { id: "held", title: "Held", meta: "held coverage", note: "held note", disabled: true, slot: 3 },
      ],
      note: "menu note",
    });
    setOpen(true);
    const menu = screen.getByTestId("approval-grant-menu");
    const held = menu.querySelector<HTMLButtonElement>('[data-offer-id="held"]')!;
    expect(menu.querySelector('[data-offer-id="open"]')?.querySelector('[data-testid="approval-grant-item-meta"]')?.textContent).toBe("open coverage");
    expect(held.querySelector('[data-testid="approval-grant-item-note"]')?.textContent).toBe("held note");
    expect(held.textContent).not.toContain("held coverage");
    expect(held.disabled).toBe(true);
    fireEvent.click(held);
    expect(onPick).not.toHaveBeenCalled();
    expect(menu.textContent).toContain("menu note");
  });
});

describe("GrantMenu pick", () => {
  it("hands the pick over while the chosen item still holds focus, then closes", () => {
    const [open, setOpen] = createSignal(false);
    let focusedAtPick: Element | null = null;
    const onPick = vi.fn(() => {
      focusedAtPick = document.activeElement;
    });
    const view = render(() => (
      <GrantMenu
        face={{ title: "Other choices" }}
        items={[{ id: "chat", title: "For this chat" }]}
        open={open()}
        onOpenChange={setOpen}
        onPick={onPick}
      />
    ));
    fireEvent.click(view.getByRole("button", { name: "Other choices" }));
    const item = screen.getByTestId("approval-grant-menu").querySelector<HTMLButtonElement>("[role=menuitem]")!;
    item.focus();
    fireEvent.click(item);
    expect(onPick).toHaveBeenCalledWith("chat");
    expect(focusedAtPick).toBe(item);
    expect(open()).toBe(false);
  });
});
