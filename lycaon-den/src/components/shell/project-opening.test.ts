// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FolderDetect, Project } from "../../api/types.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { stubClient, type ClientStubs } from "../../test/client-fixture.ts";
import { createProjectOpening } from "./project-opening.ts";

const connection = vi.hoisted(() => ({ getLycaonClient: vi.fn() }));
vi.mock("../../platform/connection/app-connection.ts", () => connection);
vi.mock("../../contributions/contribution-store.ts", () => ({ invalidateContributionFrame: vi.fn() }));
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}
const disposals: (() => void)[] = [];
afterEach(() => { for (const dispose of disposals.splice(0)) dispose(); vi.clearAllMocks(); });
function setup(detectFolder: () => Promise<FolderDetect>, overrides: ClientStubs = {}) {
  return createRoot((dispose) => {
    disposals.push(dispose);
    const project = wireProject("/repo", "p1");
    const client = stubClient({ detectFolder: vi.fn(detectFolder), createProject: vi.fn(async () => project), ...overrides });
    connection.getLycaonClient.mockReturnValue(client);
    const shell = createShellStore("connected");
    const startSession = vi.fn(async () => true);
    const reportError = vi.fn();
    const projects = { byId: () => project, summary: () => undefined, upsert: (_project: Project) => { } };
    const opening = createProjectOpening({
      shell, projects, startSession, reportError,
      formatError: String, showProjects: vi.fn(),
      beginWorkspaceOpening: (label) => shell.beginWorkspaceOpening({ label }),
      abandonWorkspaceOpening: (token) => shell.abandonWorkspaceOpening(token),
    });
    return { ...opening, client, shell, startSession, reportError, dispose };
  });
}

