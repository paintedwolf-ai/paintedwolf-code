import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { BrowseOverflowMenu } from "./BrowseOverflowMenu.tsx";

describe("BrowseOverflowMenu", () => {
  it("opens nested actions through the shared menu from the keyboard", async () => {
    const onSelect = vi.fn();
    render(() => <BrowseOverflowMenu items={[{
      label: "Open in", submenu: [{ label: "External editor", onSelect }],
    }]} />);
    const trigger = screen.getByRole("button", { name: "More" });
    fireEvent.keyDown(trigger, { key: "F10", shiftKey: true });
    const parent = await screen.findByRole("menuitem", { name: "Open in" });
    await waitFor(() => expect(document.activeElement).toBe(parent));
    fireEvent.keyDown(parent, { key: "ArrowRight" });
    const child = await screen.findByRole("menuitem", { name: "External editor" });
    await waitFor(() => expect(document.activeElement).toBe(child));
    fireEvent.keyDown(child, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("keeps a disabled icon trigger visible without opening its menu", () => {
    render(() => (
      <BrowseOverflowMenu
        variant="icon"
        disabled
        label="Export conversation"
        items={[{ label: "Export as Markdown", onSelect: vi.fn() }]}
      />
    ));

    const trigger = screen.getByRole("button", {
      name: "Export conversation",
    });
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("provides complete menu focus navigation and restores its trigger", async () => {
    render(() => (
      <BrowseOverflowMenu
        label="View options"
        items={[
          { label: "Unavailable", disabled: true, onSelect: vi.fn() },
          { label: "Rename", onSelect: vi.fn() },
          { label: "Duplicate", onSelect: vi.fn() },
        ]}
      />
    ));

    const trigger = screen.getByRole("button", { name: "View options" });
    fireEvent.click(trigger);
    const rename = await screen.findByRole("menuitem", { name: "Rename" });
    const duplicate = screen.getByRole("menuitem", { name: "Duplicate" });
    await waitFor(() => expect(document.activeElement).toBe(rename));

    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(document.activeElement).toBe(duplicate);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(rename);
    fireEvent.keyDown(document.activeElement!, { key: "ArrowUp" });
    expect(document.activeElement).toBe(duplicate);
    fireEvent.keyDown(document.activeElement!, { key: "Home" });
    expect(document.activeElement).toBe(rename);

    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });
});
