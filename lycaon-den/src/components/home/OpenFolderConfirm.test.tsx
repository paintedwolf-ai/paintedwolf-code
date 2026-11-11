import { fireEvent, render } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { FolderDetect, ProjectRoot } from "../../api/types.ts";
import {
  OpenFolderConfirm,
  defaultOpenFolderProjectId,
  detectedFolderProjects,
} from "./OpenFolderConfirm.tsx";

function attachedRoot(path: string, isPrimary = true): ProjectRoot {
  return {
    id: `root-${path}`,
    path,
    label: path.split("/").pop() ?? "root",
    is_primary: isPrimary,
    added_at: "2026-10-31T00:00:00Z",
    kind: "attached",
  };
}

describe("OpenFolderConfirm", () => {
  it("defaults to opening an existing single-root project and lets the user choose a new project instead", () => {
    const detect: FolderDetect = {
      path: "/repos/app",
      project_id: "single-1",
      projects: [
        {
          id: "single-1",
          name: "App",
          roots: [attachedRoot("/repos/app")],
          is_single_root: true,
          last_opened_at: "2026-10-31T00:00:00Z",
        },
      ],
    };
    expect(defaultOpenFolderProjectId(detect)).toBe("single-1");
    const onConfirm = vi.fn();
    const onClose = vi.fn();

    const { getByTestId } = render(() => (
      <OpenFolderConfirm
        open
        path="/repos/app"
        detect={detect}
        onConfirm={onConfirm}
        onClose={onClose}
      />
    ));

    const existingChoice = getByTestId("open-folder-choice-project-single-1");
    const newChoice = getByTestId("open-folder-choice-new");
    const submit = getByTestId("open-folder-confirm-submit");

    expect(existingChoice.getAttribute("aria-checked")).toBe("true");
    expect(newChoice.getAttribute("aria-checked")).toBe("false");
    expect(submit.textContent).toBe("Open project");

    fireEvent.click(submit);
    expect(onConfirm).toHaveBeenLastCalledWith({
      installIds: [],
      projectId: "single-1",
    });

    fireEvent.click(newChoice);
    expect(newChoice.getAttribute("aria-checked")).toBe("true");
    expect(existingChoice.getAttribute("aria-checked")).toBe("false");
    expect(submit.textContent).toBe("Create new project");

    fireEvent.click(submit);
    expect(onConfirm).toHaveBeenLastCalledWith({
      installIds: [],
      projectId: null,
    });
  });

  it("defaults to creating a new single-folder project when only multi-root projects contain the folder", () => {
    const detect: FolderDetect = {
      path: "/repos/shared-lib",
      projects: [
        {
          id: "multi-1",
          name: "Monorepo Workspace",
          roots: [
            attachedRoot("/repos/main-app", true),
            attachedRoot("/repos/shared-lib", false),
          ],
          is_single_root: false,
          last_opened_at: "2026-10-31T00:00:00Z",
        },
      ],
    };
    expect(defaultOpenFolderProjectId(detect)).toBeNull();
    const onConfirm = vi.fn();
    const onClose = vi.fn();

    const { getByTestId } = render(() => (
      <OpenFolderConfirm
        open
        path="/repos/shared-lib"
        detect={detect}
        onConfirm={onConfirm}
        onClose={onClose}
      />
    ));

    const multiChoice = getByTestId("open-folder-choice-project-multi-1");
    const newChoice = getByTestId("open-folder-choice-new");
    const submit = getByTestId("open-folder-confirm-submit");

    expect(newChoice.getAttribute("aria-checked")).toBe("true");
    expect(multiChoice.getAttribute("aria-checked")).toBe("false");
    expect(multiChoice.textContent).toContain("Monorepo Workspace");
    expect(multiChoice.textContent).toContain("2 folders: /repos/main-app, /repos/shared-lib");
    expect(submit.textContent).toBe("Create new project");

    fireEvent.click(submit);
    expect(onConfirm).toHaveBeenLastCalledWith({
      installIds: [],
      projectId: null,
    });

    fireEvent.click(multiChoice);
    expect(multiChoice.getAttribute("aria-checked")).toBe("true");
    expect(submit.textContent).toBe("Open project");

    fireEvent.click(submit);
    expect(onConfirm).toHaveBeenLastCalledWith({
      installIds: [],
      projectId: "multi-1",
    });

    fireEvent.click(getByTestId("open-folder-cancel"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("synthesizes a single-root entry when only legacy project_id is provided", () => {
    expect(detectedFolderProjects(null)).toEqual([]);
    expect(detectedFolderProjects({ path: "/repos/app" })).toEqual([]);
    expect(detectedFolderProjects({ path: "/repos/app", project_id: "p1" })).toEqual([
      {
        id: "p1",
        name: "app",
        roots: [],
        is_single_root: true,
        last_opened_at: "",
      },
    ]);
  });
});
