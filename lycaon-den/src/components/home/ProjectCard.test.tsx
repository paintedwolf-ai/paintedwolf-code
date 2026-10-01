import { stubClient } from "../../test/client-fixture.ts";
import { ErrorBoundary } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, fireEvent, screen, waitFor } from "@solidjs/testing-library";
import { ProjectCard } from "./ProjectCard.tsx";
import type { ProjectSummary } from "../../project/project-summary.ts";
import { clearArtifactDeletionMemory, notifyArtifactChanged } from "../../chat/visual/artifact-change-store.ts";

afterEach(() => {
  clearArtifactDeletionMemory();
  vi.restoreAllMocks();
});

const getLycaonClient = vi.hoisted(() => vi.fn());
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient }));

function summary(over: Partial<ProjectSummary> & { id: string }): ProjectSummary {
  return {
    id: over.id,
    displayName: over.displayName ?? over.id,
    folders: [],
    primaryFolder: null,
    folderLabel: "No folder",
    sessionCount: 0,
    chatCountLabel: "0 chats",
    lastActivityLabel: "new",
    lastActivityAtMs: over.lastActivityAtMs ?? null,
    starred: over.starred ?? false,
    isDraft: over.isDraft ?? false,
    coverArtifactId: over.coverArtifactId ?? null,
    coverRootSessionId: over.coverRootSessionId ?? null,
  };
}

function renderCard(project: ProjectSummary) {
  const onPromote = vi.fn();
  const onDelete = vi.fn();
  const result = render(() => (
    <ProjectCard
      project={project}
      onOpen={vi.fn()}
      onToggleStar={vi.fn()}
      onRename={vi.fn()}
      onAttachFolder={vi.fn()}
      onPromote={onPromote}
      onDelete={onDelete}
    />
  ));
  return { ...result, onPromote, onDelete };
}

describe("ProjectCard drafts", () => {
  it("shows a draft badge and a promote action for drafts", () => {
    const { getByTestId, onPromote } = renderCard(
      summary({ id: "d1", isDraft: true }),
    );
    expect(getByTestId("project-card-draft-d1")).toBeTruthy();
    fireEvent.click(getByTestId("project-card-menu-d1"));
    fireEvent.click(screen.getByTestId("project-card-promote-d1"));
    expect(onPromote).toHaveBeenCalledWith("d1");
  });

  it("hides the draft badge and promote action for saved projects", () => {
    const { queryByTestId, getByTestId } = renderCard(summary({ id: "s1" }));
    expect(queryByTestId("project-card-draft-s1")).toBeNull();
    fireEvent.click(getByTestId("project-card-menu-s1"));
    expect(screen.queryByTestId("project-card-promote-s1")).toBeNull();
  });

  it("calls onDelete from the project menu", () => {
    const { getByTestId, onDelete } = renderCard(summary({ id: "s2" }));
    fireEvent.click(getByTestId("project-card-menu-s2"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete" }));
    expect(onDelete).toHaveBeenCalledWith("s2");
  });
});

describe("ProjectCard rename (shared InlineRenameInput)", () => {
  it("commits rename via the shared inline field", () => {
    const onRename = vi.fn();
    const project = summary({ id: "r1", displayName: "Old name" });
    const { getByTestId } = render(() => (
      <ProjectCard
        project={project}
        onOpen={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={onRename}
        onAttachFolder={vi.fn()}
        onPromote={vi.fn()}
        onDelete={vi.fn()}
      />
    ));
    fireEvent.click(getByTestId("project-card-menu-r1"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    const input = getByTestId("project-card-rename-r1") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "New name" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onRename).toHaveBeenCalledWith("r1", "New name");
  });

  it("Esc cancels without calling onRename", () => {
    const onRename = vi.fn();
    const project = summary({ id: "r2", displayName: "Keep me" });
    const { getByTestId, queryByTestId } = render(() => (
      <ProjectCard
        project={project}
        onOpen={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={onRename}
        onAttachFolder={vi.fn()}
        onPromote={vi.fn()}
        onDelete={vi.fn()}
      />
    ));
    fireEvent.click(getByTestId("project-card-menu-r2"));
    fireEvent.click(screen.getByRole("menuitem", { name: "Rename" }));
    fireEvent.keyDown(getByTestId("project-card-rename-r2"), { key: "Escape" });
    expect(queryByTestId("project-card-rename-r2")).toBeNull();
    expect(onRename).not.toHaveBeenCalled();
  });
});

/**
 * Home renders every card in one stage. A card whose cover artifact is gone must
 * fall back to its generated thumbnail, not take the grid down with it.
 */
describe("ProjectCard missing cover", () => {
  it("retires a loaded cover when its artifact is deleted", async () => {
    const revoke = vi.spyOn(URL, "revokeObjectURL");
    getLycaonClient.mockReturnValue(stubClient({
      getSessionArtifact: vi.fn(async () => new Blob(["synthetic"], { type: "image/png" })),
    }));
    const view = renderCard(summary({ id: "cover-project", coverRootSessionId: "session", coverArtifactId: "cover-artifact" }));
    await waitFor(() => expect(view.container.querySelector("img")).not.toBeNull());
    notifyArtifactChanged({ project_id: "cover-project", artifact_id: "cover-artifact", op: "deleted" });
    await waitFor(() => expect(view.container.querySelector("img")).toBeNull());
    expect(revoke).toHaveBeenCalledOnce();
    view.unmount();
  });

  it("falls back to the generated thumbnail instead of reaching the stage boundary", async () => {
    const stageFailed = vi.fn();
    const getSessionArtifact = vi.fn(async () => {
      throw new Error("artifact 404");
    });
    getLycaonClient.mockReturnValue(stubClient({
      getSessionArtifact,
    }));

    render(() => (
      <ErrorBoundary
        fallback={(err) => {
          stageFailed(err);
          return <p data-testid="stage-boundary">Reload view</p>;
        }}
      >
        <ProjectCard
          project={summary({
            id: "p-cover",
            coverRootSessionId: "sess-1",
            coverArtifactId: "art-gone",
          })}
          onOpen={vi.fn()}
          onToggleStar={vi.fn()}
          onRename={vi.fn()}
          onAttachFolder={vi.fn()}
          onPromote={vi.fn()}
          onDelete={vi.fn()}
        />
      </ErrorBoundary>
    ));

    await waitFor(() => expect(getSessionArtifact).toHaveBeenCalled());
    await waitFor(() =>
      expect(
        screen.getByTestId("project-thumbnail").getAttribute("data-pending"),
      ).toBeNull(),
    );
    expect(stageFailed).not.toHaveBeenCalled();
    expect(screen.queryByTestId("stage-boundary")).toBeNull();
    expect(screen.queryByTestId("project-thumbnail-live")).toBeNull();
  });
});
