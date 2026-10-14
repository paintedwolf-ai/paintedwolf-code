import { fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SessionSummary } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { ProjectChatsView } from "./ProjectChatsView.tsx";

const listProjectSessions = vi.fn();

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return { ...actual, getLycaonClient: () => ({ listProjectSessions }) };
});

function chat(overrides: Partial<SessionSummary> = {}): SessionSummary {
  return {
    id: "chat-1",
    project_id: "proj-1",
    title: "Release notes",
    message_count: 4,
    created_at: "2026-10-01T12:00:00Z",
    activity_at: "2026-10-02T12:00:00Z",
    pin_rank: 0,
    ...overrides,
  } as SessionSummary;
}

function renderView(onOpenSession = vi.fn()) {
  listProjectSessions.mockResolvedValue({ sessions: [chat()], total: 1 });
  render(() => (
    <ProjectChatsView
      projectId="proj-1"
      appStore={createAppStore()}
      attention={new Map([["chat-1", { class: "running" }]]) as never}
      onOpenSession={onOpenSession}
      onSetArchived={vi.fn(async () => true)}
      onSetPinned={vi.fn(async () => true)}
      onDeleteSession={vi.fn(async () => true)}
    />
  ));
  return onOpenSession;
}

afterEach(() => listProjectSessions.mockReset());

describe("project chats rows", () => {
  it("lays the row out on a wrapper inside its list item", async () => {
    renderView();
    const item = await screen.findByTestId("chat-list-row-chat-1");
    expect(item.tagName).toBe("LI");
    expect(item.firstElementChild?.classList.contains("chat-list-row")).toBe(true);
    expect(screen.getByText("Release notes")).toBeTruthy();
    expect(item.querySelector('[aria-label="Pinned"]')).toBeTruthy();
    expect(item.querySelector('[data-attention-class="running"]')).toBeTruthy();
    expect(item.querySelector(".chat-list-row__count")?.textContent).toBe("4");
  });

  it("opens, selects and offers actions for a chat", async () => {
    const onOpenSession = renderView();
    fireEvent.click(await screen.findByTestId("chat-list-open-chat-1"));
    expect(onOpenSession).toHaveBeenCalledWith("chat-1");

    const select = screen.getByTestId("chat-list-select-chat-1") as HTMLInputElement;
    fireEvent.click(select);
    expect(screen.getByTestId("chat-list-row-chat-1").firstElementChild?.classList.contains("chat-list-row--selected")).toBe(true);

    const menu = screen.getByTestId("chat-list-menu-chat-1");
    fireEvent.click(menu);
    expect(menu.getAttribute("aria-expanded")).toBe("true");
  });
});
