import { fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProjectLauncher } from "./ProjectLauncher.tsx";
import { resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";

const summaries = [
  {
    id: "p1",
    displayName: "Alpha",
    folders: ["/repo/a"],
    primaryFolder: "/repo/a",
    folderLabel: "/repo/a",
    sessionCount: 1,
    chatCountLabel: "1 chat",
    lastActivityLabel: "new",
    lastActivityAtMs: null,
    starred: false,
    isDraft: false,
    coverArtifactId: null,
    coverRootSessionId: null,
  },
  {
    id: "p2",
    displayName: "Beta",
    folders: ["/repo/b"],
    primaryFolder: "/repo/b",
    folderLabel: "/repo/b",
    sessionCount: 2,
    chatCountLabel: "2 chats",
    lastActivityLabel: "new",
    lastActivityAtMs: null,
    starred: false,
    isDraft: false,
    coverArtifactId: null,
    coverRootSessionId: null,
  },
];

afterEach(() => {
  resetDispatcherForTests();
});

function pressOverlayKey(init: KeyboardEventInit): void {
  window.dispatchEvent(
    new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }),
  );
}

describe("ProjectLauncher", () => {
  const baseProps = {
    open: true,
    summaries,
    onSwitch: vi.fn(),
    onNewProject: vi.fn(),
    onManageAll: vi.fn(),
    onClose: vi.fn(),
  };

  it("switches on row click", () => {
    const onSwitch = vi.fn();
    render(() => <ProjectLauncher {...baseProps} onSwitch={onSwitch} />);
    fireEvent.click(screen.getByTestId("project-launcher-row-p1"));
    expect(onSwitch).toHaveBeenCalledWith("p1");
  });

  it("exposes modal semantics and moves focus inside", async () => {
    render(() => <ProjectLauncher {...baseProps} />);
    await Promise.resolve();
    const dialog = screen.getByRole("dialog", { name: "Switch project" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(screen.getByRole("textbox", { name: "Search projects" })).toBe(
      document.activeElement,
    );
  });

  it("offers New project and Manage all", () => {
    const onNewProject = vi.fn();
    const onManageAll = vi.fn();
    render(() => (
      <ProjectLauncher
        {...baseProps}
        onNewProject={onNewProject}
        onManageAll={onManageAll}
      />
    ));
    fireEvent.click(screen.getByTestId("project-launcher-new"));
    expect(onNewProject).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByTestId("project-launcher-manage-all"));
    expect(onManageAll).toHaveBeenCalledOnce();
  });

  it("navigates and confirms via list.* commands", () => {
    const onSwitch = vi.fn();
    render(() => <ProjectLauncher {...baseProps} onSwitch={onSwitch} />);
    pressOverlayKey({ key: "ArrowDown", code: "ArrowDown" });
    pressOverlayKey({ key: "Enter", code: "Enter" });
    expect(onSwitch).toHaveBeenCalledWith("p2");
  });

  it("jumps to first and last via Home / End", () => {
    const onSwitch = vi.fn();
    render(() => <ProjectLauncher {...baseProps} onSwitch={onSwitch} />);
    pressOverlayKey({ key: "End", code: "End" });
    pressOverlayKey({ key: "Enter", code: "Enter" });
    expect(onSwitch).toHaveBeenCalledWith("p2");
    onSwitch.mockClear();
    pressOverlayKey({ key: "Home", code: "Home" });
    pressOverlayKey({ key: "Enter", code: "Enter" });
    expect(onSwitch).toHaveBeenCalledWith("p1");
  });
});