describe("project opening lifecycle", () => {
  it("covers the outgoing workspace before requesting a new folder project", async () => {
    const pending = deferred<Project>();
    const observed: boolean[] = [];
    const requestProject = vi.fn(() => {
      observed.push(opening.shell.state.opening !== null);
      return pending.promise;
    });
    const opening = setup(async () => ({ path: "/repo" }), {
      createProject: requestProject,
    });
    opening.shell.beginStageSwitch({ projectId: "outgoing", kind: "cross-project" });

    const request = opening.proposeFolderAsProject("/repo");
    await vi.waitFor(() => expect(requestProject).toHaveBeenCalledTimes(1));
    expect(observed).toEqual([true]);
    expect(opening.shell.state.opening).not.toBeNull();
    expect(opening.startSession).not.toHaveBeenCalled();

    pending.resolve(wireProject("/repo", "p1"));
    await request;
    expect(opening.startSession).toHaveBeenCalledWith(
      { kind: "new-session", projectId: "p1" },
      { switchKind: "cross-project" },
    );
  });

  it("prompts when a folder already belongs to a single-root project and supports both reopen and create-new", async () => {
    const existing = wireProject("/repo", "p1");
    const created = wireProject("/repo", "p-new");
    const getProject = vi.fn(async () => existing);
    const createProject = vi.fn(async () => created);
    const opening = setup(
      async () => ({
        path: "/repo",
        project_id: "p1",
        projects: [
          {
            id: "p1",
            name: "repo",
            roots: existing.roots,
            is_single_root: true,
            last_opened_at: "2026-10-31T00:00:00Z",
          },
        ],
      }),
      { getProject, createProject },
    );

    await opening.proposeFolderAsProject("/repo");
    expect(opening.openFolderPath()).toBe("/repo");
    expect(getProject).not.toHaveBeenCalled();
    expect(createProject).not.toHaveBeenCalled();

    // Default confirmation reopens the single-root project.
    await opening.confirmOpenFolder({ installIds: [] });
    expect(getProject).toHaveBeenCalledWith("p1");
    expect(createProject).not.toHaveBeenCalled();
    expect(opening.openFolderPath()).toBeNull();

    // Choosing projectId: null creates a fresh single-folder project.
    await opening.proposeFolderAsProject("/repo");
    expect(opening.openFolderPath()).toBe("/repo");
    await opening.confirmOpenFolder({ installIds: [], projectId: null });
    expect(createProject).toHaveBeenCalledWith({ roots: [{ path: "/repo" }] });
  });

  it("defaults to creating a new single-folder project when a folder only appears in a multi-root project", async () => {
    const multi = wireProject("/main", "multi-1");
    const created = wireProject("/shared", "single-new");
    const getProject = vi.fn(async () => multi);
    const createProject = vi.fn(async () => created);
    const opening = setup(
      async () => ({
        path: "/shared",
        projects: [
          {
            id: "multi-1",
            name: "Main Workspace",
            roots: multi.roots,
            is_single_root: false,
            last_opened_at: "2026-10-31T00:00:00Z",
          },
        ],
      }),
      { getProject, createProject },
    );

    await opening.proposeFolderAsProject("/shared");
    expect(opening.openFolderPath()).toBe("/shared");
    expect(getProject).not.toHaveBeenCalled();
    expect(createProject).not.toHaveBeenCalled();

    // Default confirmation creates a fresh single-root project instead of hijacking into multi-1.
    await opening.confirmOpenFolder({ installIds: [] });
    expect(createProject).toHaveBeenCalledWith({ roots: [{ path: "/shared" }] });
    expect(getProject).not.toHaveBeenCalled();

    // Explicitly selecting multi-1 opens the existing multi-root project.
    await opening.proposeFolderAsProject("/shared");
    await opening.confirmOpenFolder({ installIds: [], projectId: "multi-1" });
    expect(getProject).toHaveBeenCalledWith("multi-1");
  });

  it("cancelling the open-folder dialog from proposeFolderAsProject aborts without switching projects", async () => {
    const existing = wireProject("/repo", "p1");
    const opening = setup(async () => ({
      path: "/repo",
      project_id: "p1",
      projects: [
        {
          id: "p1",
          name: "repo",
          roots: existing.roots,
          is_single_root: true,
          last_opened_at: "2026-10-31T00:00:00Z",
        },
      ],
    }));

    await opening.proposeFolderAsProject("/repo");
    expect(opening.openFolderPath()).toBe("/repo");

    opening.closeOpenFolder();
    expect(opening.openFolderPath()).toBeNull();
    expect(opening.openFolderDetect()).toBeNull();
    expect(opening.startSession).not.toHaveBeenCalled();
    expect(opening.client.getProject).not.toHaveBeenCalled();
    expect(opening.client.createProject).not.toHaveBeenCalled();
  });

  it("always creates a new single-folder project when forceNew is set", async () => {
    const existing = wireProject("/repo", "p1");
    const created = wireProject("/repo", "p-fresh");
    const getProject = vi.fn(async () => existing);
    const createProject = vi.fn(async () => created);
    const opening = setup(
      async () => ({
        path: "/repo",
        project_id: "p1",
        projects: [
          {
            id: "p1",
            name: "repo",
            roots: existing.roots,
            is_single_root: true,
            last_opened_at: "2026-10-31T00:00:00Z",
          },
        ],
      }),
      { getProject, createProject },
    );

    await opening.proposeFolderAsProject("/repo", { forceNew: true });
    expect(opening.openFolderPath()).toBeNull();
    expect(createProject).toHaveBeenCalledWith({ roots: [{ path: "/repo" }] });
    expect(getProject).not.toHaveBeenCalled();
  });

  it("still opens a newly cloned project when extension setup is dismissed or confirmed", async () => {
    const cloned = wireProject("/repo", "p-cloned");
    const cloneProject = vi.fn(async () => cloned);
    const getProject = vi.fn(async () => cloned);
    const opening = setup(
      async () => ({
        path: "/repo",
        project_id: "p-cloned",
        extension_suggestions: {
          project_id: "p-cloned",
          suggestion_revision: "rev-1",
          suggestions: [
            {
              id: "acme/pack",
              source: "https://example.com/acme/pack.git",
              status: "available",
            },
          ],
        },
      }),
      { cloneProject, getProject },
    );

    await opening.submitClone({ url: "https://example.com/repo.git", parentDir: "/projects" });
    expect(opening.openFolderPath()).toBe("/repo");
    expect(opening.startSession).not.toHaveBeenCalled();

    opening.closeOpenFolder();
    expect(opening.openFolderPath()).toBeNull();
    expect(opening.startSession).toHaveBeenCalledWith(
      { kind: "new-session", projectId: "p-cloned" },
      { switchKind: "cross-project" },
    );

    opening.startSession.mockClear();
    await opening.submitClone({ url: "https://example.com/repo.git", parentDir: "/projects" });
    await opening.confirmOpenFolder({ installIds: [] });
    expect(getProject).toHaveBeenCalledWith("p-cloned");
    expect(opening.startSession).toHaveBeenCalledWith(
      { kind: "new-session", projectId: "p-cloned" },
      { switchKind: "cross-project" },
    );
  });

  it("covers the outgoing workspace before cloning and throughout the pending request", async () => {
    const pending = deferred<Project>();
    const observed: boolean[] = [];
    const cloneProject = vi.fn(() => {
      observed.push(opening.shell.state.opening !== null);
      return pending.promise;
    });
    const opening = setup(async () => ({ path: "/repo" }), { cloneProject });
    opening.shell.beginStageSwitch({ projectId: "outgoing", kind: "cross-project" });

    const request = opening.submitClone({ url: "https://example.com/repo.git", parentDir: "/projects" });
    expect(observed).toEqual([true]);
    expect(opening.shell.state.opening).not.toBeNull();
    expect(opening.cloneBusy()).toBe(true);
    expect(opening.startSession).not.toHaveBeenCalled();

    pending.resolve(wireProject("/repo", "p1"));
    await request;
    expect(opening.cloneBusy()).toBe(false);
    expect(opening.startSession).toHaveBeenCalledTimes(1);
  });

  it("only opens the newest request even when the same folder is selected twice", async () => {
    const first = deferred<FolderDetect>();
    const second = deferred<FolderDetect>();
    const detect = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const opening = setup(detect);
    const old = opening.proposeFolderAsProject("/repo");
    const current = opening.proposeFolderAsProject("/repo");
    first.resolve({ path: "/repo" });
    await old;
    expect(opening.client.createProject).not.toHaveBeenCalled();
    second.resolve({ path: "/repo" });
    await current;
    expect(opening.client.createProject).toHaveBeenCalledTimes(1);
    expect(opening.startSession).toHaveBeenCalledTimes(1);
  });
  it("does not clear a newer folder's identity when old project creation finishes", async () => {
    const oldProject = deferred<Project>();
    const newProject = deferred<Project>();
    const createProject = vi.fn().mockReturnValueOnce(oldProject.promise).mockReturnValueOnce(newProject.promise);
    const detect = vi.fn().mockResolvedValueOnce({ path: "/old" }).mockResolvedValueOnce({ path: "/new" });
    const opening = setup(detect, { createProject });
    const old = opening.proposeFolderAsProject("/old");
    await vi.waitFor(() => expect(createProject).toHaveBeenCalledTimes(1));
    const current = opening.proposeFolderAsProject("/new");
    await vi.waitFor(() => expect(createProject).toHaveBeenCalledTimes(2));
    oldProject.resolve(wireProject("/old", "old"));
    await old;
    expect(opening.openFolderDetect()?.path).toBe("/new");
    expect(opening.startSession).not.toHaveBeenCalled();
    newProject.resolve(wireProject("/new", "new"));
    await current;
    expect(opening.startSession).toHaveBeenCalledTimes(1);
  });

  it("does not open a project after its controller is disposed", async () => {
    const pending = deferred<FolderDetect>();
    const opening = setup(() => pending.promise);
    const request = opening.proposeFolderAsProject("/repo");
    opening.dispose();
    pending.resolve({ path: "/repo" });
    await request;
    expect(opening.client.createProject).not.toHaveBeenCalled();
    expect(opening.startSession).not.toHaveBeenCalled();
  });
});
