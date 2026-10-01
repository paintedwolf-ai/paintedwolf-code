import { render } from "@solidjs/testing-library";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createLycaonClient } from "../../api/client-impl.ts";
import type { Message } from "../../api/types.ts";
import { mswServer } from "../../api/mocks/setup.ts";
import { ChatSpanBlocks } from "../../components/transcript/transcript-viewport-test-harness.tsx";
import { createAppStore } from "../../store/app-state.ts";
import {
  applyMessageEvent,
  completeChatHydrationAndReplay,
} from "../transcript/projection/message-events.ts";
import { clearChildMessageBuffer } from "../transcript/projection/worker-child-message-buffer.ts";
import { clearSessionChatCacheForTests } from "./session-chat-cache.ts";
import { resumeChatSession } from "./session-lifecycle.ts";
import { reconcileActiveScope } from "./session-reconcile.ts";
import {
  ambientImplementRun,
  demoSession,
  MSW_API_BASE,
  sessionTranscriptHandlers,
} from "../test/session-msw-fixtures.ts";
import {
  assertChatTranscriptRenderable,
} from "../test/transcript-render-assert.ts";
import {
  createSessionSwitchGeneration,
  runCreateSession,
  runResumeSession,
  type SessionSwitchDeps,
} from "./session-switch.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";

const connection = { baseUrl: MSW_API_BASE, apiToken: "msw-tok" };

const coordinatorTranscript: Message[] = [
  {
    id: "m-boundary",
    role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
    content: "",
    kind: "workflow_boundary",
    visibility: "internal",
    workflow_run_id: ambientImplementRun.id,
    created_at: "2025-01-01T00:00:01Z",
    seq: 1,
  },
  {
    id: "m-user",
    role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
    content: "Build a tic-tac-toe game",
    workflow_run_id: ambientImplementRun.id,
    created_at: "2025-01-01T00:00:02Z",
    seq: 2,
  },
  {
    id: "m-reply",
    role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
    content: "I'll delegate that to a worker.",
    workflow_run_id: ambientImplementRun.id,
    created_at: "2025-01-01T00:00:03Z",
    seq: 3,
  },
];

function shellSwitchDeps(appStore: ReturnType<typeof createAppStore>): SessionSwitchDeps {
  return {
    returnToHome: vi.fn(),
    clearChatForSessionSwitch: (lock) =>
      appStore.actions.clearChatForSessionSwitch(lock),
    beginSessionResumeSwitch: (lock) =>
      appStore.actions.beginSessionResumeSwitch(lock),
    // Hydration completion replays buffered SSE.
    completeChatSessionHydration: () => completeChatHydrationAndReplay(appStore),
    resetChat: () => appStore.actions.resetChatForSessionSwitch(),
    reportError: () => {},
    // Foreground binding routes prior-session replay away.
    openSession: (scope) =>
      appStore.actions.setCurrentSession({
        ...demoSession,
        id: scope.sessionId,
        project_id: scope.projectId,
      }),
    snapshotSessionChat: () => undefined,
    restoreCachedSessionChat: () => false,
  };
}

