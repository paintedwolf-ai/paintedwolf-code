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

  it("keeps today's dialog unchanged when deleting a chat with no running commands", async () => {
    const { unmount } = render(() => <ConfirmDestructiveHost />);
    const pending = confirmChatDelete({
      count: 1,
      projectId: "proj-1",
      chats: [{ sessionId: "s-1", title: "Empty chat" }],
      client: {
        listProjectBackgroundProcesses: async () => ({ processes: [], revision: 1 }),
      },
    });
    const dialog = await waitFor(() =>
      screen.getByTestId("confirm-destructive-dialog"),
    );
    expect(dialog.textContent).toContain(
      "Delete this chat and its full history? This cannot be undone.",
    );
    expect(screen.getByTestId("confirm-destructive-ok").textContent).toBe(
      "Delete",
    );
    expect(screen.queryByTestId("confirm-destructive-open-running")).toBeNull();
    fireEvent.click(screen.getByTestId("confirm-destructive-ok"));
    await expect(pending).resolves.toBe(true);
    unmount();
  });

  it("lists running commands with ports, uses 'Delete and stop', links to Running, and refreshes if the affected set changes", async () => {
    const { unmount } = render(() => <ConfirmDestructiveHost />);
    let calls = 0;
    const initialProcesses = [
      {
        process_id: "bg_1",
        project_id: "proj-1",
        session_id: "s-1",
        session_title: "Dev server",
        command: "python3 -m http.server 8000",
        running: true,
        ports: ["127.0.0.1:8000"],
      },
      {
        process_id: "bg_2",
        project_id: "proj-1",
        session_id: "s-1",
        session_title: "Dev server",
        command: "npm run watch",
        running: true,
        ports: [],
      },
    ];
    const updatedProcesses = [
      ...initialProcesses,
      {
        process_id: "bg_3",
        project_id: "proj-1",
        session_id: "s-1",
        session_title: "Dev server",
        command: "redis-server --port 6380",
        running: true,
        ports: ["127.0.0.1:6380"],
      },
    ];
    let openedRunning = 0;
    const pending = confirmChatDelete({
      count: 1,
      projectId: "proj-1",
      chats: [{ sessionId: "s-1", title: "Dev server" }],
      onOpenRunning: () => {
        openedRunning += 1;
      },
      client: {
        listProjectBackgroundProcesses: async () => {
          calls += 1;
          return {
            processes: calls === 1 ? initialProcesses : updatedProcesses,
          } as never;
        },
      },
    });
    const dialog = await waitFor(() =>
      screen.getByTestId("confirm-destructive-dialog"),
    );
    expect(dialog.textContent).toContain(
      "2 background commands are running in this chat and will stop when the chat is deleted:",
    );
    expect(dialog.textContent).toContain(
      "• python3 -m http.server 8000 (127.0.0.1:8000)",
    );
    expect(dialog.textContent).toContain("• npm run watch");
    expect(screen.getByTestId("confirm-destructive-ok").textContent).toBe(
      "Delete and stop",
    );
    expect(screen.getByTestId("confirm-destructive-open-running")).toBeTruthy();

    // First confirm detects that bg_3 started before destructive admission and refreshes the confirmation.
    fireEvent.click(screen.getByTestId("confirm-destructive-ok"));
    await waitFor(() =>
      expect(
        screen.getByTestId("confirm-destructive-dialog").textContent,
      ).toContain(
        "3 background commands are running in this chat and will stop when the chat is deleted:",
      ),
    );
    expect(
      screen.getByTestId("confirm-destructive-dialog").textContent,
    ).toContain("• redis-server --port 6380 (127.0.0.1:6380)");

    // Second confirm admits deletion now that the set matches.
    fireEvent.click(screen.getByTestId("confirm-destructive-ok"));
    await expect(pending).resolves.toBe(true);
    expect(openedRunning).toBe(0);
    unmount();
  });

  it("groups running commands by chat on multi-chat delete", async () => {
    const { unmount } = render(() => <ConfirmDestructiveHost />);
    const processes = [
      {
        process_id: "bg_1",
        project_id: "proj-1",
        session_id: "s-1",
        session_title: "API server",
        command: "go run ./cmd/api",
        running: true,
        ports: ["127.0.0.1:8080"],
      },
      {
        process_id: "bg_2",
        project_id: "proj-1",
        session_id: "s-2",
        session_title: "Frontend dev",
        command: "vite --port 5173",
        running: true,
        ports: ["127.0.0.1:5173"],
      },
    ];
    const pending = confirmChatDelete({
      count: 2,
      projectId: "proj-1",
      chats: [
        { sessionId: "s-1", title: "API server" },
        { sessionId: "s-2", title: "Frontend dev" },
      ],
      client: {
        listProjectBackgroundProcesses: async () =>
          ({ processes }) as never,
      },
    });
    const dialog = await waitFor(() =>
      screen.getByTestId("confirm-destructive-dialog"),
    );
    expect(dialog.textContent).toContain("API server:");
    expect(dialog.textContent).toContain(
      "• go run ./cmd/api (127.0.0.1:8080)",
    );
    expect(dialog.textContent).toContain("Frontend dev:");
    expect(dialog.textContent).toContain(
      "• vite --port 5173 (127.0.0.1:5173)",
    );
    expect(screen.getByTestId("confirm-destructive-ok").textContent).toBe(
      "Delete and stop",
    );
    fireEvent.click(screen.getByTestId("confirm-destructive-cancel"));
    await expect(pending).resolves.toBe(false);
    unmount();
  });
});
