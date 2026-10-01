import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { MruEntry } from "../../chat/session/mru-switcher-model.ts";
import {
  RecentChatSwitcher,
  type RecentChatSwitcherHandle,
} from "./RecentChatSwitcher.tsx";

function entry(sessionId: string, projectId = "p1"): MruEntry {
  return { projectId, sessionId, title: sessionId, projectName: projectId };
}

function mount(entries: MruEntry[], onCommit = vi.fn()) {
  let handle: RecentChatSwitcherHandle | undefined;
  const rendered = render(() => (
    <RecentChatSwitcher
      ref={(h) => (handle = h)}
      entries={entries}
      onCommit={onCommit}
    />
  ));
  return { handle: handle!, onCommit, ...rendered };
}

function activeSessionId(): string | undefined {
  return screen
    .getAllByTestId("recent-switcher-row")
    .find((el) => el.getAttribute("data-active") === "true")
    ?.getAttribute("data-session-id") ?? undefined;
}

function releaseModifier(): void {
  fireEvent.keyUp(window, { key: "Control" });
}

describe("RecentChatSwitcher", () => {
  it("stays hidden until the gesture starts", () => {
    mount([entry("a"), entry("b")]);
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
  });

  // The first step selects the previous chat.
  it("opens on the previous chat", () => {
    const { handle } = mount([entry("current"), entry("previous"), entry("older")]);
    handle.step(1);
    expect(activeSessionId()).toBe("previous");
    expect(screen.getByTestId("recent-switcher").hasAttribute("aria-modal")).toBe(
      false,
    );
  });

  it("steps further while the modifier is held", () => {
    const { handle } = mount([entry("current"), entry("previous"), entry("older")]);
    handle.step(1);
    expect(screen.getByTestId("recent-switcher-status").textContent).toBe(
      "Recent chat 2 of 3: previous",
    );
    handle.step(1);
    expect(activeSessionId()).toBe("older");
    expect(screen.getByTestId("recent-switcher-status").textContent).toBe(
      "Recent chat 3 of 3: older",
    );
  });

  it("wraps around the list", () => {
    const { handle } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    handle.step(1);
    expect(activeSessionId()).toBe("current");
  });

  it("opens on the oldest chat when stepping backward", () => {
    const { handle } = mount([entry("current"), entry("previous"), entry("older")]);
    handle.step(-1);
    expect(activeSessionId()).toBe("older");
  });

  it("commits the selected chat when the modifier is released", () => {
    const { handle, onCommit } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    releaseModifier();
    expect(onCommit).toHaveBeenCalledOnce();
    expect(onCommit.mock.calls[0]?.[0]?.sessionId).toBe("previous");
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
  });

  // Cycling to the current chat is a no-op.
  it("does not navigate when it lands on the chat already in front", () => {
    const { handle, onCommit } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    handle.step(1);
    releaseModifier();
    expect(onCommit).not.toHaveBeenCalled();
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
  });

  // The shell routes dismissal through the handle.
  it("cancels without navigating", () => {
    const { handle, onCommit } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    expect(handle.isOpen()).toBe(true);
    handle.cancel();
    expect(handle.isOpen()).toBe(false);
    expect(onCommit).not.toHaveBeenCalled();
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
  });

  // Blur cancels the open gesture.
  it("closes when the window loses focus mid-gesture", () => {
    const { handle, onCommit } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    fireEvent.blur(window);
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
    expect(onCommit).not.toHaveBeenCalled();
  });

  it("commits on click", () => {
    const { handle, onCommit } = mount([entry("current"), entry("previous")]);
    handle.step(1);
    const rows = screen.getAllByTestId("recent-switcher-row");
    fireEvent.click(rows[1]!);
    expect(onCommit.mock.calls[0]?.[0]?.sessionId).toBe("previous");
  });

  it("does nothing with no chats to switch between", () => {
    const { handle } = mount([]);
    handle.step(1);
    expect(screen.queryByTestId("recent-switcher")).toBeNull();
  });

  it("names the project each chat belongs to", () => {
    const { handle } = mount([entry("a", "p1"), entry("b", "p2")]);
    handle.step(1);
    expect(screen.getByTestId("recent-switcher").textContent).toContain("p2");
  });
});
