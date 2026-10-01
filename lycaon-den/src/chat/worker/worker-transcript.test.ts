import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { Message, WorkerTask } from "../../api/types.ts";
import {
  WorkerTranscriptRetention,
  fetchWorkerTranscriptTail,
  hydrateWorkerTranscripts,
  inflightWorkerPrefetchTargets,
  parentReferencedWorkerPrefetchTargets,
  latestWorkerForChildSession,
  workersNeedingTranscriptHydrate,
  workersColdCacheRevision,
  type WorkerTranscript,
} from "./worker-transcript.ts";
import { createAppStore } from "../../store/app-state.ts";
import { workerTranscriptFixture } from "../../test/worker-transcript-fixture.ts";

const worker: WorkerTask = {
  id: "job-1",
  agent_type: "implementer",
  status: "complete",
  created_at: "2020-01-01T00:00:00Z",
};

function parentTaskResult(): Message {
  return {
    id: "t-task",
    role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
    content: "",
    tool_result: {
      content: "",
      dispatch: { worker_id: "job-1" },
    },
    created_at: "t",
  };
}

describe("worker-transcript", () => {
  const assistant = (id: string, ord: number, seq = 1, content = id): Message => ({
    id, worker_id: "job-1", ord, seq, content, created_at: "t",
    role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
  });
  const tailPage = (messages: Message[], hasMoreBefore = false) => ({
    messages, ...(hasMoreBefore ? { before_cursor: "older" } : {}),
    watermark: 0, turn_clocks: {}, turn_loads: {},
  });

  it("leaves the resident transcript alone while the host cannot answer", async () => {
    const listSessionMessages = vi.fn(async () => {
      throw new Error("temporary backend failure");
    });
    const page = await fetchWorkerTranscriptTail(
      stubClient({ listSessionMessages }),
      { ...worker, child_session_id: "child-1" },
    );
    expect(page).toBeUndefined();
    expect(listSessionMessages).toHaveBeenCalledOnce();
  });

  it("keeps an empty selected run eligible for hydration retries", () => {
    const store = createAppStore();
    const selected = { ...worker, child_session_id: "child-1" };
    store.actions.applyWorkerTranscriptRows(selected.id, [assistant("live", 1)]);
    store.actions.installWorkerTranscriptTail(selected.id, tailPage([]));

    expect(store.state.workerTranscripts[selected.id]?.rows.map((row) => row.id)).toEqual(["live"]);
    expect(
      workersNeedingTranscriptHydrate([], [selected], store.state.workerTranscripts, {
        selectedWorkerId: selected.id,
      }),
    ).toEqual([selected]);
  });

  it("hydrates an evicted worker again without requiring selection", () => {
    const running: WorkerTask = { ...worker, status: "running", child_session_id: "child-1" };
    const retention = new WorkerTranscriptRetention();
    const cache: Record<string, WorkerTranscript> = {
      [running.id]: workerTranscriptFixture([assistant("m1", 1)]),
    };
    expect(workersNeedingTranscriptHydrate([], [running], cache)).toEqual([]);
    retention.evictions(cache, running.id);
    for (let i = 0; i < 32; i++) cache[`other-${i}`] = workerTranscriptFixture([assistant(`o${i}`, 1)]);
    const evicted = retention.evictions(cache, "other-31");
    expect(evicted).toEqual([running.id]);
    for (const id of evicted) delete cache[id];
    expect(workersNeedingTranscriptHydrate([], [running], cache)).toEqual([running]);
  });

  it("keeps an open reader's transcript resident past the cache budget", () => {
    const retention = new WorkerTranscriptRetention();
    const cache: Record<string, WorkerTranscript> = { read: workerTranscriptFixture([assistant("r", 1)]) };
    const release = retention.retain("read");
    retention.evictions(cache, "read");
    for (let i = 0; i < 32; i++) cache[`other-${i}`] = workerTranscriptFixture([assistant(`o${i}`, 1)]);
    expect(retention.evictions(cache, "other-31")).toEqual(["other-0"]);
    release();
    release();
    expect(retention.evictions(cache, "other-31")).toEqual(["read"]);
  });

  it("merges a durable tail page with a newer live revision of its rows", () => {
    const store = createAppStore();
    store.actions.applyWorkerTranscriptRows("job-1", [assistant("a2", 3, 2, "live draft")]);
    store.actions.installWorkerTranscriptTail(
      "job-1",
      tailPage([assistant("a1", 2, 1, "durable"), assistant("a2", 3, 1, "durable draft")], true),
    );
    const entry = store.state.workerTranscripts["job-1"];
    expect(entry?.hydrated).toBe(true);
    expect(entry?.window.hasMoreBefore).toBe(true);
    expect(entry?.rows.map((row) => [row.id, row.content])).toEqual([
      ["a1", "durable"],
      ["a2", "live draft"],
    ]);
  });

  it("requests the selected run when a child session is reused", async () => {
    const listSessionMessages = vi.fn(async () => tailPage([assistant("current", 1)]));
    await fetchWorkerTranscriptTail(
      stubClient({ listSessionMessages }),
      { ...worker, child_session_id: "child-shared" },
    );
    expect(listSessionMessages).toHaveBeenCalledWith("child-shared", { workerId: "job-1" });
  });

  it("uses the host roster for child notifications", () => {
    const rows = [
      { ...worker, id: "a", child_session_id: "child-a" },
      { ...worker, id: "b", child_session_id: "child-b" },
    ];
    expect(latestWorkerForChildSession(rows, "child-b")?.id).toBe("b");
    expect(latestWorkerForChildSession(rows, "missing")).toBeUndefined();
    expect(latestWorkerForChildSession(rows, "  ")).toBeUndefined();
  });

  it("chooses the newest run when a child is reused", () => {
    const rows = [
      {
        ...worker,
        id: "new",
        child_session_id: "child-shared",
        created_at: "2020-01-02T00:00:00Z",
      },
      { ...worker, id: "old", child_session_id: "child-shared" },
    ];
    expect(latestWorkerForChildSession(rows, "child-shared")?.id).toBe("new");
  });

  it("cold-cache revision includes child_session_id for hydrate scheduling", () => {
    const cache = {};
    const parent: Message[] = [];
    const before = workersColdCacheRevision(
      parent,
      [{ ...worker, id: "job-1", status: "pending" }],
      cache,
    );
    const after = workersColdCacheRevision(
      parent,
      [
        {
          ...worker,
          id: "job-1",
          status: "running",
          child_session_id: "child-1",
        },
      ],
      cache,
    );
    expect(before).not.toBe(after);
  });

  it("cold-cache revision ignores status-only worker SSE ticks", () => {
    const cache = {};
    const parent: Message[] = [];
    const pending = {
      ...worker,
      id: "job-run",
      status: "pending" as const,
      child_session_id: "child-1",
    };
    const running = { ...pending, status: "running" as const };
    const before = workersColdCacheRevision(parent, [pending], cache);
    const after = workersColdCacheRevision(parent, [running], cache);
    expect(before).toBe(after);
  });

  it("cold-cache revision tracks selected worker tool progress while cache is cold", () => {
    const running: WorkerTask = {
      ...worker,
      id: "job-run",
      status: "running",
      child_session_id: "child-1",
      tool_loops_used: 1,
    };
    const progressed = { ...running, tool_loops_used: 4 };
    const before = workersColdCacheRevision([], [running], {}, "job-run");
    const after = workersColdCacheRevision([], [progressed], {}, "job-run");
    expect(before).not.toBe(after);
  });

  it("cold-cache revision tracks drawer selection when open", () => {
    const running: WorkerTask = {
      ...worker,
      id: "job-a",
      status: "running",
      child_session_id: "child-a",
    };
    const other: WorkerTask = {
      ...worker,
      id: "job-b",
      status: "running",
      child_session_id: "child-b",
    };
    const revA = workersColdCacheRevision([], [running, other], {}, "job-a");
    const revB = workersColdCacheRevision([], [running, other], {}, "job-b");
    expect(revA).not.toBe(revB);
  });

  it("inflight hydrate targets running workers", () => {
    const inflight = {
      ...worker,
      id: "job-run",
      status: "running" as const,
      child_session_id: "child-run",
    };
    expect(inflightWorkerPrefetchTargets([inflight]).map((w) => w.id)).toEqual([
      "job-run",
    ]);
  });

  it("parent-referenced hydrate targets merged workers missing cached file edits", () => {
    const mergedWorker: WorkerTask = {
      ...worker,
      merge_status: "merged",
    };
    const parentMessages = [parentTaskResult()];
    expect(
      parentReferencedWorkerPrefetchTargets(
        parentMessages,
        [mergedWorker],
        {},
      ).map((w) => w.id),
    ).toEqual(["job-1"]);
  });

  it("parent-referenced hydrate skips workers before overlay lands on primary", () => {
    const parentMessages = [parentTaskResult()];
    expect(
      parentReferencedWorkerPrefetchTargets(parentMessages, [worker], {}).map(
        (w) => w.id,
      ),
    ).toEqual([]);
  });

  it("parent-referenced hydrate skips workers with warm transcripts", () => {
    const mergedWorker: WorkerTask = {
      ...worker,
      merge_status: "merged",
    };
    const parentMessages = [parentTaskResult()];
    expect(
      parentReferencedWorkerPrefetchTargets(
        parentMessages,
        [mergedWorker],
        {
        "job-1": workerTranscriptFixture([
          {
            id: "t-write",
            role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "wrote",
            tool_result: {
              content: "wrote",
              file_edit_preview: fileEditPreviewFixture({ path: "README.md", after: "new" }),
            },
            created_at: "t",
          },
        ], { hydrated: false }),
      }),
    ).toEqual([]);
  });

  it("parent-referenced hydrate skips warm transcripts with zero file edits", () => {
    // A warm read-only transcript already satisfies hydration.
    const mergedWorker: WorkerTask = {
      ...worker,
      merge_status: "merged",
    };
    const parentMessages = [parentTaskResult()];
    expect(
      parentReferencedWorkerPrefetchTargets(
        parentMessages,
        [mergedWorker],
        {
        "job-1": workerTranscriptFixture([
          {
            id: "t-run",
            role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: '{"Command":"pytest .","ExitCode":0}',
            created_at: "t",
          },
        ], { hydrated: false }),
      }),
    ).toEqual([]);
  });

  it("hydrates a warm cache once to reconcile the durable child history", () => {
    const running: WorkerTask = {
      ...worker,
      id: "job-run",
      status: "running",
      child_session_id: "child-1",
    };
    expect(
      workersNeedingTranscriptHydrate([], [running], {
        "job-run": workerTranscriptFixture(
          [{ id: "m1", role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const, content: "x", created_at: "t" }],
          { hydrated: false },
        ),
      }),
    ).toEqual([running]);
    expect(
      workersNeedingTranscriptHydrate([], [running], {}, {
        selectedWorkerId: "job-run",
      }).map((w) => w.id),
    ).toEqual(["job-run"]);
  });

  it("hydrate targets a selected completed worker with a child session", () => {
    const done: WorkerTask = {
      ...worker,
      id: "job-done",
      status: "complete",
      child_session_id: "child-done",
      tool_loops_used: 12,
    };
    expect(
      workersNeedingTranscriptHydrate([], [done], {}, {
        selectedWorkerId: "job-done",
      }).map((w) => w.id),
    ).toEqual(["job-done"]);
  });

  it("hydrate targets inflight workers when child session links", () => {
    const running: WorkerTask = {
      ...worker,
      id: "job-run",
      status: "running",
      child_session_id: "child-1",
    };
    expect(
      workersNeedingTranscriptHydrate([], [running], {}).map((w) => w.id),
    ).toEqual(["job-run"]);
  });

  it("hydrate loads cold child transcripts", async () => {
    const client = stubClient({
      listSessionMessages: vi.fn(async () => ({
        messages: [
          {
            id: "a1",
            worker_id: "job-1",
            role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "from child",
            created_at: "t",
          },
        ],
        watermark: 0,
      })),
    });
    const onLoaded = vi.fn();
    await hydrateWorkerTranscripts(
      client,
      [],
      [
        {
          ...worker,
          id: "job-1",
          status: "running",
          child_session_id: "child-1",
        },
      ],
      {},
      onLoaded,
      { selectedWorkerId: "job-1" },
    );
    expect(onLoaded).toHaveBeenCalledWith("job-1", expect.objectContaining({ messages: expect.any(Array) }));
  });

  it("hydrate loads parent-referenced merged workers", async () => {
    const client = stubClient({
      listSessionMessages: vi.fn(async () => ({
        messages: [
          {
            id: "t-write",
            worker_id: "job-1",
            role: "tool" as const, origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "wrote",
            tool_result: {
              content: "wrote",
              file_edit_preview: fileEditPreviewFixture({ path: "README.md", after: "new" }),
            },
            created_at: "t",
          },
        ],
        watermark: 0,
      })),
    });
    const onLoaded = vi.fn();
    await hydrateWorkerTranscripts(
      client,
      [parentTaskResult()],
      [{ ...worker, child_session_id: "child-1", merge_status: "merged" }],
      {},
      onLoaded,
    );
    expect(onLoaded).toHaveBeenCalledWith("job-1", expect.objectContaining({ messages: expect.any(Array) }));
  });
});
