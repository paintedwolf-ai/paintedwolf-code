import { afterEach, describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import {
  cancelWorkerTranscriptCoalesce,
  flushWorkerTranscriptCoalesce,
  queueWorkerTranscriptPatch,
  workerTranscriptRowsForDisplay,
} from "./worker-transcript-coalesce.ts";

afterEach(() => {
  cancelWorkerTranscriptCoalesce();
  vi.restoreAllMocks();
});

describe("worker-transcript-coalesce", () => {
  it("batches child worker transcript SSE patches in one microtask", async () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);

    expect(
      queueWorkerTranscriptPatch(store, "job-1", {
        id: "a1",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Hel",
        created_at: "t",
      }),
    ).toBe(true);
    queueWorkerTranscriptPatch(store, "job-1", {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "Hello",
      created_at: "t",
    });

    expect(store.state.workerTranscripts["job-1"]?.rows).toBeUndefined();

    await Promise.resolve();
    expect(store.state.workerTranscripts["job-1"]?.rows).toHaveLength(1);
    expect(store.state.workerTranscripts["job-1"]?.rows?.[0]?.content).toBe("Hello");
  });

  it("keeps a new batch scheduled when a canceled callback runs", async () => {
    const store = createAppStore();
    const schedule = vi.spyOn(globalThis, "queueMicrotask");
    const patch = (content: string) => queueWorkerTranscriptPatch(store, "job-1", {
      id: "a1", role: "assistant", origin: "model", authority: "none",
      trust_tier: "trusted", content, created_at: "t",
    });
    patch("discarded");
    cancelWorkerTranscriptCoalesce();
    queueMicrotask(() => patch("newest"));
    patch("new");

    await Promise.resolve();

    expect(schedule).toHaveBeenCalledTimes(3);
    expect(store.state.workerTranscripts["job-1"]?.rows?.map(row => row.content)).toEqual(["newest"]);
  });

  it("flushWorkerTranscriptCoalesce applies pending rows immediately", () => {
    const store = createAppStore();
    store.actions.setWorkers([
      {
        id: "job-1",
        parent_session_id: "parent-1",
        child_session_id: "child-1",
        agent_type: "implementer",
        status: "running",
        created_at: "t",
      },
    ]);

    queueWorkerTranscriptPatch(store, "job-1", {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "done",
      created_at: "t",
    });
    flushWorkerTranscriptCoalesce(store);
    expect(store.state.workerTranscripts["job-1"]?.rows?.[0]?.content).toBe("done");
  });

  it("cancelWorkerTranscriptCoalesce drops pending rows", () => {
    const store = createAppStore();
    queueWorkerTranscriptPatch(store, "job-1", {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "lost",
      created_at: "t",
    });
    cancelWorkerTranscriptCoalesce();
    flushWorkerTranscriptCoalesce(store);
    expect(store.state.workerTranscripts["job-1"]?.rows).toBeUndefined();
  });

  it("workerTranscriptRowsForDisplay reads pending rows before flush", () => {
    const store = createAppStore();
    queueWorkerTranscriptPatch(store, "job-1", {
      id: "a1",
      role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
      content: "streaming",
      created_at: "t",
    });
    expect(store.state.workerTranscripts["job-1"]?.rows).toBeUndefined();
    expect(workerTranscriptRowsForDisplay(store, "job-1")).toHaveLength(1);
    flushWorkerTranscriptCoalesce(store);
    expect(workerTranscriptRowsForDisplay(store, "job-1")).toHaveLength(1);
  });
});

describe("worker transcript writes stay proportional to the patch", () => {
  const row = (id: string, content: string) => ({
    id,
    role: "assistant" as const,
    origin: "model" as const,
    authority: "none" as const,
    trust_tier: "trusted" as const,
    content,
    created_at: "t",
  });

  it("appends without re-identifying the rows already committed", () => {
    const store = createAppStore();
    const settled = Array.from({ length: 200 }, (_, i) => row(`m${i}`, `row ${i}`));
    store.actions.applyWorkerTranscriptRows("job-1", settled);
    const before = store.state.workerTranscripts["job-1"]!.rows;
    const firstRow = before[0];

    queueWorkerTranscriptPatch(store, "job-1", row("new", "fresh"));
    flushWorkerTranscriptCoalesce(store);

    // Tail overflow rolls into an older page, which stays resident for scrollback.
    const after = store.state.workerTranscripts["job-1"]!.rows;
    expect(after).toHaveLength(201);
    expect(after[200]?.id).toBe("new");
    // Stable row identity preserves mounted content.
    expect(after[0]).toBe(firstRow);
  });

  it("patches one row in place and leaves its neighbours identical", () => {
    const store = createAppStore();
    store.actions.applyWorkerTranscriptRows("job-1", [
      row("a", "first"),
      row("b", "second"),
      row("c", "third"),
    ]);
    const before = store.state.workerTranscripts["job-1"]!.rows;
    const neighbours = [before[0], before[2]];

    queueWorkerTranscriptPatch(store, "job-1", row("b", "second edited"));
    flushWorkerTranscriptCoalesce(store);

    const after = store.state.workerTranscripts["job-1"]!.rows;
    expect(after[1]?.content).toBe("second edited");
    expect(after[0]).toBe(neighbours[0]);
    expect(after[2]).toBe(neighbours[1]);
  });

  it("collapses repeated patches for one row into a single committed write", () => {
    const store = createAppStore();
    store.actions.applyWorkerTranscriptRows("job-1", [row("a", "H")]);

    queueWorkerTranscriptPatch(store, "job-1", row("a", "He"));
    queueWorkerTranscriptPatch(store, "job-1", row("a", "Hel"));
    // The un-flushed view already reflects the newest patch.
    expect(workerTranscriptRowsForDisplay(store, "job-1")?.[0]?.content).toBe("Hel");

    flushWorkerTranscriptCoalesce(store);
    const after = store.state.workerTranscripts["job-1"]!.rows;
    expect(after).toHaveLength(1);
    expect(after[0]?.content).toBe("Hel");
  });

  it("ignores a patch that changes nothing", () => {
    const store = createAppStore();
    store.actions.applyWorkerTranscriptRows("job-1", [row("a", "same")]);
    expect(queueWorkerTranscriptPatch(store, "job-1", row("a", "same"))).toBe(false);
  });
});