describe("session transcript hydrate integration", () => {
  beforeAll(() => mswServer.listen({ onUnhandledRequest: "error" }));
  // Clear process-wide buffers between cases.
  afterEach(() => {
    mswServer.resetHandlers();
    clearChildMessageBuffer();
    clearSessionChatCacheForTests();
  });
  afterAll(() => mswServer.close());

  it("renders rows that arrived ahead of their workflow run", () => {
    const blocks = assertChatTranscriptRenderable(
      [{ id: "m1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hello", workflow_run_id: ambientImplementRun.id, created_at: "t" }],
      [],
      undefined,
    );
    expect(blocks).toHaveLength(1);
    expect(blocks[0]?.items).not.toHaveLength(0);
    assertChatTranscriptRenderable([], [ambientImplementRun], ambientImplementRun);
  });

  it("runResumeSession blocks coordinator SSE during hydrate, then renders server transcript", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers(coordinatorTranscript, [ambientImplementRun]),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();
    appStore.actions.setCurrentSession({
      ...demoSession,
      status: "busy",
    });
    appStore.actions.installTranscriptBaseline(
      "sess-prev",
      [{ id: "stale", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "old turn", workflow_run_id: ambientImplementRun.id, created_at: "t", seq: 1 }],
      1,
    );
    appStore.actions.setWorkflowState(demoSession.id, appStore.state.sessionViewEpoch, {
      activeWorkflowRun: ambientImplementRun,
      workflowRuns: [ambientImplementRun],
      workflowCatalog: [],
      blueprints: [],
    });

    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "same-project",
      scope: {
        projectId: demoSession.project_id!,
        sessionId: demoSession.id,
      },
      deps: shellSwitchDeps(appStore),
      hasClient: true,
      hydrate: async () => {
        expect(appStore.state.chatHydrationLock).toBe(demoSession.id);
        // Replay rejects seq 2 below the refetched seq 3 baseline.
        expect(
          applyMessageEvent(appStore, {
            session_id: demoSession.id,
            op: "append",
            seq: 2,
            message: {
              id: "sse-mid-hydrate",
              role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
              content: "partial coordinator stream",
              workflow_run_id: ambientImplementRun.id,
              created_at: "2025-01-01T00:00:04Z",
              seq: 2,
            },
          }),
        ).toBe(false);
        // The outgoing transcript remains renderable while hydration blocks SSE.
        expect(appStore.state.messages.map((m) => m.id)).toEqual(["stale"]);
        assertChatTranscriptRenderable(
          appStore.state.messages,
          appStore.state.workflowRuns,
          appStore.state.activeWorkflowRun,
        );

        await resumeChatSession(appStore, client, demoSession.id, emptyProjects);
      },
    });

    expect(appStore.state.chatHydrationLock).toBeUndefined();
    expect(appStore.state.messages.map((m) => m.id)).toEqual([
      "m-boundary",
      "m-user",
      "m-reply",
    ]);

    const blocks = assertChatTranscriptRenderable(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
    expect(() =>
      render(() => (
        <ChatSpanBlocks
          blocks={blocks}
          messages={appStore.state.messages}
          sessionId={demoSession.id}
          workers={[]}
          visibleTurnActive={false}
        />
      )),
    ).not.toThrow();
  });

  it("cross-project runResumeSession clears stale transcript before hydrate", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers(coordinatorTranscript, [ambientImplementRun]),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();
    appStore.actions.installTranscriptBaseline(
      "sess-other-project",
      [{ id: "wrong-repo", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "other project transcript", created_at: "t", seq: 1 }],
      1,
    );
    appStore.actions.setWorkflowState("sess-other-project", appStore.state.sessionViewEpoch, {
      activeWorkflowRun: ambientImplementRun,
      workflowRuns: [ambientImplementRun],
      workflowCatalog: [],
      blueprints: [],
    });

    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "cross-project",
      scope: {
        projectId: demoSession.project_id!,
        sessionId: demoSession.id,
      },
      deps: shellSwitchDeps(appStore),
      hasClient: true,
      hydrate: async () => {
        expect(appStore.state.messages).toEqual([]);
        await resumeChatSession(appStore, client, demoSession.id, emptyProjects);
      },
    });

    expect(appStore.state.messages.map((m) => m.id)).toEqual([
      "m-boundary",
      "m-user",
      "m-reply",
    ]);
  });

  it("reconcileActiveScope refreshes workflow before messages (reconnect path)", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers(coordinatorTranscript, [ambientImplementRun]),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(demoSession);
    appStore.actions.installTranscriptBaseline(
      demoSession.id,
      [{ id: "stale", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "cached", created_at: "t", seq: 1 }],
      1,
    );
    appStore.actions.setWorkflowState(demoSession.id, appStore.state.sessionViewEpoch, {
      activeWorkflowRun: ambientImplementRun,
      workflowRuns: [ambientImplementRun],
      workflowCatalog: [],
      blueprints: [],
    });

    const callOrder: string[] = [];
    const origWorkflow = appStore.actions.setWorkflowState.bind(appStore.actions);
    const origInstall = appStore.actions.installTranscriptBaseline.bind(
      appStore.actions,
    );
    vi.spyOn(appStore.actions, "setWorkflowState").mockImplementation((sessionId, epoch, input) => {
      callOrder.push("setWorkflowState");
      origWorkflow(sessionId, epoch, input);
    });
    vi.spyOn(appStore.actions, "installTranscriptBaseline").mockImplementation(
      (sessionId, msgs, watermark) => {
        callOrder.push("installBaseline");
        expect(appStore.state.workflowRuns.length).toBeGreaterThan(0);
        origInstall(sessionId, msgs, watermark);
      },
    );

    await reconcileActiveScope(appStore, client, emptyProjects);

    expect(callOrder.indexOf("setWorkflowState")).toBeLessThan(
      callOrder.indexOf("installBaseline"),
    );
    expect(appStore.state.workflowRuns).toEqual([ambientImplementRun]);
    expect(appStore.state.messages.map((m) => m.id)).toEqual([
      "m-boundary",
      "m-user",
      "m-reply",
    ]);
    assertChatTranscriptRenderable(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
  });

  it("create-session wildcard lock blocks parent SSE until bind completes", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(demoSession);
    appStore.actions.installTranscriptBaseline(
      demoSession.id,
      [{ id: "live", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "in flight", created_at: "t", seq: 1 }],
      1,
    );
    appStore.actions.setWorkflowState(demoSession.id, appStore.state.sessionViewEpoch, {
      activeWorkflowRun: ambientImplementRun,
      workflowRuns: [ambientImplementRun],
      workflowCatalog: [],
      blueprints: [],
    });

    await runCreateSession({
      generation: createSessionSwitchGeneration(),
      projectId: demoSession.project_id!,
      deps: shellSwitchDeps(appStore),
      hasClient: true,
      create: async () => {
        expect(appStore.state.chatHydrationLock).toBe("*");
        expect(
          applyMessageEvent(appStore, {
            session_id: demoSession.id,
            op: "append",
            message: {
              id: "sse-during-create",
              role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
              content: "should not land",
              created_at: "t2",
            },
          }),
        ).toBe(false);
        return {
          projectId: demoSession.project_id!,
          sessionId: "sess-new",
        };
      },
      enrich: async () => undefined,
    });

    expect(appStore.state.chatHydrationLock).toBeUndefined();
    expect(appStore.state.messages).toHaveLength(0);
  });

  it("live SSE applies again after hydration completes", async () => {
    mswServer.use(
      ...sessionTranscriptHandlers([], [ambientImplementRun]),
    );
    const client = createLycaonClient(connection);
    const appStore = createAppStore();

    await runResumeSession({
      generation: createSessionSwitchGeneration(),
      kind: "same-project",
      scope: {
        projectId: demoSession.project_id!,
        sessionId: demoSession.id,
      },
      deps: shellSwitchDeps(appStore),
      hasClient: true,
      hydrate: () => resumeChatSession(appStore, client, demoSession.id, emptyProjects),
    });

    appStore.actions.setCurrentSession(demoSession);
    expect(
      applyMessageEvent(appStore, {
        session_id: demoSession.id,
        op: "append",
        message: {
          id: "m-live",
          role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
          content: "follow-up",
          workflow_run_id: ambientImplementRun.id,
          created_at: "t",
        },
      }),
    ).toBe(true);

    assertChatTranscriptRenderable(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
  });
});
