import { createRoot } from "solid-js";
import { expect, it, onTestFinished, vi } from "vitest";
import type { WorkflowSummary } from "../../api/types.ts";
import { demoSession } from "../../chat/test/session-msw-fixtures.ts";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createChatWorkflowState } from "./chat-workflow-state.ts";

it("keeps an Ask chat from arming or starting a workflow that requires Agent", async () => {
  const appStore = createAppStore();
  const session = { ...demoSession, mode: "ask" as const };
  const workflow: WorkflowSummary = {
    id: "plan", version: "1.0.0", name: "Plan", trigger: "/plan", requires_agent: true,
  };
  appStore.actions.setCurrentSession(session);
  appStore.actions.setWorkflowState(session.id, appStore.state.sessionViewEpoch, {
    activeWorkflowRun: undefined,
    workflowRuns: [],
    workflowCatalog: [workflow],
    blueprints: [],
  });
  const clientOrThrow = vi.fn(() => stubClient());
  const state = createRoot((dispose) => {
    onTestFinished(dispose);
    return createChatWorkflowState({
      appStore, clientOrThrow, projects: [],
      sessionId: () => session.id,
      projectDir: () => session.workspace_path ?? "",
      catalogRun: () => undefined,
      workflowsOpen: () => true,
      setWorkflowsOpen: vi.fn(),
      scrollToRun: vi.fn(),
    });
  });

  state.armWorkflow(workflow);
  expect(state.armed()).toBeNull();
  expect(state.error()).toBe("This workflow needs Agent.");

  state.startProposalWorkflow({ workflow_id: workflow.id, workflow_version: "1.0.0" });
  await vi.waitFor(() => expect(state.busy()).toBe(false));
  expect(state.error()).toBe("This workflow needs Agent.");
  expect(clientOrThrow).not.toHaveBeenCalled();
});
