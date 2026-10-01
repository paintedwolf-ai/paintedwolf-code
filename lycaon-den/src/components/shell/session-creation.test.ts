// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { afterEach, assert, describe, expect, it, vi } from "vitest";
import type { Project } from "../../api/types.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { shellSessionSwitchGeneration } from "../../chat/session/session-switch.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createSessionCreation } from "./session-creation.ts";

const connection = vi.hoisted(() => ({ getLycaonClient: vi.fn() }));
vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/connection/app-connection.ts")>(),
  ...connection,
}));

const disposals: (() => void)[] = [];
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose();
  shellSessionSwitchGeneration.next();
  vi.clearAllMocks();
});

function setup(homeDraft: boolean) {
  return createRoot((dispose) => {
    disposals.push(dispose);
    const shell = createShellStore("connected");
    if (homeDraft) shell.beginDraftMaterialization();
    else shell.beginStageSwitch({ projectId: "outgoing", kind: "cross-project" });
    let resolveProject!: (project: Project) => void;
    const pending = new Promise<Project>((resolve) => { resolveProject = resolve; });
    const observed: { opening: boolean; materializingDraft: boolean }[] = [];
    const createProject = vi.fn(() => {
      observed.push({
        opening: shell.state.opening !== null,
        materializingDraft: shell.state.materializingDraft,
      });
      return pending;
    });
    const client = stubClient({ createProject });
    connection.getLycaonClient.mockReturnValue(client);
    const projects = createProjectsStore(() => client, { onProjectEvent: () => () => {} });
    const sessionSwitchDeps = vi.fn(() => assert.fail("Superseded creation reached session switching"));
    const start = createSessionCreation({
      shell,
      appStore: createAppStore(),
      projects,
      recents: createRecentsStore(),
      showProjects: vi.fn(),
      beginWorkspaceOpening: (label) => shell.beginWorkspaceOpening({ label }),
      conversationInLayout: () => true,
      sessionSwitchDeps,
    });
    return { shell, start, observed, resolveProject, createProject, projects, sessionSwitchDeps };
  });
}

describe("session creation presentation", () => {
  it("covers the outgoing workspace before creating a new project", async () => {
    const creation = setup(false);
    const request = creation.start({ kind: "new-project" });
    expect(creation.createProject).toHaveBeenCalledTimes(1);
    expect(creation.observed).toEqual([{ opening: true, materializingDraft: false }]);
    expect(creation.shell.state.opening).not.toBeNull();
    expect(creation.sessionSwitchDeps).not.toHaveBeenCalled();

    shellSessionSwitchGeneration.next();
    creation.resolveProject(wireProject("/repo", "p1"));
    await request;
    expect(creation.projects.byId("p1")).toBeUndefined();
    expect(creation.sessionSwitchDeps).not.toHaveBeenCalled();
  });

  it("keeps a submitted Home idea visible while its project materializes", async () => {
    const creation = setup(true);
    const request = creation.start({ kind: "new-project" });
    expect(creation.observed).toEqual([{ opening: false, materializingDraft: true }]);
    expect(creation.shell.state.materializingDraft).toBe(true);
    expect(creation.shell.state.opening).toBeNull();

    shellSessionSwitchGeneration.next();
    creation.resolveProject(wireProject("/repo", "p1"));
    await request;
    expect(creation.sessionSwitchDeps).not.toHaveBeenCalled();
  });
});
