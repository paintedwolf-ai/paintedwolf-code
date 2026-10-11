import { render } from "@solidjs/testing-library";
import { createSignal, type ComponentProps } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectDialogs } from "./ProjectDialogs.tsx";
import type { MoveToProjectDialog } from "../home/MoveToProjectDialog.tsx";
import type { CloneRepoDialog } from "../home/CloneRepoDialog.tsx";
import type { WorkspaceFoldersPanel } from "../WorkspaceFoldersPanel.tsx";
import type { OpenFolderConfirm } from "../home/OpenFolderConfirm.tsx";

const seams = vi.hoisted(() => ({
  move: null as unknown as ComponentProps<typeof MoveToProjectDialog>,
  clone: null as unknown as ComponentProps<typeof CloneRepoDialog>,
  folders: null as unknown as ComponentProps<typeof WorkspaceFoldersPanel>,
  open: null as unknown as ComponentProps<typeof OpenFolderConfirm>,
  pick: vi.fn(), report: vi.fn(), client: null as unknown as {
    attachProjectRoot: ReturnType<typeof vi.fn>;
    updateProjectRoot: ReturnType<typeof vi.fn>;
  } | null,
}));
vi.mock("../home/MoveToProjectDialog.tsx", () => ({ MoveToProjectDialog: (p: typeof seams.move) => { seams.move = p; return null; } }));
vi.mock("../home/CloneRepoDialog.tsx", () => ({ CloneRepoDialog: (p: typeof seams.clone) => { seams.clone = p; return null; } }));
vi.mock("../home/OpenFolderConfirm.tsx", () => ({ OpenFolderConfirm: (p: typeof seams.open) => { seams.open = p; return null; } }));
vi.mock("../WorkspaceFoldersPanel.tsx", () => ({ WorkspaceFoldersPanel: (p: typeof seams.folders) => { seams.folders = p; return null; } }));
vi.mock("./ProjectRemovalDialog.tsx", () => ({ ProjectRemovalDialog: () => null }));
vi.mock("../../platform/files/folder.ts", () => ({ pickProjectFolder: seams.pick }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => seams.client, noticeReporterFor: () => ({ reportError: seams.report }) }));

function mountDialogs() {
  const [promotion, setPromotion] = createSignal<string>("copying");
  const [panel, setPanel] = createSignal<string | null>("project");
  const lifecycle = {
    moveTarget: () => "project", moveBusy: () => false, moveError: () => null,
    setMoveError: vi.fn(), submitMove: vi.fn(async () => undefined), setMoveTarget: vi.fn(),
    dialogError: (error: unknown) => (error as Error).message, detachProjectRoot: vi.fn(async () => undefined),
  };
  const opening = {
    cloneOpen: () => true, cloneBusy: () => false, cloneError: () => null, setCloneError: vi.fn(),
    setCloneOpen: vi.fn(), submitClone: vi.fn(async () => undefined), openFolderPath: () => "/workspace",
    openFolderDetect: () => null, openFolderBusy: () => false, confirmOpenFolder: vi.fn(async () => undefined), closeOpenFolder: vi.fn(),
  };
  const projects = {
    byId: () => ({ roots: [{ id: "attached", kind: "attached" }, { id: "primary", kind: "primary" }] }),
    upsert: vi.fn(),
  };
  const result = render(() => <ProjectDialogs projects={projects as never} activeProject={() => ({ promotion: { phase: promotion() } }) as never}
    review={{} as never} lifecycle={lifecycle} opening={opening as never} foldersPanelProjectId={panel} setFoldersPanelProjectId={setPanel} />);
  return { ...result, lifecycle, opening, projects, setPromotion, panel };
}

beforeEach(() => {
  seams.pick.mockReset(); seams.report.mockReset();
  seams.client = { attachProjectRoot: vi.fn(), updateProjectRoot: vi.fn() };
});

describe("workspace project dialogs", () => {
  it("keeps promotion busy until committed and routes picker errors to the correct dialog", async () => {
    const { lifecycle, opening, setPromotion } = mountDialogs();
    expect(seams.move.busy).toBe(true);
    setPromotion("committed");
    expect(seams.move.busy).toBe(false);
    seams.pick.mockResolvedValueOnce("/move");
    expect(await seams.move.onPickFolder()).toBe("/move");
    seams.pick.mockRejectedValueOnce(new Error("picker unavailable"));
    expect(await seams.move.onPickFolder()).toBeNull();
    expect(lifecycle.setMoveError).toHaveBeenLastCalledWith("picker unavailable");
    seams.pick.mockResolvedValueOnce("/parent");
    expect(await seams.clone.onPickParent()).toBe("/parent");
    seams.pick.mockRejectedValueOnce(new Error("clone picker unavailable"));
    expect(await seams.clone.onPickParent()).toBeNull();
    expect(opening.setCloneError).toHaveBeenLastCalledWith("clone picker unavailable");
    seams.move.onSubmit({ rootPath: "/move", initGit: true }); seams.move.onClose();
    expect(lifecycle.submitMove).toHaveBeenCalledWith({ rootPath: "/move", initGit: true });
    expect(lifecycle.setMoveTarget).toHaveBeenCalledWith(null);
    seams.clone.onClose(); seams.open.onClose();
    expect(opening.setCloneOpen).toHaveBeenCalledWith(false);
    expect(opening.closeOpenFolder).toHaveBeenCalledOnce();
  });

  it("updates folder rows only after host mutations and reports attachment failures", async () => {
    const { projects, lifecycle, panel } = mountDialogs();
    expect(seams.folders.roots.map((root) => root.id)).toEqual(["attached"]);
    const updated = { id: "project", roots: [] };
    seams.client!.attachProjectRoot.mockResolvedValueOnce(updated);
    await seams.folders.onAttach("/new");
    expect(seams.client!.attachProjectRoot).toHaveBeenCalledWith("project", { path: "/new" });
    expect(projects.upsert).toHaveBeenLastCalledWith(updated);
    const error = new Error("attach failed");
    seams.client!.attachProjectRoot.mockRejectedValueOnce(error);
    await seams.folders.onAttach("/bad");
    expect(seams.report).toHaveBeenCalledWith(error);
    seams.client!.updateProjectRoot.mockResolvedValue(updated);
    await seams.folders.onSetPrimary("attached");
    await seams.folders.onRenameLabel("attached", "Sources");
    expect(seams.client!.updateProjectRoot).toHaveBeenCalledWith("project", "attached", { is_primary: true });
    expect(seams.client!.updateProjectRoot).toHaveBeenCalledWith("project", "attached", { label: "Sources" });
    await seams.folders.onDetach("attached");
    expect(lifecycle.detachProjectRoot).toHaveBeenCalledWith("project", "attached");
    seams.client = null;
    projects.upsert.mockClear();
    await seams.folders.onAttach("/offline"); await seams.folders.onSetPrimary("attached"); await seams.folders.onRenameLabel("attached", "Offline");
    expect(projects.upsert).not.toHaveBeenCalled();
    seams.folders.onClose(); expect(panel()).toBeNull();
  });
});
