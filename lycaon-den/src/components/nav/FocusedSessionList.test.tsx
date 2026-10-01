import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { FocusedSessionList } from "./FocusedSessionList.tsx";
import type {
  SidebarChatRow,
  SidebarChatSections,
} from "../../store/projects-sidebar-model.ts";
import { indexAttention } from "../../attention/attention-model.ts";
import type { AttentionRow } from "../../api/types.ts";

const exportSessionTranscript = vi.fn(async () => undefined);
const getLycaonClient = vi.fn(() => ({
  exportSessionTranscript: vi.fn(),
}));

vi.mock("../../settings/storage/data-backup-actions.ts", () => ({
  exportSessionTranscript: (...args: unknown[]) =>
    exportSessionTranscript(...(args as [])),
}));

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => getLycaonClient(),
}));

function row(overrides: Partial<SidebarChatRow> = {}): SidebarChatRow {
  return {
    projectId: "p1",
    sessionId: "s1",
    title: "Draft",
    activityAtMs: 0,
    pinRank: null,
    messageCount: 0,
    ...overrides,
  };
}

function chats(...rows: SidebarChatRow[]): SidebarChatSections {
  return { pinned: [], chats: rows };
}

function titles(testId: string): string[] {
  return [...screen.getByTestId(testId).querySelectorAll(".focused-session-list__title")].map(
    (el) => el.textContent ?? "",
  );
}

function baseProps() {
  return {
    projectId: "p1",
    loaded: true,
    totalChats: 1,
    activeChat: null,
    sort: "created" as const,
    sortSettled: true,
    onSortChange: vi.fn(),
    onSelectSession: vi.fn(),
    onRenameSession: vi.fn(),
    onTogglePin: vi.fn(),
    onMovePin: vi.fn(),
    onArchiveSession: vi.fn(),
    onDeleteSession: vi.fn(),
    onOpenAllChats: vi.fn(),
  };
}

