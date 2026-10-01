import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import {
  findController,
  findEverywhereSeed,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { createAppStore } from "../store/app-state.ts";
import { WorkerTranscript } from "../components/worker/WorkerTranscript.tsx";
import type { WorkerTask } from "../api/types.ts";

afterEach(() => {
  resetFindControllerForTests();
  document.body.replaceChildren();
});

function completeWorker(id = "job-1"): WorkerTask {
  return {
    id,
    agent_type: "implement",
    status: "complete",
    brief: "WORKER_FIND_NEEDLE_TASK",
    created_at: "2026-01-01T00:00:00Z",
  };
}

describe("worker transcript findable adapter", () => {
  it("registers the worker pane and finds assignment text", async () => {
    const appStore = createAppStore();
    appStore.actions.applyWorkerTranscriptRows("job-1", [
      {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "WORKER_FIND_NEEDLE_ACTIVITY",
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);

    render(() => (
      <WorkerTranscript
        workerId="job-1"
        workers={[completeWorker()]}
        appStore={appStore}
        projectDir="/tmp/p"
      />
    ));

    const activity = await screen.findByTestId("worker-activity");
    activity.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    openFind();
    setFindQuery("WORKER_FIND_NEEDLE");
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length).toBeGreaterThanOrEqual(1);
  });
});

describe("find everywhere seed", () => {
  it("exposes trimmed controller query for search pane seeding", () => {
    openFind();
    setFindQuery("  seeded-query  ");
    expect(findEverywhereSeed()).toBe("seeded-query");
  });
});
