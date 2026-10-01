import { Composer, typeDraft } from "../../test/composer-view-fixture.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen, waitFor } from "@solidjs/testing-library";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";

describe("Composer", () => {

  it("keeps the selection highlighted when a right-click opens the menu", async () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "hello world");
    input.focus();
    input.setSelectionRange(0, 5);
    fireEvent.contextMenu(input, { clientX: 12, clientY: 12, button: 2 });

    await screen.findByTestId("composer-mark-secret");
    await Promise.resolve();
    // The textarea needs focus to paint its selection.
    expect(document.activeElement).toBe(input);
    expect([input.selectionStart, input.selectionEnd]).toEqual([0, 5]);
    expect(screen.getByTestId("text-edit-paste")).toBeTruthy();
    expect(screen.getByTestId("text-edit-select-all")).toBeTruthy();
  });

  it("offers the system edit items without Mark as secret when nothing is selected", async () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "hello world");
    input.focus();
    input.setSelectionRange(5, 5);
    fireEvent.contextMenu(input, { clientX: 12, clientY: 12, button: 2 });

    await screen.findByTestId("text-edit-paste");
    expect(screen.queryByTestId("composer-mark-secret")).toBeNull();
    expect((screen.getByTestId("text-edit-copy") as HTMLButtonElement).disabled).toBe(true);
  });

  it("protects a selected short secret and sends only its typed reference", async () => {
    const onSend = vi.fn().mockResolvedValue(undefined);
    const createComposerSecret = vi.fn().mockResolvedValue({
      id: "123e4567-e89b-12d3-a456-426614174000",
      project_id: "proj-1",
      chat_session_id: "sess-1",
      name: "PIN",
      purpose: "test credential",
      scope: "chat",
      origin: "composer_marked",
      reference: "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}",
      created_at: "2026-09-01T00:00:00Z",
      state: "active",
      version: 1,
      use_count: 0,
      reveal_count: 0,
    });
    setLycaonClientForTest(stubClient({
      createComposerSecret,
      revokeProjectManagedSecret: vi.fn().mockResolvedValue({}),
    }));
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={onSend} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "x");
    input.setSelectionRange(0, 1);
    fireEvent.contextMenu(input, { clientX: 12, clientY: 12 });
    fireEvent.click(await screen.findByTestId("composer-mark-secret"));
    await screen.findByTestId("mark-secret-dialog");
    fireEvent.input(screen.getByTestId("mark-secret-name"), {
      target: { value: "PIN" },
    });
    fireEvent.input(screen.getByTestId("mark-secret-purpose"), {
      target: { value: "test credential" },
    });
    await waitFor(() =>
      expect((screen.getByTestId("mark-secret-submit") as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByTestId("mark-secret-submit"));

    await waitFor(() => expect(input.value).toBe(""));
    expect(createComposerSecret).toHaveBeenCalledWith("sess-1", expect.objectContaining({
      name: "PIN",
      secret_value: "x",
    }));
    expect((await screen.findByTestId("composer-attachment-chips")).textContent).toContain("PIN");
    expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(getByTestId("composer-send"));
    await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1));
    expect(onSend).toHaveBeenCalledWith(expect.objectContaining({
      text: "",
      secrets: [{ reference: "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}" }],
    }));
    expect(JSON.stringify(onSend.mock.calls)).not.toContain('"x"');
  });

  it("removes the marked bytes through an undoable edit the draft follows", async () => {
    const createComposerSecret = vi.fn().mockResolvedValue({
      reference: "{{paintedwolf-secret:123e4567-e89b-12d3-a456-426614174000}}",
      name: "PIN",
      scope: "chat",
      origin: "composer_marked",
      created_at: "2026-09-01T00:00:00Z",
      state: "active",
      version: 1,
      use_count: 0,
      reveal_count: 0,
    });
    setLycaonClientForTest(stubClient({
      createComposerSecret,
      revokeProjectManagedSecret: vi.fn().mockResolvedValue({}),
    }));
    // Supply the undoable edit primitive absent from the test environment.
    const execCommand = vi.fn((command: string) => {
      if (command !== "delete") return false;
      const el = screen.getByTestId("chat-composer") as HTMLTextAreaElement;
      el.value = el.value.slice(0, el.selectionStart) + el.value.slice(el.selectionEnd);
      fireEvent.input(el, { target: { value: el.value } });
      return true;
    });
    Object.defineProperty(document, "execCommand", {
      value: execCommand,
      configurable: true,
      writable: true,
    });

    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "pin hunter2 end");
    input.setSelectionRange(4, 11);
    fireEvent.contextMenu(input, { clientX: 12, clientY: 12 });
    fireEvent.click(await screen.findByTestId("composer-mark-secret"));
    await screen.findByTestId("mark-secret-dialog");
    fireEvent.input(screen.getByTestId("mark-secret-name"), { target: { value: "PIN" } });
    fireEvent.input(screen.getByTestId("mark-secret-purpose"), {
      target: { value: "test credential" },
    });
    await waitFor(() =>
      expect((screen.getByTestId("mark-secret-submit") as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByTestId("mark-secret-submit"));

    // The native edit path creates an undo step.
    await waitFor(() => expect(execCommand).toHaveBeenCalledWith("delete"));
    await waitFor(() => expect(input.value).toBe("pin  end"));

    // Input after undo updates the draft.
    fireEvent.input(input, { target: { value: "pin hunter2 end" } });
    await waitFor(() => expect(input.value).toBe("pin hunter2 end"));
    expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false);

    Reflect.deleteProperty(document, "execCommand");
  });
});
