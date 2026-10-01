import { stubClient } from "../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { CostSummary } from "../api/types.ts";
import { createCostStore } from "./cost-store.ts";

const summary: CostSummary = {
  scope: "session",
  session_id: "s1",
  estimated_nano_usd: 250_000_000,
  token_totals: { prompt: 100, completion: 20 },
  coordinator: { estimated_nano_usd: 200_000_000, token_totals: { prompt: 80, completion: 16 } },
  workers: { estimated_nano_usd: 50_000_000, token_totals: { prompt: 20, completion: 4 } },
  summarizer: { estimated_nano_usd: 0, token_totals: { prompt: 0, completion: 0 } },
  estimate_coverage: "complete",
  pricing_provenance: [],
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("createCostStore", () => {
  it("counts open cost surfaces and releases them once", () => {
    const store = createCostStore();
    expect(store.state.liveConsumers).toBe(0);

    const releasePopover = store.holdLiveRefresh();
    const releaseStage = store.holdLiveRefresh();
    expect(store.state.liveConsumers).toBe(2);

    releasePopover();
    releasePopover();
    expect(store.state.liveConsumers).toBe(1);

    releaseStage();
    expect(store.state.liveConsumers).toBe(0);
  });

  it("keeps live consumers across a summary clear", async () => {
    const store = createCostStore();
    const client = stubClient({
      getCostSummary: vi.fn(async () => summary),
    });
    await store.refreshSession(client, "s1");
    const release = store.holdLiveRefresh();

    store.actions.clear();

    expect(store.state.session).toBeUndefined();
    expect(store.state.liveConsumers).toBe(1);
    release();
  });

  it("fans invalidation out to surfaces that fetch independently", () => {
    const store = createCostStore();
    const stage = vi.fn();
    const unsubscribe = store.onInvalidated(stage);

    store.emitInvalidated();
    store.emitInvalidated();
    expect(stage).toHaveBeenCalledTimes(2);

    unsubscribe();
    store.emitInvalidated();
    expect(stage).toHaveBeenCalledTimes(2);
  });

  it("keeps the newest session when requests finish out of order", async () => {
    const first = deferred<CostSummary>();
    const second = deferred<CostSummary>();
    const client = stubClient({
      getCostSummary: vi.fn((sessionId: string) =>
        sessionId === "s1" ? first.promise : second.promise,
      ),
    });
    const store = createCostStore();

    const firstRefresh = store.refreshSession(client, "s1");
    const secondRefresh = store.refreshSession(client, "s2");
    expect(store.state.sessionId).toBe("s2");
    expect(store.state.session).toBeUndefined();

    second.resolve({ ...summary, session_id: "s2", estimated_nano_usd: 2_000_000_000 });
    await secondRefresh;
    first.resolve({ ...summary, session_id: "s1", estimated_nano_usd: 1_000_000_000 });
    await firstRefresh;

    expect(store.state.sessionId).toBe("s2");
    expect(store.state.session?.estimated_nano_usd).toBe(2_000_000_000);
    expect(store.state.loading).toBe(false);
  });

  it("does not restore an in-flight summary after clear", async () => {
    const pending = deferred<CostSummary>();
    const client = stubClient({
      getCostSummary: vi.fn(() => pending.promise),
    });
    const store = createCostStore();

    const refresh = store.refreshSession(client, "s1");
    store.actions.clear();
    pending.resolve(summary);
    await refresh;

    expect(store.state.sessionId).toBeUndefined();
    expect(store.state.session).toBeUndefined();
    expect(store.state.loading).toBe(false);
  });
});
