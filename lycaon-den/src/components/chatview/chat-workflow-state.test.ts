import { createRoot } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createChatWorkflowState } from "./chat-workflow-state.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { emptyProjects } from "../../test/projects-fixture.ts";

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => null,
}));

describe("createChatWorkflowState", () => {
  it("derives start proposal from session ui DTO", () => {
    createRoot((dispose) => {
      const proposal = {
        workflow_id: "plan",
        workflow_version: "1.0.0",
      };
      const workflow = createChatWorkflowState({
        appStore: {
          state: {
            currentSession: { ui: { pending_workflow_start: proposal } },
            workflowCatalog: [],
          },
        } as unknown as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        catalogRun: () => null,
        workflowsOpen: () => false,
        setWorkflowsOpen: vi.fn(),
        scrollToRun: vi.fn(),
        projects: emptyProjects,
        clientOrThrow: () => {
          throw new Error("no client");
        },
      });
      expect(workflow.startProposal()).toEqual(proposal);
      dispose();
    });
  });

  it("clears start proposal after dismiss", () => {
    createRoot((dispose) => {
      const workflow = createChatWorkflowState({
        appStore: {
          state: {
            currentSession: {
              ui: { pending_workflow_start: { workflow_id: "plan" } },
            },
            workflowCatalog: [],
          },
        } as unknown as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        catalogRun: () => null,
        workflowsOpen: () => false,
        setWorkflowsOpen: vi.fn(),
        scrollToRun: vi.fn(),
        projects: emptyProjects,
        clientOrThrow: () => {
          throw new Error("no client");
        },
      });
      workflow.dismissProposal();
      expect(workflow.startProposal()).toBeNull();
      dispose();
    });
  });

  it("arms a catalog workflow without starting a run", () => {
    createRoot((dispose) => {
      const onArmed = vi.fn();
      const workflow = createChatWorkflowState({
        appStore: {
          state: {
            currentSession: null,
            workflowCatalog: [],
          },
        } as unknown as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        catalogRun: () => null,
        workflowsOpen: () => false,
        setWorkflowsOpen: vi.fn(),
        scrollToRun: vi.fn(),
        projects: emptyProjects,
        clientOrThrow: () => {
          throw new Error("no client");
        },
        onArmed,
      });
      workflow.armWorkflow({
        id: "plan",
        version: "1.0.0",
        name: "Plan",
        trigger: "/plan",
      });
      expect(workflow.armed()).toEqual({
        workflow_id: "plan",
        workflow_version: "1.0.0",
        trigger: "/plan",
        label: "Plan",
      });
      expect(onArmed).toHaveBeenCalledTimes(1);
      workflow.armWorkflow({
        id: "plan",
        version: "1.0.0",
        name: "Plan",
        trigger: "/plan",
      });
      expect(workflow.armed()).toBeNull();
      dispose();
    });
  });

  it("toggles the workflows tab from the session-launcher More path", () => {
    createRoot((dispose) => {
      let open = false;
      const setWorkflowsOpen = vi.fn((next: boolean) => {
        open = next;
      });
      const workflow = createChatWorkflowState({
        appStore: {
          state: {
            currentSession: null,
            workflowCatalog: [],
          },
        } as unknown as AppStore,
        sessionId: () => "sess-1",
        projectDir: () => "/repo",
        catalogRun: () => null,
        workflowsOpen: () => open,
        setWorkflowsOpen,
        scrollToRun: vi.fn(),
        projects: emptyProjects,
        clientOrThrow: () => {
          throw new Error("no client");
        },
      });
      workflow.toggleDrawerWithPicker();
      expect(setWorkflowsOpen).toHaveBeenLastCalledWith(true);
      expect(workflow.pickerOpen()).toBe(true);
      workflow.toggleDrawerWithPicker();
      expect(setWorkflowsOpen).toHaveBeenLastCalledWith(false);
      expect(workflow.pickerOpen()).toBe(false);
      dispose();
    });
  });

  it("does not clear arm for a catalog run belonging to another session", () => {
    createRoot((dispose) => {
      const catalogRun = () =>
        ({
          id: "run-old",
          session_id: "sess-old",
          workflow_id: "plan",
          workflow_version: "1.0.0",
          status: "running",
          current_phase: "intake",
        }) as const;
      const workflow = createChatWorkflowState({
        appStore: {
          state: {
            currentSession: null,
            workflowCatalog: [],
          },
        } as unknown as AppStore,
        sessionId: () => "sess-new",
        projectDir: () => "/repo",
        catalogRun: () => catalogRun() as never,
        workflowsOpen: () => false,
        setWorkflowsOpen: vi.fn(),
        scrollToRun: vi.fn(),
        projects: emptyProjects,
        clientOrThrow: () => {
          throw new Error("no client");
        },
      });
      workflow.setArmed({
        workflow_id: "plan",
        workflow_version: "1.0.0",
        trigger: "/plan",
        label: "Plan",
      });
      expect(workflow.armed()?.workflow_id).toBe("plan");
      dispose();
    });
  });
});