describe("FocusedSessionList", () => {
  beforeEach(() => {
    exportSessionTranscript.mockClear();
    getLycaonClient.mockClear();
  });
  it("shows empty state when project has no sessions", () => {
    render(() => (
      <FocusedSessionList {...baseProps()} sections={chats()} totalChats={0} loaded />
    ));
    expect(screen.getByTestId("focused-session-list-empty")).toBeTruthy();
    expect(screen.getByText("No chats yet")).toBeTruthy();
  });

  it("does not claim the project is empty before host inventory settles", () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats()}
        totalChats={0}
        loaded={false}
      />
    ));
    expect(screen.getByTestId("focused-session-list-empty").textContent).toBe("");
    expect(screen.queryByText("No chats yet")).toBeNull();
  });

  it("highlights active session and selects on click", () => {
    const props = baseProps();
    render(() => (
      <FocusedSessionList
        {...props}
        sections={chats(
          row({ sessionId: "s1", title: "Fix tests" }),
          row({ sessionId: "s2", title: "Other" }),
        )}
        activeChat={{ projectId: "p1", sessionId: "s1" }}
      />
    ));
    const active = screen.getByText("Fix tests").closest("button");
    expect(active?.classList.contains("den-shell-nav-sub-link-active")).toBe(true);

    // One marker follows current-window visibility.
    const markers = document.querySelectorAll(".den-nav-marker");
    expect(markers).toHaveLength(1);

    fireEvent.click(screen.getByText("Other"));
    expect(props.onSelectSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s2" }),
    );
  });

  it("keeps the selected chat marked while a stage holds the column", async () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(
          row({ sessionId: "s1", title: "Fix tests" }),
          row({ sessionId: "s2", title: "Other" }),
        )}
        activeChat={{ projectId: "p1", sessionId: "s1" }}
        focused={false}
      />
    ));
    const subject = screen.getByText("Fix tests").closest("button");
    expect(subject?.classList.contains("den-shell-nav-sub-link-active")).toBe(
      true,
    );
    expect(subject?.getAttribute("aria-current")).toBe("true");
    expect(subject?.textContent).toContain(
      "Selected chat. Activate to show the conversation.",
    );
    const other = screen.getByText("Other").closest("button");
    expect(other?.classList.contains("den-shell-nav-sub-link-active")).toBe(
      false,
    );
    expect(other?.getAttribute("aria-current")).toBeNull();

    // The visibility marker follows the column.
    const dot = document.querySelector<HTMLElement>(".den-nav-marker");
    await waitFor(() => expect(dot?.style.opacity).toBe("0"));
  });

  it("lights the dot for a chat visible beside a split stage", async () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(
          row({ sessionId: "s1", title: "Fix tests" }),
          row({ sessionId: "s2", title: "Other" }),
        )}
        activeChat={{ projectId: "p1", sessionId: "s1" }}
        focused={true}
      />
    ));
    const dot = document.querySelector<HTMLElement>(".den-nav-marker");
    await waitFor(() => expect(dot?.style.opacity).toBe("1"));
    expect(
      screen
        .getByText("Fix tests")
        .closest("button")
        ?.classList.contains("den-shell-nav-sub-link-active"),
    ).toBe(true);
    expect(screen.getByText("Fix tests").closest("button")?.textContent).toContain(
      "Selected chat, visible.",
    );
  });

  it("archives and deletes from the right-click menu", () => {
    const props = baseProps();
    render(() => (
      <FocusedSessionList
        {...props}
        sections={chats(row({ title: "Fix tests" }))}
      />
    ));

    fireEvent.contextMenu(screen.getByText("Fix tests"));
    expect(screen.getByTestId("context-menu")).toBeTruthy();
    fireEvent.click(screen.getByTestId("session-menu-archive"));
    expect(props.onArchiveSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
    );
    // Selecting dismisses the menu.
    expect(screen.queryByTestId("context-menu")).toBeNull();

    fireEvent.contextMenu(screen.getByText("Fix tests"));
    fireEvent.click(screen.getByTestId("session-menu-delete"));
    expect(props.onDeleteSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
    );
  });

  it("keeps a pulled-off session in the list and raises its window", () => {
    const props = baseProps();
    const openInNewWindow = vi.fn();
    const raiseWindow = vi.fn();
    const closeWindow = vi.fn();
    render(() => (
      <FocusedSessionList
        {...props}
        sections={chats(row())}
        sessionWindowIds={new Set(["s1"])}
        sessionWindowCounts={new Map([["s1", 2]])}
        sessionWindowViewNumbers={new Map([["s1", [3, 7]]])}
        onOpenInNewWindow={openInNewWindow}
        onRaiseWindow={raiseWindow}
        onCloseWindow={closeWindow}
      />
    ));
    const windowButton = screen.getByTestId("session-window-glyph");
    expect(windowButton.tagName).toBe("BUTTON");
    expect(windowButton.closest("button")).toBe(windowButton);
    expect(screen.getByTestId("session-window-glyph").getAttribute("aria-label")).toBe(
      "2 open window views",
    );
    fireEvent.click(screen.getByText("Draft"));
    expect(props.onSelectSession).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "s1" }));
    fireEvent.click(screen.getByTestId("session-window-glyph"));
    expect(raiseWindow).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      3,
    );
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-focus-window"));
    fireEvent.click(screen.getByTestId("session-menu-focus-view-7"));
    expect(raiseWindow).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      7,
    );
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-close-window"));
    fireEvent.click(screen.getByTestId("session-menu-close-view-3"));
    expect(closeWindow).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      3,
    );
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-open-window"));
    expect(openInNewWindow).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "s1" }));
  });

  it("omits window actions without their handlers", () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(row())}
        sessionWindowIds={new Set(["s1"])}
        sessionWindowViewNumbers={new Map([["s1", [3]]])}
      />
    ));
    fireEvent.contextMenu(screen.getByText("Draft"));
    expect(screen.queryByTestId("session-menu-focus-window")).toBeNull();
    expect(screen.queryByTestId("session-menu-close-window")).toBeNull();
    expect(screen.queryByTestId("session-menu-open-window")).toBeNull();
  });

  it("pins and unpins from the menu", () => {
    const props = baseProps();
    const { unmount } = render(() => (
      <FocusedSessionList {...props} sections={chats(row())} />
    ));
    fireEvent.contextMenu(screen.getByText("Draft"));
    expect(screen.getByTestId("session-menu-pin").textContent).toBe("Pin");
    fireEvent.click(screen.getByTestId("session-menu-pin"));
    expect(props.onTogglePin).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      true,
    );
    unmount();

    const pinnedProps = baseProps();
    render(() => (
      <FocusedSessionList
        {...pinnedProps}
        sections={{ pinned: [row({ pinRank: 1 })], chats: [] }}
      />
    ));
    fireEvent.contextMenu(screen.getByText("Draft"));
    expect(screen.getByTestId("session-menu-pin").textContent).toBe("Unpin");
    fireEvent.click(screen.getByTestId("session-menu-pin"));
    expect(pinnedProps.onTogglePin).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      false,
    );
  });

  it("says the attention class as a word in the timestamp slot", () => {
    const att: AttentionRow = {
      session_id: "s1",
      project_id: "p1",
      class: "needs_you",
      reason: "ask",
      since_at: "2026-07-28T00:00:00Z",
    };
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(row({ activityAtMs: Date.now() }))}
        attention={indexAttention([att])}
      />
    ));
    const status = document.querySelector(".focused-session-list__status");
    expect(status?.textContent).toBe("Needs you");
    expect(status?.getAttribute("data-attention-class")).toBe("needs_you");
    // Attention text occupies the time slot.
    expect(document.querySelector(".focused-session-list__time")).toBeNull();
    expect(document.querySelector(".focused-session-list__dot")).toBeNull();
  });

  it("opens All chats from the footer entry with the total count", () => {
    const props = baseProps();
    render(() => (
      <FocusedSessionList {...props} sections={chats(row())} totalChats={42} />
    ));
    const entry = screen.getByTestId("all-chats-entry");
    expect(entry.textContent).toContain("All chats");
    expect(entry.textContent).toContain("42");
    fireEvent.click(entry);
    expect(props.onOpenAllChats).toHaveBeenCalled();
  });

  it("copies the session id from the menu", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    render(() => <FocusedSessionList {...baseProps()} sections={chats(row())} />);
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-copy-id"));
    expect(writeText).toHaveBeenCalledWith("s1");
    vi.unstubAllGlobals();
  });

  it("offers Rename and commits via the shared inline field", () => {
    const props = baseProps();
    render(() => <FocusedSessionList {...props} sections={chats(row())} />);
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-rename"));
    const input = screen.getByTestId("session-rename-input") as HTMLInputElement;
    expect(input.value).toBe("Draft");
    fireEvent.input(input, { target: { value: "Ship checklist" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(props.onRenameSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      "Ship checklist",
    );
  });

  it("renames from the row's hover action button", () => {
    const props = baseProps();
    render(() => <FocusedSessionList {...props} sections={chats(row())} />);
    fireEvent.click(screen.getByTestId("session-rename-action"));
    const input = screen.getByTestId("session-rename-input") as HTMLInputElement;
    expect(input.value).toBe("Draft");
    fireEvent.input(input, { target: { value: "Renamed inline" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(props.onRenameSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      "Renamed inline",
    );
  });

  it("double-clicking a row opens the inline rename field", () => {
    const props = baseProps();
    render(() => <FocusedSessionList {...props} sections={chats(row())} />);
    fireEvent.dblClick(screen.getByText("Draft"));
    const input = screen.getByTestId("session-rename-input") as HTMLInputElement;
    expect(input.value).toBe("Draft");
    fireEvent.input(input, { target: { value: "Renamed by dblclick" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(props.onRenameSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "s1" }),
      "Renamed by dblclick",
    );
  });

  it("Esc cancels rename without committing", () => {
    const props = baseProps();
    render(() => <FocusedSessionList {...props} sections={chats(row())} />);
    fireEvent.contextMenu(screen.getByText("Draft"));
    fireEvent.click(screen.getByTestId("session-menu-rename"));
    const input = screen.getByTestId("session-rename-input");
    fireEvent.input(input, { target: { value: "Nope" } });
    fireEvent.keyDown(input, { key: "Escape" });
    expect(props.onRenameSession).not.toHaveBeenCalled();
    expect(screen.queryByTestId("session-rename-input")).toBeNull();
    expect(screen.getByText("Draft")).toBeTruthy();
  });

  it("disables export for empty/never-run sessions", () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(row({ messageCount: 0 }))}
      />
    ));
    fireEvent.contextMenu(screen.getByText("Draft"));
    expect(
      (screen.getByTestId("session-menu-export") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("exports Markdown via downloadExport path when history exists", () => {
    const client = { exportSessionTranscript: vi.fn() };
    getLycaonClient.mockReturnValue(client);
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={chats(row({ title: "With history", messageCount: 3 }))}
      />
    ));
    fireEvent.contextMenu(screen.getByText("With history"));
    fireEvent.click(screen.getByTestId("session-menu-export"));
    fireEvent.click(screen.getByTestId("session-menu-export-md"));
    expect(exportSessionTranscript).toHaveBeenCalledWith(client, "s1", "md");
  });

  it("groups pinned chats above the rest, in pin order", () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={{
          pinned: [
            row({ sessionId: "p1", title: "First pin", pinRank: 1 }),
            row({ sessionId: "p2", title: "Second pin", pinRank: 2 }),
          ],
          chats: [row({ sessionId: "c1", title: "Loose" })],
        }}
      />
    ));
    expect(screen.getByText("Pinned")).toBeTruthy();
    expect(titles("focused-session-list-pinned")).toEqual(["First pin", "Second pin"]);
    expect(titles("focused-session-list-chats")).toEqual(["Loose"]);
  });

  it("pins and unpins from the row's hover action", () => {
    const props = baseProps();
    render(() => (
      <FocusedSessionList
        {...props}
        sections={{
          pinned: [row({ sessionId: "p1", title: "Pinned one", pinRank: 1 })],
          chats: [row({ sessionId: "c1", title: "Loose" })],
        }}
      />
    ));
    const [unpin, pin] = screen.getAllByTestId("session-pin-action");
    expect(unpin?.getAttribute("aria-pressed")).toBe("true");
    expect(unpin?.getAttribute("aria-label")).toBe("Unpin chat");
    expect(pin?.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(pin!);
    expect(props.onTogglePin).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "c1" }), true);
    fireEvent.click(unpin!);
    expect(props.onTogglePin).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "p1" }), false);
    expect(props.onSelectSession).not.toHaveBeenCalled();
  });

  it("moves a pinned chat up or down from its menu, and only a pinned one", () => {
    const props = baseProps();
    render(() => (
      <FocusedSessionList
        {...props}
        sections={{
          pinned: [
            row({ sessionId: "p1", title: "Top", pinRank: 1 }),
            row({ sessionId: "p2", title: "Bottom", pinRank: 2 }),
          ],
          chats: [row({ sessionId: "c1", title: "Loose" })],
        }}
      />
    ));
    fireEvent.contextMenu(screen.getByText("Top"));
    expect((screen.getByTestId("session-menu-move-up") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId("session-menu-move-down"));
    expect(props.onMovePin).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "p1" }), 2);

    fireEvent.contextMenu(screen.getByText("Bottom"));
    expect((screen.getByTestId("session-menu-move-down") as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId("session-menu-move-up"));
    expect(props.onMovePin).toHaveBeenCalledWith(expect.objectContaining({ sessionId: "p2" }), 1);

    fireEvent.contextMenu(screen.getByText("Loose"));
    expect(screen.queryByTestId("session-menu-move-up")).toBeNull();
  });

  it("chooses the order from the menu beside Chats", () => {
    const props = baseProps();
    render(() => <FocusedSessionList {...props} sections={chats(row())} />);
    const trigger = screen.getByTestId("focused-session-list-sort");
    expect(trigger.textContent).toContain("Created");
    fireEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(
      screen.getByTestId("focused-session-list-sort-created").getAttribute("aria-checked"),
    ).toBe("true");
    fireEvent.click(screen.getByTestId("focused-session-list-sort-activity"));
    expect(props.onSortChange).toHaveBeenCalledWith("activity");
  });

  it("keeps rows still under the pointer and applies the order when it leaves", () => {
    const [sections, setSections] = createSignal(chats(
      row({ sessionId: "a", title: "Alpha" }),
      row({ sessionId: "b", title: "Beta" }),
    ));
    render(() => <FocusedSessionList {...baseProps()} sections={sections()} />);
    const list = screen.getByTestId("focused-session-list");

    fireEvent.pointerEnter(list);
    setSections(chats(
      row({ sessionId: "b", title: "Beta" }),
      row({ sessionId: "a", title: "Alpha" }),
      row({ sessionId: "c", title: "Gamma" }),
    ));
    expect(titles("focused-session-list-chats")).toEqual(["Alpha", "Beta", "Gamma"]);

    fireEvent.pointerLeave(list);
    expect(titles("focused-session-list-chats")).toEqual(["Beta", "Alpha", "Gamma"]);
  });

  it("shows the person's own pin at once, even under the pointer", () => {
    const loose = row({ sessionId: "a", title: "Alpha" });
    const other = row({ sessionId: "b", title: "Beta" });
    const [sections, setSections] = createSignal(chats(loose, other));
    const props = {
      ...baseProps(),
      onTogglePin: vi.fn(() => setSections({ pinned: [{ ...loose, pinRank: 1 }], chats: [other] })),
    };
    render(() => <FocusedSessionList {...props} sections={sections()} />);
    fireEvent.pointerEnter(screen.getByTestId("focused-session-list"));
    fireEvent.click(screen.getAllByTestId("session-pin-action")[0]!);
    expect(titles("focused-session-list-pinned")).toEqual(["Alpha"]);
    expect(titles("focused-session-list-chats")).toEqual(["Beta"]);
  });

  it("applies a new order at once while its rows are still arriving", () => {
    const [sections, setSections] = createSignal(chats(
      row({ sessionId: "a", title: "Alpha" }),
      row({ sessionId: "b", title: "Beta" }),
    ));
    const [settled, setSettled] = createSignal(true);
    render(() => (
      <FocusedSessionList {...baseProps()} sections={sections()} sortSettled={settled()} />
    ));
    fireEvent.pointerEnter(screen.getByTestId("focused-session-list"));
    setSettled(false);
    setSections(chats(row({ sessionId: "b", title: "Beta" }), row({ sessionId: "a", title: "Alpha" })));
    expect(titles("focused-session-list-chats")).toEqual(["Beta", "Alpha"]);
    setSettled(true);
    setSections(chats(row({ sessionId: "a", title: "Alpha" }), row({ sessionId: "b", title: "Beta" })));
    expect(titles("focused-session-list-chats")).toEqual(["Beta", "Alpha"]);
  });

  it("omits the empty line when every chat is pinned", () => {
    render(() => (
      <FocusedSessionList
        {...baseProps()}
        sections={{ pinned: [row({ pinRank: 1 })], chats: [] }}
      />
    ));
    expect(screen.queryByTestId("focused-session-list-empty")).toBeNull();
  });
});
