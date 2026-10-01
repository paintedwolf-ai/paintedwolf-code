import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createLycaonClient } from "../../api/client-impl.ts";
import type { Message } from "../../api/types.ts";
import { mswServer } from "../../api/mocks/setup.ts";
import { createAppStore } from "../../store/app-state.ts";
import { resumeChatSession } from "./session-lifecycle.ts";
import {
  ambientImplementRun,
  demoSession,
  MSW_API_BASE,
  sessionTranscriptHandlers,
  terminalImplementRun,
} from "../test/session-msw-fixtures.ts";
import {
  createSessionSwitchGeneration,
  runResumeSession,
  type SessionSwitchDeps,
} from "./session-switch.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";

const connection = { baseUrl: MSW_API_BASE, apiToken: "msw-tok" };

describe("session resume MSW integration", () => {
  beforeAll(() => mswServer.listen({ onUnhandledRequest: "error" }));
  afterEach(() => mswServer.resetHandlers());
  afterAll(() => mswServer.close());

  it("resumeChatSession succeeds for empty transcript with active ambient run", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers([], [ambientImplementRun]),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();
    await resumeChatSession(appStore, client, demoSession.id, emptyProjects);
    expect(appStore.state.messages).toEqual([]);
    expect(appStore.state.activeWorkflowRun?.id).toBe(ambientImplementRun.id);
  });

  it("resumeChatSession fetches terminal run referenced only on messages", async () => {
    const messages: Message[] = [
      {
        id: "m1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "done",
        workflow_run_id: terminalImplementRun.id,
        created_at: "t",
      },
    ];
    mswServer.use(
      ...sessionTranscriptHandlers(messages, [], { active: null }),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();
    await resumeChatSession(appStore, client, demoSession.id, emptyProjects);
    expect(appStore.state.workflowRuns.map((r) => r.id)).toContain(
      terminalImplementRun.id,
    );
  });

  it("resumeChatSession installs the transcript when no run resolves", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers(
        [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" }],
        [],
        { active: null },
      ),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();

    await resumeChatSession(appStore, client, demoSession.id, emptyProjects);

    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
  });

  it("a superseded switch hands the selection on and never hydrates", async () => {
    const gen = createSessionSwitchGeneration();
    let releaseSlow!: () => void;
    const slowGate = new Promise<void>((resolve) => {
      releaseSlow = resolve;
    });
    const opened: string[] = [];
    const hydrated: string[] = [];
    const deps: SessionSwitchDeps = {
      returnToHome: vi.fn(),
      clearChatForSessionSwitch: vi.fn(),
      beginSessionResumeSwitch: vi.fn(),
      completeChatSessionHydration: vi.fn(),
      resetChat: vi.fn(),
      reportError: vi.fn(),
      openSession: (scope) => {
        opened.push(scope.sessionId);
      },
      snapshotSessionChat: () => undefined,
      restoreCachedSessionChat: () => false,
    };
    const slow = runResumeSession({
      generation: gen,
      kind: "same-project",
      scope: { projectId: demoSession.project_id!, sessionId: "slow" },
      deps,
      hasClient: true,
      hydrate: async () => {
        hydrated.push("slow");
        await slowGate;
      },
    });
    await runResumeSession({
      generation: gen,
      kind: "same-project",
      scope: { projectId: demoSession.project_id!, sessionId: "fast" },
      deps,
      hasClient: true,
      hydrate: async () => {
        hydrated.push("fast");
      },
    });
    releaseSlow();
    await slow;
    // Each selection lands as it is made; only the latest hydrates and unlocks.
    expect(opened).toEqual(["slow", "fast"]);
    expect(hydrated).toEqual(["fast"]);
    expect(deps.completeChatSessionHydration).toHaveBeenCalledTimes(1);
  });
});
