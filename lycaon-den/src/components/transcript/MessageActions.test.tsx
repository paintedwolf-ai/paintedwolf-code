import { render, fireEvent, screen, cleanup, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { RecoveryTarget } from "../../chat/recovery/session-recovery.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MessageActions } from "./MessageActions.tsx";
import { SessionRecoverDialog } from "./SessionRecoverDialog.tsx";

afterEach(cleanup);

describe("MessageActions", () => {
  it("renders one icon per provided handler, labelled for assistive tech", () => {
    render(() => (
      <MessageActions onCopy={() => {}} onEdit={() => {}} onRewind={() => {}} />
    ));
    for (const [testid, label] of [
      ["bubble-copy", "Copy"],
      ["user-bubble-edit", "Edit from here"],
      ["user-bubble-rewind", "Rewind to here"],
    ] as const) {
      const btn = screen.getByTestId(testid);
      expect(btn.getAttribute("aria-label")).toBe(label);
      if (testid === "bubble-copy") {
        expect(btn.hasAttribute("data-tip")).toBe(false);
      } else {
        expect(btn.getAttribute("data-tip")).toBe(label);
        expect(btn.getAttribute("data-tip-pos")).toBe("below");
      }
    }
  });

  it("caps recovery chrome at three actions", () => {
    render(() => (
      <MessageActions onCopy={() => {}} onEdit={() => {}} onRewind={() => {}} />
    ));
    expect(
      screen.getByTestId("message-actions").querySelectorAll("button"),
    ).toHaveLength(3);
    expect(screen.queryByTestId("user-bubble-resend")).toBeNull();
  });

  it("fires each action from its own button", () => {
    const onCopy = vi.fn();
    const onEdit = vi.fn();
    const onRewind = vi.fn();
    render(() => (
      <MessageActions onCopy={onCopy} onEdit={onEdit} onRewind={onRewind} />
    ));
    fireEvent.click(screen.getByTestId("bubble-copy"));
    fireEvent.click(screen.getByTestId("user-bubble-edit"));
    fireEvent.click(screen.getByTestId("user-bubble-rewind"));
    expect(onCopy).toHaveBeenCalledTimes(1);
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onRewind).toHaveBeenCalledTimes(1);
  });

  it("offers Copy alone on an assistant row", () => {
    render(() => <MessageActions onCopy={() => {}} />);
    expect(screen.getByTestId("bubble-copy")).toBeTruthy();
    expect(screen.queryByTestId("user-bubble-rewind")).toBeNull();
    expect(screen.queryByTestId("user-bubble-edit")).toBeNull();
  });

  it("walks the toolbar with arrow keys, wrapping at the ends", () => {
    render(() => (
      <MessageActions onCopy={() => {}} onEdit={() => {}} onRewind={() => {}} />
    ));
    const copy = screen.getByTestId("bubble-copy");
    const edit = screen.getByTestId("user-bubble-edit");
    const rewind = screen.getByTestId("user-bubble-rewind");
    copy.focus();
    fireEvent.keyDown(copy, { key: "ArrowRight" });
    expect(document.activeElement).toBe(edit);
    fireEvent.keyDown(edit, { key: "ArrowRight" });
    expect(document.activeElement).toBe(rewind);
    fireEvent.keyDown(rewind, { key: "ArrowRight" });
    expect(document.activeElement).toBe(copy);
    fireEvent.keyDown(copy, { key: "ArrowLeft" });
    expect(document.activeElement).toBe(rewind);
  });

  it("flashes Copied on the copy button, then reverts", () => {
    vi.useFakeTimers();
    try {
      render(() => <MessageActions onCopy={() => {}} />);
      const btn = screen.getByTestId("bubble-copy");
      fireEvent.click(btn);
      expect(btn.getAttribute("aria-label")).toBe("Copied");
      vi.advanceTimersByTime(1500);
      expect(btn.getAttribute("aria-label")).toBe("Copy");
    } finally {
      vi.useRealTimers();
    }
  });

  it("leaves every action available unless recovery is held", () => {
    render(() => (
      <MessageActions onCopy={() => {}} onEdit={() => {}} onRewind={() => {}} />
    ));
    for (const testid of ["bubble-copy", "user-bubble-edit", "user-bubble-rewind"]) {
      const button = screen.getByTestId(testid) as HTMLButtonElement;
      expect(button.disabled).toBe(false);
      expect(button.hasAttribute("aria-disabled")).toBe(false);
    }
  });

  it("holds edit and rewind in place during a turn while Copy keeps working", () => {
    const onCopy = vi.fn();
    const onEdit = vi.fn();
    const onRewind = vi.fn();
    const [held, setHeld] = createSignal(true);
    render(() => (
      <MessageActions
        onCopy={onCopy}
        onEdit={onEdit}
        onRewind={onRewind}
        recoveryHeld={held()}
      />
    ));
    const toolbar = screen.getByTestId("message-actions");
    const edit = screen.getByTestId("user-bubble-edit");
    const rewind = screen.getByTestId("user-bubble-rewind");
    // Held actions stay mounted, so the row keeps one height through the turn.
    expect(toolbar.querySelectorAll("button")).toHaveLength(3);
    expect(edit.getAttribute("aria-disabled")).toBe("true");
    expect(rewind.getAttribute("aria-disabled")).toBe("true");
    expect(edit.getAttribute("aria-label")).toBe("Edit from here");
    expect(edit.getAttribute("data-tip")).toBe("Edit after this turn finishes");
    expect(rewind.getAttribute("data-tip")).toBe("Rewind after this turn finishes");

    fireEvent.click(edit);
    fireEvent.click(rewind);
    fireEvent.click(screen.getByTestId("bubble-copy"));
    expect(onEdit).not.toHaveBeenCalled();
    expect(onRewind).not.toHaveBeenCalled();
    expect(onCopy).toHaveBeenCalledOnce();

    setHeld(false);
    expect(toolbar.querySelectorAll("button")).toHaveLength(3);
    expect(edit.hasAttribute("aria-disabled")).toBe(false);
    fireEvent.click(rewind);
    expect(onRewind).toHaveBeenCalledOnce();
  });
});

