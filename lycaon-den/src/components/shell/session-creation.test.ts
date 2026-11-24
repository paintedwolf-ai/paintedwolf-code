// @vitest-environment jsdom
import { createEffect, createRoot } from "solid-js";
import { afterEach, assert, describe, expect, it, vi } from "vitest";
import type { Project, SessionMode, WorkflowSummary } from "../../api/types.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import { shellSessionSwitchGeneration, type SessionSwitchDeps } from "../../chat/session/session-switch.ts";
import { captureInitialSessionMode, sessionModeView } from "../../chat/session/session-mode-state.ts";
import { demoSession } from "../../chat/test/session-msw-fixtures.ts";
import { clearPendingArmWorkflow, peekArmWorkflow } from "../../chat/workflow/pending-arm.ts";
import { syncChatPrefsFromSnapshot } from "../../settings/chat/chat-prefs.ts";
import { resetAppStateSnapshotForTests, setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createProjectsStore } from "../../store/projects-store.ts";
import { createRecentsStore } from "../../store/recents-store.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createSessionCreation } from "./session-creation.ts";

const connection = vi.hoisted(() => ({
  getLycaonClient: vi.fn(),
  projectEventsAreSubscribed: vi.fn(() => true),
  subscribeProjectEvents: vi.fn(),
  waitForProjectEvents: vi.fn(async () => true),
}));
const sendPrompt = vi.hoisted(() =>
  vi.fn<typeof import("../../chat/session/session-lifecycle.ts").sendChatPrompt>(async () => undefined),
);
vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/connection/app-connection.ts")>(),
  ...connection,
}));
vi.mock("../../chat/session/session-lifecycle.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../chat/session/session-lifecycle.ts")>(),
  enrichCreatedSession: vi.fn(async () => undefined),
  refreshCreatedSessionBoard: vi.fn(async () => undefined),
  sendChatPrompt: sendPrompt,
}));

const disposals: (() => void)[] = [];
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose();
  shellSessionSwitchGeneration.next();
  clearPendingArmWorkflow();
  resetAppStateSnapshotForTests();
  syncChatPrefsFromSnapshot();
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

function setupModeEntry(lastStartedMode: SessionMode, homeDraft = false) {
  setAppStateSnapshot({ ...EMPTY_APP_STATE_V1, chat: { lastStartedMode } });
  syncChatPrefsFromSnapshot();
  return createRoot((dispose) => {
    disposals.push(dispose);
    const shell = createShellStore("connected");
    if (homeDraft) shell.beginDraftMaterialization();
    const appStore = createAppStore();
    const project = wireProject("/repo", "p1");
    const session = { ...demoSession, project_id: project.id, started: false };
    const createSession = vi.fn(async () => session);
    const client = stubClient({ createSession, createProject: vi.fn(async () => project) });
    connection.getLycaonClient.mockReturnValue(client);
    const projects = createProjectsStore(() => client, { onProjectEvent: () => () => {} });
    projects.upsert(project);
    createEffect(() => {
      if (appStore.state.currentSession?.id === session.id) captureInitialSessionMode(appStore, session.id);
    });
    const observedModes: (SessionMode | undefined)[] = [];
    const openSession: SessionSwitchDeps["openSession"] = (scope) => {
      if (scope.sessionId === session.id) observedModes.push(sessionModeView(appStore, session.id).mode);
    };
    const deps: SessionSwitchDeps = {
      returnToHome: vi.fn(),
      clearChatForSessionSwitch: vi.fn(),
      beginSessionResumeSwitch: vi.fn(),
      completeChatSessionHydration: vi.fn(),
      resetChat: vi.fn(),
      reportError: vi.fn(),
      openSession,
      snapshotSessionChat: vi.fn(),
      restoreCachedSessionChat: vi.fn(() => false),
    };
    const start = createSessionCreation({
      shell, appStore, projects, recents: createRecentsStore(),
      showProjects: vi.fn(),
      beginWorkspaceOpening: (label) => shell.beginWorkspaceOpening({ label }),
      conversationInLayout: () => true,
      sessionSwitchDeps: () => deps,
    });
    return { start, observedModes, createSession, project, session, reportError: deps.reportError };
  });
}

describe("session mode at shell entry", () => {
  it("carries the Home Ask selection into creation, reveal, and first send", async () => {
    const { start, observedModes, createSession, project, session, reportError } = setupModeEntry("agent", true);

    expect(await start({ kind: "new-project" }, { mode: "ask", prompt: "What is in this repository?" })).toBe(true);

    expect(createSession).toHaveBeenCalledWith({ mode: "ask", posture: "build", project_id: project.id });
    expect(observedModes).toEqual(["ask"]);
    expect(sendPrompt).toHaveBeenCalledOnce();
    const sendArgs = sendPrompt.mock.calls[0];
    expect(sendArgs?.[3]).toBe(session.id);
    expect(sendArgs?.[7]).toBe("What is in this repository?");
    expect(sendArgs?.[11]).toMatchObject({ mode: "ask" });
    expect(reportError).not.toHaveBeenCalled();
  });

  it("reveals Agent on a pre-armed supporting workflow when the last chat used Ask", async () => {
    const { start, observedModes, createSession, project, session, reportError } = setupModeEntry("ask");
    const workflow: WorkflowSummary = {
      id: "plan", version: "1.0.0", name: "Plan", trigger: "/plan", requires_agent: true,
    };

    await start({ kind: "new-session", projectId: project.id }, { armWorkflow: workflow });

    expect(createSession).toHaveBeenCalledWith({ posture: "build", project_id: project.id });
    expect(observedModes).toEqual(["agent"]);
    expect(peekArmWorkflow(session.id)).toEqual(workflow);
    expect(sendPrompt).not.toHaveBeenCalled();
    expect(reportError).not.toHaveBeenCalled();
  });
});
