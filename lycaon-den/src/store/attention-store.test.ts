import { stubClient } from "../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { AttentionRow, AttentionView } from "../api/types.ts";
import { createAttentionStore } from "./attention-store.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function row(over: Partial<AttentionRow> = {}): AttentionRow {
  return {
    session_id: "s1",
    project_id: "p1",
    class: "needs_you",
    reason: "checkpoint",
    since_at: "2026-07-27T12:00:00Z",
    ...over,
  };
}

function busWith(): {
  bus: { onAttentionEvent: (cb: (v: AttentionView) => void) => () => void };
  emit: (v: AttentionView) => void;
} {
  const listeners = new Set<(v: AttentionView) => void>();
  return {
    bus: {
      onAttentionEvent: (cb) => {
        listeners.add(cb);
        return () => listeners.delete(cb);
      },
    },
    emit: (v) => listeners.forEach((cb) => cb(v)),
  };
}

describe("attention store", () => {
  it("starts empty and unloaded", () => {
    const { bus } = busWith();
    const store = createAttentionStore(() => null, bus);
    expect(store.state.rows).toEqual([]);
    expect(store.state.loaded).toBe(false);
  });

  it("applies an SSE view and indexes it by session", () => {
    const { bus, emit } = busWith();
    const store = createAttentionStore(() => null, bus);
    emit({ rows: [row({ session_id: "a" }), row({ session_id: "b" })] });
    expect(store.state.rows).toHaveLength(2);
    expect(store.index().get("b")?.session_id).toBe("b");
    expect(store.state.loaded).toBe(true);
  });

  // The host ships the whole view every time, so a later event must replace
  // rather than merge — otherwise a resolved checkpoint would linger forever.
  it("replaces the previous view wholesale", () => {
    const { bus, emit } = busWith();
    const store = createAttentionStore(() => null, bus);
    emit({ rows: [row({ session_id: "a" }), row({ session_id: "b" })] });
    emit({ rows: [row({ session_id: "b" })] });
    expect(store.state.rows.map((r) => r.session_id)).toEqual(["b"]);
    expect(store.index().has("a")).toBe(false);
  });

  it("clears to empty when every session goes idle", () => {
    const { bus, emit } = busWith();
    const store = createAttentionStore(() => null, bus);
    emit({ rows: [row()] });
    emit({ rows: [] });
    expect(store.state.rows).toEqual([]);
    expect(store.index().size).toBe(0);
  });

  it("loads the snapshot from the client", async () => {
    const { bus } = busWith();
    const getAttention = vi.fn().mockResolvedValue({ rows: [row({ session_id: "z" })] });
    const client = stubClient({ getAttention });
    const store = createAttentionStore(() => client, bus);
    await store.load();
    expect(store.state.rows.map((r) => r.session_id)).toEqual(["z"]);
  });

  // The list is an affordance, not something the user asked for. Blanking the
  // queue on one flaky request would be worse than showing slightly stale rows.
  it("keeps the last known rows when the snapshot request fails", async () => {
    const { bus, emit } = busWith();
    const getAttention = vi.fn().mockRejectedValue(new Error("offline"));
    const client = stubClient({ getAttention });
    const store = createAttentionStore(() => client, bus);
    emit({ rows: [row({ session_id: "kept" })] });
    await store.load();
    expect(store.state.rows.map((r) => r.session_id)).toEqual(["kept"]);
  });

  it("drops rows for a deleted session or project immediately", () => {
    const { bus, emit } = busWith();
    const store = createAttentionStore(() => null, bus);
    emit({
      rows: [
        row({ session_id: "a", project_id: "p1" }),
        row({ session_id: "b", project_id: "p2" }),
      ],
    });
    store.dropSession("a");
    expect(store.state.rows.map((r) => r.session_id)).toEqual(["b"]);
    store.dropProject("p2");
    expect(store.state.rows).toEqual([]);
  });

  it("is a no-op without a client", async () => {
    const { bus } = busWith();
    const store = createAttentionStore(() => null, bus);
    await store.load();
    expect(store.state.loaded).toBe(false);
  });
  it("keeps a live approval when an older snapshot finishes", async () => {
    const { bus, emit } = busWith();
    const pending = deferred<AttentionView>();
    const client = stubClient({ getAttention: () => pending.promise });
    const store = createAttentionStore(() => client, bus);
    const loading = store.load();
    emit({ rows: [row()] });
    pending.resolve({ rows: [] });
    await loading;
    expect(store.state.rows).toEqual([row()]);
  });

  it("does not resurrect resolved approvals from a delayed snapshot", async () => {
    const { bus, emit } = busWith();
    const pending = deferred<AttentionView>();
    const client = stubClient({ getAttention: () => pending.promise });
    const store = createAttentionStore(() => client, bus);
    emit({ rows: [row()] });
    const loading = store.load();
    emit({ rows: [] });
    pending.resolve({ rows: [row()] });
    await loading;
    expect(store.state.rows).toEqual([]);
  });

  it.each(["clear", "dropSession", "dropProject"] as const)(
    "%s invalidates outstanding snapshots",
    async (operation) => {
      const { bus, emit } = busWith();
      const pending = deferred<AttentionView>();
      const client = stubClient({ getAttention: () => pending.promise });
      const store = createAttentionStore(() => client, bus);
      emit({ rows: [row()] });
      const loading = store.load();
      if (operation === "clear") store.clear();
      else if (operation === "dropSession") store.dropSession("s1");
      else store.dropProject("p1");
      pending.resolve({ rows: [row()] });
      await loading;
      expect(store.state.rows).toEqual([]);
    },
  );

  it("keeps the newest of overlapping snapshot requests", async () => {
    const { bus } = busWith();
    const older = deferred<AttentionView>();
    const newer = deferred<AttentionView>();
    const getAttention = vi.fn()
      .mockReturnValueOnce(older.promise)
      .mockReturnValueOnce(newer.promise);
    const client = stubClient({ getAttention });
    const store = createAttentionStore(() => client, bus);
    const first = store.load();
    const second = store.load();
    newer.resolve({ rows: [row()] });
    await second;
    older.resolve({ rows: [] });
    await first;
    expect(store.state.rows).toEqual([row()]);
  });

});
