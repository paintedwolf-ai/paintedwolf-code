import { describe, expect, it } from "vitest";
import type { WorkflowRun } from "../api/types.ts";
import {
  catalogWorkflowRun,
  isAmbientRootRun,
  isSessionCreateAttachRun,
  latestAmbientRootRun,
  resolveCatalogRun,
  resolveLeafRun,
  runsByIdMap,
} from "./workflow-run-stack.ts";

const parentPlan: WorkflowRun = {
  id: "run-plan",
  session_id: "s1",
  workflow_id: "plan",
  workflow_version: "1.0.0",
	revision: 1,
  status: "paused_on_child",
  current_phase: "build",
  created_at: "t",
  updated_at: "t",
};

const childImplement: WorkflowRun = {
  id: "run-child",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "boot",
  parent_run_id: "run-plan",
  created_at: "t",
  updated_at: "t",
};

describe("workflow-run-stack", () => {
  it("resolveCatalogRun returns catalog parent while child is leaf", () => {
    const byId = runsByIdMap([parentPlan, childImplement]);
    expect(resolveCatalogRun(childImplement, byId)?.id).toBe("run-plan");
    expect(resolveCatalogRun(parentPlan, byId)?.id).toBe("run-plan");
  });

  it("resolveCatalogRun is undefined for ambient-only implement", () => {
    const ambient: WorkflowRun = {
      ...childImplement,
      id: "run-amb",
      parent_run_id: undefined,
      attach_policy: "session_create",
    };
    expect(resolveCatalogRun(ambient, runsByIdMap([ambient]))).toBeUndefined();
  });

  it("resolveCatalogRun hides session_create child when parent is not hydrated", () => {
    expect(
      resolveCatalogRun(childImplement, runsByIdMap([childImplement])),
    ).toBeUndefined();
  });

  it("resolveCatalogRun returns catalog child nested under ambient parent", () => {
    const ambient: WorkflowRun = {
      id: "run-amb",
      session_id: "s1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create",
      status: "paused_on_child",
      current_phase: "work",
      created_at: "t",
      updated_at: "t",
    };
    const childPlan: WorkflowRun = {
      ...parentPlan,
      id: "run-plan-child",
      parent_run_id: "run-amb",
      status: "running",
    };
    expect(
      resolveCatalogRun(childPlan, runsByIdMap([ambient, childPlan]))?.id,
    ).toBe("run-plan-child");
  });

  it("resolveLeafRun prefers active poll over runs list", () => {
    expect(resolveLeafRun(childImplement, [parentPlan])).toBe(childImplement);
  });

  it("resolveLeafRun returns completed root implement when session is idle", () => {
    const idleAmbient: WorkflowRun = {
      id: "run-ambient",
      session_id: "s1",
      workflow_id: "implement",
      workflow_version: "1.0.0",
	  revision: 1,
      attach_policy: "session_create",
      status: "complete",
      current_phase: "boot",
      created_at: "t",
      updated_at: "t",
    };
    expect(resolveLeafRun(undefined, [idleAmbient])).toBe(idleAmbient);
  });

  it("resolveLeafRun chooses the newest run independent of API ordering", () => {
	const older = { ...parentPlan, id: "run-older", created_at: "2026-01-01T00:00:00Z" };
	const newer = { ...parentPlan, id: "run-newer", created_at: "2026-01-02T00:00:00Z" };
	expect(resolveLeafRun(undefined, [newer, older])?.id).toBe("run-newer");
	expect(resolveLeafRun(undefined, [older, newer])?.id).toBe("run-newer");
  });

  it("catalogWorkflowRun returns parent during subroutine child", () => {
    expect(
      catalogWorkflowRun(childImplement, [parentPlan, childImplement])?.id,
    ).toBe("run-plan");
  });

  it("catalogWorkflowRun is undefined for ambient-only implement", () => {
    const ambient: WorkflowRun = {
      ...childImplement,
      id: "run-amb",
      parent_run_id: undefined,
      attach_policy: "session_create",
    };
    expect(catalogWorkflowRun(ambient, [ambient])).toBeUndefined();
  });

  it("keeps a retired workflow visible independently of the start catalog", () => {
    const retired = { ...parentPlan, workflow_id: "security-survey", workflow_version: "1.0.0" };
    expect(catalogWorkflowRun(retired, [retired])).toBe(retired);
  });

  // session_create identifies the ambient recipe independently of workflow names.
  it("resolves the ambient run from attach_policy, never from a workflow id", () => {
    const renamedAmbient: WorkflowRun = {
      ...childImplement,
      id: "run-amb",
      workflow_id: "build",
      parent_run_id: undefined,
      status: "complete",
    };
    const decoy: WorkflowRun = {
      ...parentPlan,
      id: "run-decoy",
      workflow_id: "implement",
      status: "running",
      created_at: "2027-01-01T00:00:00Z",
    };

    expect(latestAmbientRootRun([renamedAmbient, decoy])?.id).toBe("run-amb");
    expect(resolveLeafRun(undefined, [decoy, renamedAmbient])?.id).toBe("run-amb");
    // A live ambient child still outranks the idle ambient root.
    const liveChild: WorkflowRun = { ...childImplement, workflow_id: "build" };
    expect(resolveLeafRun(undefined, [renamedAmbient, liveChild])?.id).toBe("run-child");
  });

  it("isAmbientRootRun requires session_create attach policy and no parent", () => {
    const ambient: WorkflowRun = {
      ...childImplement,
      id: "run-amb",
      parent_run_id: undefined,
      attach_policy: "session_create",
    };
    expect(isSessionCreateAttachRun(ambient)).toBe(true);
    expect(isAmbientRootRun(ambient)).toBe(true);
    expect(isAmbientRootRun(childImplement)).toBe(false);
    expect(isAmbientRootRun(parentPlan)).toBe(false);
    expect(
      isAmbientRootRun({
        ...ambient,
        parent_run_id: "run-plan",
      }),
    ).toBe(false);
  });
});
