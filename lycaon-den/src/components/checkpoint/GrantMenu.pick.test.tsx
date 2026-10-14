import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { GrantMenu } from "./ApprovalShell.tsx";

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