describe("SessionRecoverDialog", () => {
  it("names the action it is about to take", () => {
    const { unmount } = render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "edit", messageId: "u1", text: "x" }}
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(screen.getByText("Edit this ask?")).toBeTruthy();
    // Both Edit and Rewind put the ask back in the composer.
    expect(screen.getByTestId("session-recover-dialog").textContent).toContain("composer");
    unmount();

    render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(screen.getByText("Rewind from here?")).toBeTruthy();
    expect(screen.getByTestId("session-recover-dialog").textContent).toContain("composer");
  });

  it("blocks confirmation while a turn is live", () => {
    const { unmount } = render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        stopsLive
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(screen.getByTestId("session-recover-dialog").textContent).toContain(
      "Wait for the current response to finish.",
    );
    unmount();

    render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(screen.getByTestId("session-recover-dialog").textContent).not.toContain(
      "stops first",
    );
  });

  it("offers no file-vs-chat chooser and no don't-ask-again", () => {
    render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    const dialog = screen.getByTestId("session-recover-dialog");
    expect(dialog.querySelectorAll("input[type=radio], input[type=checkbox]").length).toBe(0);
  });

  it("locks both buttons while working", () => {
    render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        busy
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect((screen.getByTestId("session-recover-cancel") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("session-recover-confirm") as HTMLButtonElement).disabled).toBe(true);
  });

  it("keeps the dialog open and shows the host error on failure", () => {
    render(() => (
      <SessionRecoverDialog
        target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
        error="Still working"
        onConfirm={() => {}}
        onCancel={() => {}}
      />
    ));
    expect(screen.getByTestId("session-recover-error").textContent).toContain("Still working");
  });

  it("takes focus on open and returns it to the opener on cancel", async () => {
    const opener = document.createElement("button");
    opener.textContent = "Edit";
    document.body.appendChild(opener);
    opener.focus();
    expect(document.activeElement).toBe(opener);

    const [target, setTarget] = createSignal<RecoveryTarget | null>({
      operationId: "operation-1",
      action: "rewind",
      messageId: "u1",
      text: "x",
    });
    render(() => (
      <SessionRecoverDialog
        target={target()}
        onConfirm={() => {}}
        onCancel={() => setTarget(null)}
      />
    ));
    await waitFor(() =>
      expect(
        screen.getByTestId("session-recover-dialog").contains(document.activeElement),
      ).toBe(true),
    );

    fireEvent.click(screen.getByTestId("session-recover-cancel"));
    await waitFor(() => expect(document.activeElement).toBe(opener));
    opener.remove();
  });

  it("requires a complete conflict-free host preview", () => {
    const [preview, setPreview] = createSignal({ plan_digest: "", files: [], issues: [{ root_id: "root", path: "a.txt", code: "working_file_changed" }], truncated_message_count: 2 });
    const confirm = vi.fn();
    render(() => <SessionRecoverDialog
      target={{ operationId: "operation-1", action: "rewind", messageId: "u1", text: "x" }}
      preview={preview()} onConfirm={confirm} onCancel={() => {}}
    />);
    const button = screen.getByTestId("session-recover-confirm") as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    fireEvent.click(button);
    expect(confirm).not.toHaveBeenCalled();
    setPreview({ plan_digest: "reviewed", files: [], issues: [], truncated_message_count: 2 });
    expect(button.disabled).toBe(false);
    fireEvent.click(button);
    expect(confirm).toHaveBeenCalledOnce();
  });

  it("renders nothing without a target", () => {
    render(() => (
      <SessionRecoverDialog target={null} onConfirm={() => {}} onCancel={() => {}} />
    ));
    expect(screen.queryByTestId("session-recover-dialog")).toBeNull();
  });
});
