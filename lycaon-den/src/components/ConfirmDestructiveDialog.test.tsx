import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { confirmDestructive } from "../platform/interaction/confirm-dialog.ts";
import { confirmChatDelete } from "../session/confirm-chat-delete.ts";
import {
  ConfirmDestructiveDialog,
  ConfirmDestructiveHost,
} from "./ConfirmDestructiveDialog.tsx";

describe("ConfirmDestructiveDialog", () => {
  it("renders title, body, and both actions", () => {
    const { unmount } = render(() => (
      <ConfirmDestructiveDialog
        request={{
          title: "Delete chat",
          message: "Delete this chat and its full history? This cannot be undone.",
          okLabel: "Delete",
        }}
        onCancel={() => undefined}
        onConfirm={() => undefined}
      />
    ));
    expect(screen.getByTestId("confirm-destructive-dialog")).toBeTruthy();
    expect(screen.getByTestId("confirm-destructive-dialog").textContent).toContain(
      "Delete this chat and its full history?",
    );
    expect(screen.getByTestId("confirm-destructive-cancel").textContent).toBe(
      "Cancel",
    );
    expect(screen.getByTestId("confirm-destructive-ok").textContent).toBe(
      "Delete",
    );
    unmount();
    document.querySelector(".den-dialog-backdrop")?.remove();
  });

  it("cancels from the backdrop and the Cancel button", () => {
    const cancelled: boolean[] = [];
    const { unmount } = render(() => (
      <ConfirmDestructiveDialog
        request={{
          title: "Delete chat",
          message: "Gone.",
          okLabel: "Delete",
        }}
        onCancel={() => cancelled.push(true)}
        onConfirm={() => undefined}
      />
    ));
    fireEvent.click(screen.getByTestId("confirm-destructive-cancel"));
    const backdrop = document.querySelector(".den-dialog-backdrop");
    expect(backdrop).toBeTruthy();
    fireEvent.click(backdrop!);
    expect(cancelled).toEqual([true, true]);
    unmount();
    document.querySelector(".den-dialog-backdrop")?.remove();
  });
});

describe("ConfirmDestructiveHost", () => {
  afterEach(() => {
    document.querySelector(".den-dialog-backdrop")?.remove();
  });

  it("resolves Delete and Cancel from the in-app dialog", async () => {
    const { unmount } = render(() => <ConfirmDestructiveHost />);
    const pending = confirmChatDelete();
    await waitFor(() => screen.getByTestId("confirm-destructive-dialog"));
    expect(screen.getByRole("heading", { name: "Delete chat" })).toBeTruthy();
    fireEvent.click(screen.getByTestId("confirm-destructive-ok"));
    await expect(pending).resolves.toBe(true);

    const declined = confirmDestructive({
      message: "Clear the index?",
      title: "Clear index",
      okLabel: "Clear",
    });
    await waitFor(() => screen.getByTestId("confirm-destructive-ok"));
    expect(screen.getByTestId("confirm-destructive-ok").textContent).toBe(
      "Clear",
    );
    fireEvent.click(screen.getByTestId("confirm-destructive-cancel"));
    await expect(declined).resolves.toBe(false);
    unmount();
  });

  it("Escape declines without running the destructive action", async () => {
    const { unmount } = render(() => <ConfirmDestructiveHost />);
    const pending = confirmChatDelete();
    const dialog = await waitFor(() =>
      screen.getByTestId("confirm-destructive-dialog"),
    );
    fireEvent.keyDown(dialog, { key: "Escape" });
    await expect(pending).resolves.toBe(false);
    unmount();
  });
});
