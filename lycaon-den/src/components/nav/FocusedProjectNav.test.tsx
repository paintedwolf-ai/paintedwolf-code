import { fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { FocusedProjectNav } from "./FocusedProjectNav.tsx";
import type { SidebarChatRow } from "../../store/projects-sidebar-model.ts";
import {
  requestedFirstTimeTips,
  resetFirstTimeTipRequestsForTests,
} from "../../first-time-tips/first-time-tips-service.ts";

function chatRow(overrides: Partial<SidebarChatRow> = {}): SidebarChatRow {
  return {
    projectId: "p1",
    sessionId: "s1",
    title: "Fix tests",
    activityAtMs: 0,
    pinRank: null,
    messageCount: 0,
    ...overrides,
  };
}

const baseProps = {
  roots: [],
  sessionsLoaded: true,
  totalChats: 0,
  newChatActive: false,
  onNewChat: vi.fn(),
  onSelectSession: vi.fn(),
  onRenameSession: vi.fn(),
  onTogglePin: vi.fn(),
  onMovePin: vi.fn(),
  chatSort: "created" as const,
  chatSortSettled: true,
  onChatSortChange: vi.fn(),
  onArchiveSession: vi.fn(),
  onDeleteSession: vi.fn(),
  onOpenAllChats: vi.fn(),
  allChatsActive: false,
  onOpenLauncher: vi.fn(),
  onAddFolder: vi.fn(),
  onDetachFolder: vi.fn(),
  onOpenStage: vi.fn(),
  isStageActive: () => false,
  isStageAvailable: () => true,
  onOpenConfiguration: vi.fn(),
  configActive: false,
  chatFocused: true,
};

describe("FocusedProjectNav", () => {
  beforeEach(() => resetFirstTimeTipRequestsForTests());
  afterEach(() => resetFirstTimeTipRequestsForTests());

  it("shows empty state without active project", () => {
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={null}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
      />
    ));
    expect(screen.getByTestId("focused-project-nav-no-project")).toBeTruthy();
  });

  it("renders the switcher, folder zone, sessions, and context for an active project", () => {
    const onOpenLauncher = vi.fn();
    const onSelectSession = vi.fn();
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={{
          id: "p1",
          displayName: "Alpha",
          folders: ["/tmp/a"],
          primaryFolder: "/tmp/a",
          folderLabel: "/tmp/a",
          sessionCount: 0,
          chatCountLabel: "0 chats",
          lastActivityLabel: "new",
          lastActivityAtMs: null,
          starred: false,
          isDraft: false,
          coverArtifactId: null,
          coverRootSessionId: null,
        }}
        roots={[
          {
            id: "r1",
            path: "/tmp/a",
            label: "a",
            is_primary: true,
            added_at: "",
            kind: "attached",
          },
        ]}
        sessions={{ pinned: [], chats: [chatRow()] }}
        totalChats={1}
        identityLoaded={true}
        activeChat={{ projectId: "p1", sessionId: "s1" }}
        onSelectSession={onSelectSession}
        onOpenLauncher={onOpenLauncher}
      />
    ));
    expect(screen.getByTestId("project-cluster")).toBeTruthy();
    expect(screen.getByTestId("project-switcher")).toBeTruthy();
    expect(screen.getByTestId("project-folder-zone")).toBeTruthy();
    expect(screen.getByTestId("focused-session-list")).toBeTruthy();
    expect(screen.getByTestId("project-context-zone")).toBeTruthy();
    expect(screen.getByTestId("project-files-entry")).toBeTruthy();
    expect(screen.getByTestId("project-configuration-entry")).toBeTruthy();
    expect(screen.getByText("Fix tests")).toBeTruthy();
    expect(screen.getByTestId("all-chats-entry")).toBeTruthy();
    expect(requestedFirstTimeTips()).toContain("files-ai-editor");
    expect(requestedFirstTimeTips()).toContain("selected-chat");
    // Chat-list guidance anchors to the list.
    expect(
      screen
        .getByTestId("focused-session-list")
        .getAttribute("data-first-time-tip-anchor"),
    ).toBe("selected-chat");
    fireEvent.click(screen.getByLabelText(/Switch project — Alpha/i));
    expect(onOpenLauncher).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByText("Fix tests"));
    expect(onSelectSession).toHaveBeenCalled();
  });

  it("keeps the rail mounted when the project record refreshes and remounts only on a new project", () => {
    const summary = (id: string, displayName: string) => ({
      id,
      displayName,
      folders: ["/tmp/a"],
      primaryFolder: "/tmp/a",
      folderLabel: "/tmp/a",
      sessionCount: 0,
      chatCountLabel: "0 chats",
      lastActivityLabel: "new",
      lastActivityAtMs: null,
      starred: false,
      isDraft: false,
      coverArtifactId: null,
      coverRootSessionId: null,
    });
    const [activeProject, setActiveProject] = createSignal(summary("p1", "Alpha"));
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={activeProject()}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
        statusChips={<div data-testid="rail-status-probe" />}
      />
    ));
    const chips = screen.getByTestId("rail-status-probe");
    const newChat = screen.getByTestId("new-chat-btn");
    const sessionList = screen.getByTestId("focused-session-list");

    setActiveProject(summary("p1", "Alpha renamed"));
    expect(screen.getByTestId("rail-status-probe")).toBe(chips);
    expect(screen.getByTestId("new-chat-btn")).toBe(newChat);
    expect(screen.getByTestId("focused-session-list")).toBe(sessionList);
    expect(screen.getByLabelText(/Switch project — Alpha renamed/i)).toBeTruthy();

    setActiveProject(summary("p2", "Beta"));
    expect(screen.getByTestId("rail-status-probe")).not.toBe(chips);
    expect(screen.getByTestId("new-chat-btn")).not.toBe(newChat);
    expect(screen.getByLabelText(/Switch project — Beta/i)).toBeTruthy();
  });

  it("keeps mounted status chips invisible until the workspace reveals", () => {
    const [revealed, setRevealed] = createSignal(false);
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={{
          id: "p1",
          displayName: "Alpha",
          folders: ["/tmp/a"],
          primaryFolder: "/tmp/a",
          folderLabel: "/tmp/a",
          sessionCount: 0,
          chatCountLabel: "0 chats",
          lastActivityLabel: "new",
          lastActivityAtMs: null,
          starred: false,
          isDraft: false,
          coverArtifactId: null,
          coverRootSessionId: null,
        }}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
        statusRevealed={revealed()}
        statusChips={<div data-testid="rail-status-probe" />}
      />
    ));
    // Chips stay in the DOM so their reads can register before the reveal.
    const chips = screen.getByTestId("rail-status-probe");
    const slot = chips.parentElement!;
    expect(slot.getAttribute("data-presentation")).toBe("preparing");
    setRevealed(true);
    expect(slot.getAttribute("data-presentation")).toBe("published");
    expect(screen.getByTestId("rail-status-probe")).toBe(chips);
  });

  it("opens Project configuration from the gear in the cluster header", () => {
    const onOpenConfiguration = vi.fn();
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={{
          id: "p1",
          displayName: "Alpha",
          folders: ["/tmp/a"],
          primaryFolder: "/tmp/a",
          folderLabel: "/tmp/a",
          sessionCount: 0,
          chatCountLabel: "0 chats",
          lastActivityLabel: "new",
          lastActivityAtMs: null,
          starred: false,
          isDraft: false,
          coverArtifactId: null,
          coverRootSessionId: null,
        }}
        roots={[
          {
            id: "r1",
            path: "/tmp/a",
            label: "a",
            is_primary: true,
            added_at: "",
            kind: "attached",
          },
        ]}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
        configActive={true}
        onOpenConfiguration={onOpenConfiguration}
      />
    ));
    const gear = screen.getByTestId("project-configuration-entry");
    expect(gear.getAttribute("data-first-time-tip-anchor")).toBeNull();
    expect(gear.getAttribute("aria-pressed")).toBe("true");
    expect(gear.classList.contains("project-cluster__icon-btn--active")).toBe(
      true,
    );
    fireEvent.click(gear);
    expect(onOpenConfiguration).toHaveBeenCalledOnce();
  });

  it("grays and disables New chat while the new-chat page is open", () => {
    const onNewChat = vi.fn();
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={{
          id: "p1",
          displayName: "Alpha",
          folders: ["/tmp/a"],
          primaryFolder: "/tmp/a",
          folderLabel: "/tmp/a",
          sessionCount: 0,
          chatCountLabel: "0 chats",
          lastActivityLabel: "new",
          lastActivityAtMs: null,
          starred: false,
          isDraft: false,
          coverArtifactId: null,
          coverRootSessionId: null,
        }}
        roots={[
          {
            id: "r1",
            path: "/tmp/a",
            label: "a",
            is_primary: true,
            added_at: "",
            kind: "attached",
          },
        ]}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
        newChatActive={true}
        onNewChat={onNewChat}
      />
    ));
    const btn = screen.getByTestId("new-chat-btn") as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
    expect(btn.classList.contains("new-chat-btn--current")).toBe(true);
    fireEvent.click(btn);
    expect(onNewChat).not.toHaveBeenCalled();
  });

  it("leads with a prominent Add folder when there are no roots", () => {
    const onAddFolder = vi.fn();
    render(() => (
      <FocusedProjectNav
        {...baseProps}
        activeProject={{
          id: "p1",
          displayName: "Untitled",
          folders: [],
          primaryFolder: null,
          folderLabel: "No folder",
          sessionCount: 0,
          chatCountLabel: "0 chats",
          lastActivityLabel: "new",
          lastActivityAtMs: null,
          starred: false,
          isDraft: false,
          coverArtifactId: null,
          coverRootSessionId: null,
        }}
        roots={[]}
        sessions={{ pinned: [], chats: [] }}
        identityLoaded={true}
        activeChat={null}
        onAddFolder={onAddFolder}
      />
    ));
    fireEvent.click(screen.getByTestId("project-folder-add"));
    expect(onAddFolder).toHaveBeenCalledOnce();
  });
});
