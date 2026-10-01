import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen, waitFor } from "@solidjs/testing-library";
import { WorkspaceFoldersPanel } from "./WorkspaceFoldersPanel.tsx";
import type { ProjectRoot } from "../api/types.ts";

const roots: ProjectRoot[] = [
  {
    id: "root-a",
    path: "/tmp/a",
    label: "a",
    is_primary: true,
    added_at: "2025-01-01T00:00:00Z",
    kind: "attached",
  },
  {
    id: "root-b",
    path: "/tmp/b",
    label: "b",
    is_primary: false,
    added_at: "2025-01-01T00:00:00Z",
    kind: "attached",
  },
];

describe("WorkspaceFoldersPanel", () => {
  it("calls detachProjectRoot after remove confirm", async () => {
    const onDetach = vi.fn().mockResolvedValue(undefined);
    const { getByTestId } = render(() => (
      <WorkspaceFoldersPanel
        open
        projectId="proj-1"
        roots={roots}
        onClose={() => undefined}
        onAttach={vi.fn()}
        onDetach={onDetach}
        onSetPrimary={vi.fn()}
        onRenameLabel={vi.fn()}
      />
    ));

    fireEvent.click(getByTestId("workspace-folder-remove-root-b"));
    fireEvent.click(getByTestId("workspace-folder-remove-confirm-root-b"));

    await waitFor(() => {
      expect(onDetach).toHaveBeenCalledWith("root-b");
    });
  });

  it("calls patch primary for set main folder", async () => {
    const onSetPrimary = vi.fn().mockResolvedValue(undefined);
    const { getByTestId } = render(() => (
      <WorkspaceFoldersPanel
        open
        projectId="proj-1"
        roots={roots}
        onClose={() => undefined}
        onAttach={vi.fn()}
        onDetach={vi.fn()}
        onSetPrimary={onSetPrimary}
        onRenameLabel={vi.fn()}
      />
    ));

    fireEvent.click(getByTestId("workspace-folder-set-main-root-b"));

    await waitFor(() => {
      expect(onSetPrimary).toHaveBeenCalledWith("root-b");
    });
  });

  it("commits root label rename via shared inline field", async () => {
    const onRenameLabel = vi.fn().mockResolvedValue(undefined);
    const { getByTestId } = render(() => (
      <WorkspaceFoldersPanel
        open
        projectId="proj-1"
        roots={roots}
        onClose={() => undefined}
        onAttach={vi.fn()}
        onDetach={vi.fn()}
        onSetPrimary={vi.fn()}
        onRenameLabel={onRenameLabel}
      />
    ));

    fireEvent.click(getByTestId("project-root-rename-trigger-root-b"));
    const input = getByTestId("project-root-rename-input");
    fireEvent.input(input, { target: { value: "docs" } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(onRenameLabel).toHaveBeenCalledWith("root-b", "docs");
    });
  });

  it("rejects empty root label rename without calling onRenameLabel", async () => {
    const onRenameLabel = vi.fn().mockResolvedValue(undefined);
    const { getByTestId, queryByTestId } = render(() => (
      <WorkspaceFoldersPanel
        open
        projectId="proj-1"
        roots={roots}
        onClose={() => undefined}
        onAttach={vi.fn()}
        onDetach={vi.fn()}
        onSetPrimary={vi.fn()}
        onRenameLabel={onRenameLabel}
      />
    ));

    fireEvent.click(getByTestId("project-root-rename-trigger-root-a"));
    const input = getByTestId("project-root-rename-input");
    fireEvent.input(input, { target: { value: "   " } });
    fireEvent.keyDown(input, { key: "Enter" });

    await waitFor(() => {
      expect(queryByTestId("project-root-rename-input")).toBeNull();
    });
    expect(onRenameLabel).not.toHaveBeenCalled();
    expect(getByTestId("project-root-rename-trigger-root-a").textContent).toBe(
      "a",
    );
  });

  it("opens Reveal + Copy path context menu on folder rows", () => {
    const { getByTestId } = render(() => (
      <WorkspaceFoldersPanel
        open
        projectId="proj-1"
        roots={roots}
        onClose={() => undefined}
        onAttach={vi.fn()}
        onDetach={vi.fn()}
        onSetPrimary={vi.fn()}
        onRenameLabel={vi.fn()}
      />
    ));

    fireEvent.contextMenu(getByTestId("workspace-folder-row-root-a"));
    expect(screen.getByTestId("context-menu")).toBeTruthy();
    expect(screen.getByTestId("project-root-menu-rename-root-a")).toBeTruthy();
    expect(screen.getByTestId("open-in-menu")).toBeTruthy();
    expect(screen.getByTestId("path-menu-copy-path")).toBeTruthy();
    fireEvent.click(screen.getByTestId("project-root-menu-remove-root-a"));
    expect(screen.getByText("Remove this folder from the project?")).toBeTruthy();
  });
});
