import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import {
  findController,
  findEverywhereSeed,
  findNext,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { createAppStore } from "../store/app-state.ts";
import { WorkerTranscript } from "../components/worker/WorkerTranscript.tsx";
import type { Message, WorkerTask } from "../api/types.ts";

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

  it("searches and reveals tool output across a virtualized worker transcript", async () => {
    const appStore = createAppStore();
    const rows: Message[] = Array.from({ length: 62 }, (_, index) => ({
      id: `m-${index}`,
      ord: index + 1,
      role: index % 2 === 0 ? ("user" as const) : ("assistant" as const),
      origin: index % 2 === 0 ? ("user" as const) : ("model" as const),
      authority: index % 2 === 0 ? ("user" as const) : ("none" as const),
      trust_tier: "trusted" as const,
      content: `step ${index}`,
      created_at: `2026-01-01T00:00:${String(index % 60).padStart(2, "0")}Z`,
    }));
    rows.unshift(
      {
        id: "a-tool",
        ord: 0,
        role: "assistant",
        origin: "model",
        authority: "none",
        trust_tier: "trusted",
        content: "",
        tool_calls: [{ id: "tc-find", name: "read", args: { path: "VIRTUAL_TOOL_NEEDLE.go" } }],
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "t-tool",
        ord: 0,
        role: "tool",
        origin: "tool",
        authority: "none",
        trust_tier: "untrusted",
        content: "VIRTUAL_TOOL_NEEDLE",
        tool_result: {
          content: "VIRTUAL_TOOL_NEEDLE",
          tool: "read",
          tool_call_id: "tc-find",
          assistant_message_id: "a-tool",
          tool_args: { path: "VIRTUAL_TOOL_NEEDLE.go" },
        },
        created_at: "2026-01-01T00:00:00Z",
      },
    );
    appStore.actions.applyWorkerTranscriptRows("job-1", rows);

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
    setFindQuery("VIRTUAL_TOOL_NEEDLE");
    await new Promise((resolve) => setTimeout(resolve, 160));
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    findNext();
    await Promise.resolve();
  });
});

describe("find everywhere seed", () => {
  it("exposes trimmed controller query for search pane seeding", () => {
    openFind();
    setFindQuery("  seeded-query  ");
    expect(findEverywhereSeed()).toBe("seeded-query");
  });
});
