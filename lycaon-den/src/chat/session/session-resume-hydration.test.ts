import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { resumeChatSession } from "./session-lifecycle.ts";
import { clearSessionChatCacheForTests } from "./session-chat-cache.ts";
import { buildChatTranscriptBlocks } from "../workflow/workflow-spans.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";
import { withBootstrap, stalledRecents } from "./session-lifecycle-test-fixture.ts";

describe("resumeChatSession hydration", () => {
  it("resumeChatSession hydrates runs referenced on transcript rows", async () => {
    const appStore = createAppStore();
    const terminalRun = {
      id: "run-1",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "complete" as const,
      current_phase: "boot",
      created_at: "t",
      updated_at: "t",
    };
    const messages = [
      {
        id: "m1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        workflow_run_id: "run-1",
        created_at: "t",
      },
    ];
    const client = withBootstrap({
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      getWorkflowRun: vi.fn().mockResolvedValue(terminalRun),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(client.getWorkflowRun).toHaveBeenCalledWith("run-1");
    expect(appStore.state.workflowRuns).toEqual([terminalRun]);
    expect(() =>
      buildChatTranscriptBlocks(
        appStore.state.messages,
        appStore.state.workflowRuns,
        appStore.state.activeWorkflowRun,
      ),
    ).not.toThrow();
  });

  it("resumeChatSession installs workflow state before messages without redundant checkpoint IO", async () => {
    const appStore = createAppStore();
    const ambientRun = {
      id: "run-ambient",
      session_id: "sess-1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create" as const,
      status: "running" as const,
      current_phase: "boot",
      created_at: "t",
      updated_at: "t",
    };
    const messages = [
      {
        id: "m1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary" as const,
        visibility: "internal" as const,
        workflow_run_id: "run-ambient",
        created_at: "t",
      },
    ];
    const callOrder: string[] = [];
    const client = withBootstrap({
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi
        .fn()
        .mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockImplementation(async () => {
        callOrder.push("checkpoints");
        return [];
      }),
      getActiveWorkflowRun: vi.fn().mockImplementation(async () => {
        callOrder.push("activeRun");
        return ambientRun;
      }),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [ambientRun] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });
    const originalInstall = appStore.actions.installTranscriptBaseline.bind(
      appStore.actions,
    );
    const installSpy = vi.spyOn(appStore.actions, "installTranscriptBaseline");
    installSpy.mockImplementation((sessionId, next, watermark) => {
      callOrder.push("installBaseline");
      expect(() =>
        buildChatTranscriptBlocks(
          next,
          appStore.state.workflowRuns,
          appStore.state.activeWorkflowRun,
        ),
      ).not.toThrow();
      originalInstall(sessionId, next, watermark);
    });

    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(callOrder.indexOf("activeRun")).toBeLessThan(
      callOrder.indexOf("installBaseline"),
    );
    expect(client.listCheckpoints).not.toHaveBeenCalled();
    installSpy.mockRestore();
  });

  it("resumeChatSession opens a session whose workflow runs are missing", async () => {
    const appStore = createAppStore();
    const messages = [
      {
        id: "m1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "hello",
        created_at: "t",
      },
    ];
    const client = withBootstrap({
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue({ messages, watermark: 0, turn_clocks: {}, turn_loads: {} }),
      listCheckpoints: vi.fn().mockResolvedValue([]),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });

    // A missing run is an enrichment gap; the rows still open.
    await resumeChatSession(appStore, client, "sess-1", emptyProjects);

    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
    const blocks = buildChatTranscriptBlocks(
      appStore.state.messages,
      appStore.state.workflowRuns,
      appStore.state.activeWorkflowRun,
    );
    expect(blocks.flatMap((b) => b.items)).not.toHaveLength(0);
  });
});

describe("resumeChatSession recents and cancellation", () => {
  it("applies the transcript without waiting for the recents write and passes the switch signal", async () => {
    clearSessionChatCacheForTests();
    const appStore = createAppStore();
    const recents = stalledRecents();
    const client = withBootstrap({
      getSession: vi.fn().mockResolvedValue({
        id: "sess-1",
        title: "Named chat",
        owner_person_id: "00000000-0000-4000-8000-000000000002",
        project_id: "proj-1",
        workspace_path: "/tmp/p",
        posture: "build",
        status: "idle",
        created_at: "t",
        activity_at: "t",
        updated_at: "t",
      }),
      listSessionMessages: vi.fn().mockResolvedValue({
        messages: [
          {
            id: "m1",
            role: "user", origin: "user", authority: "user", trust_tier: "trusted",
            content: "hello",
            created_at: "t",
          },
        ],
        watermark: 0,
        turn_clocks: {},
        turn_loads: {},
      }),
      getActiveWorkflowRun: vi.fn().mockResolvedValue(null),
      listSessionWorkflowRuns: vi.fn().mockResolvedValue({ runs: [] }),
      listWorkflows: vi.fn().mockResolvedValue([]),
      listBlueprints: vi.fn().mockResolvedValue([]),
    });
    const signal = new AbortController().signal;

    await resumeChatSession(appStore, client, "sess-1", emptyProjects, recents, {
      projectId: "proj-1",
      signal,
    });

    expect(recents.registerSession).toHaveBeenCalledWith({
      projectId: "proj-1",
      sessionId: "sess-1",
      title: "Named chat",
    });
    expect(client.getSessionBootstrap).toHaveBeenCalledWith("sess-1", { signal });
    expect(appStore.state.messages.map((m) => m.id)).toEqual(["m1"]);
  });
});
