import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { ProjectFolderZone } from "./ProjectFolderZone.tsx";
import type { ProjectRoot } from "../../api/types.ts";

const roots: ProjectRoot[] = [
  {
    id: "root-a",
    path: "/Users/me/proj",
    label: "proj",
    is_primary: true,
    added_at: "2025-01-01T00:00:00Z",
    kind: "attached",
  },
];

vi.mock("../project-path-menu-items.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../project-path-menu-items.ts")>();
  return {
    ...actual,
    projectPathMenuItems: (opts: {
      absolutePath: string;
    }) => [
      {
        label: "Open in",
        testId: "open-in-menu",
        onSelect: () => {
          void opts.absolutePath;
        },
      },
      {
        label: "Copy path",
        testId: "path-menu-copy-path",
        onSelect: () => {
          void opts.absolutePath;
        },
      },
    ],
  };
});

function manyRoots(n: number): ProjectRoot[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `root-${i}`,
    path: `/Users/me/proj-${i}`,
    label: `proj-${i}`,
    is_primary: i === 0,
    added_at: "2025-01-01T00:00:00Z",
    kind: "attached" as const,
  }));
}

describe("ProjectFolderZone", () => {
  it("opens Reveal + Copy path context menu on folder root rows", () => {
    render(() => (
      <ProjectFolderZone
        roots={roots}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByTestId("project-folder-summary"));
    fireEvent.contextMenu(screen.getByTestId("project-folder-row-root-a"));
    expect(screen.getByTestId("context-menu")).toBeTruthy();
    expect(screen.getByTestId("open-in-menu")).toBeTruthy();
    expect(screen.getByTestId("path-menu-copy-path")).toBeTruthy();
  });

  it("offers clear remove affordances in the row and its context menu", () => {
    const onDetach = vi.fn();
    render(() => (
      <ProjectFolderZone
        roots={roots}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={onDetach}
      />
    ));
    fireEvent.click(screen.getByTestId("project-folder-summary"));
    fireEvent.click(screen.getByTestId("project-folder-remove-root-a"));
    expect(onDetach).toHaveBeenCalledWith("root-a");

    fireEvent.contextMenu(screen.getByTestId("project-folder-row-root-a"));
    fireEvent.click(screen.getByTestId("project-folder-menu-remove-root-a"));
    expect(onDetach).toHaveBeenCalledTimes(2);
  });

  it("keeps the folder list collapsed until the summary is opened", () => {
    render(() => (
      <ProjectFolderZone
        roots={roots}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));
    const summary = screen.getByTestId("project-folder-summary");
    expect(summary.getAttribute("aria-expanded")).toBe("false");
    // Rows stay mounted but hidden, so nothing below the cluster reflows.
    expect(screen.getByTestId("project-folder-row-root-a").offsetParent).toBe(
      null,
    );
    fireEvent.click(summary);
    expect(summary.getAttribute("aria-expanded")).toBe("true");
  });

  it("caps the pile at three tiles and counts the rest", () => {
    render(() => (
      <ProjectFolderZone
        roots={manyRoots(12)}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));
    // Multiple roots share one summary row.
    expect(screen.getByTestId("project-folder-overflow").textContent).toBe("+9");
    expect(
      screen.getByTestId("project-folder-summary").textContent,
    ).toContain("proj-0");
  });

  it("leads the pile with the primary root", () => {
    const shuffled: ProjectRoot[] = [
      { ...manyRoots(3)[0]!, id: "sec", label: "second", is_primary: false },
      { ...manyRoots(3)[1]!, id: "main", label: "main", is_primary: true },
    ];
    render(() => (
      <ProjectFolderZone
        roots={shuffled}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));
    expect(
      screen.getByTestId("project-folder-summary").textContent,
    ).toContain("main");
  });

  it("renders the host label while retaining the physical path as its tooltip", () => {
    render(() => (
      <ProjectFolderZone
        roots={[{ ...roots[0]!, path: "/Users/me/soc-tools", label: "SOC Tools" }]}
        projectId="proj-1"
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));
    const summary = screen.getByTestId("project-folder-summary");
    expect(summary.textContent).toContain("SOC Tools");
    expect(summary.querySelector(".project-folders__path")?.getAttribute("data-tip"))
      .toBe("/Users/me/soc-tools");
  });

  it("keeps Add reachable without expanding", () => {
    const onAddFolder = vi.fn();
    render(() => (
      <ProjectFolderZone
        roots={manyRoots(6)}
        projectId="proj-1"
        onAddFolder={onAddFolder}
        onDetach={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("project-folder-add"));
    expect(onAddFolder).toHaveBeenCalledOnce();
  });

  it("offers retry and cancel when saving a draft fails", () => {
    const onRetryPromotion = vi.fn();
    const onCancelPromotion = vi.fn();
    render(() => (
      <ProjectFolderZone
        roots={[]}
        projectId="proj-1"
        isDraft={true}
        promotePending={true}
        promotionError="Destination changed"
        onRetryPromotion={onRetryPromotion}
        onCancelPromotion={onCancelPromotion}
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));

    expect(screen.getByText("Destination changed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onRetryPromotion).toHaveBeenCalledOnce();
    expect(onCancelPromotion).toHaveBeenCalledOnce();
  });

  it("offers cleanup retry without rollback after promotion commits", () => {
    const onRetryPromotion = vi.fn();
    render(() => (
      <ProjectFolderZone
        roots={roots}
        projectId="proj-1"
        promotePending={true}
        promotionPhase="committed"
        promotionError="Draft cleanup needs attention"
        onRetryPromotion={onRetryPromotion}
        onAddFolder={vi.fn()}
        onDetach={vi.fn()}
      />
    ));

    expect(screen.getByText("Draft cleanup needs attention")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetryPromotion).toHaveBeenCalledOnce();
  });
});
